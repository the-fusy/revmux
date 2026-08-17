package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Cursor runs the cursor-agent CLI in print mode and decodes its stream-json output.
// Effort is folded into the model slug (`cursor-grok-4.6` + high → `cursor-grok-4.6-high`)
// because the CLI has no separate --effort flag.
type Cursor struct {
	proc
}

// NewCursor builds a cursor-agent executor. A nil Opts.Clock is filled with the production clock,
// because the composition root assembles Opts from flags that carry no clock at all.
func NewCursor(runner CommandRunner, opts Opts) *Cursor {
	return &Cursor{proc: newProc("cursor-agent", runner, opts)}
}

// Run executes one request. A non-zero exit or an idle timeout comes back on the Result rather than as
// an error. There is no --json-schema, so the output contract is appended to the prompt and the answer
// is extracted from the result event the same way Codex extracts from prose.
func (c *Cursor) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += CursorOutputContract(req.Schema)
	spec := runSpec{
		argv:  c.args(req),
		sink:  sink,
		parse: func(ctx context.Context, r io.Reader) Result { return c.parseStream(ctx, r, sink) },
	}
	res, err := c.run(ctx, req, spec)
	res.RequestedModel = req.Model
	if err != nil {
		return res, err
	}
	if len(res.StructuredOutput) == 0 {
		if out, exErr := extractJSONObject(res.Raw); exErr == nil {
			res.StructuredOutput = out
		}
	}
	return res, nil
}

// CursorOutputContract is cursor-agent's substitute for claude's --json-schema, appended to every
// prompt Run dispatches. Exported because Run appends it after the caller archived the composed prompt,
// and an archived prompt missing it describes a run that did not happen.
func CursorOutputContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return "\n\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\nSchema:\n" +
		string(schema) + "\n"
}

func (c *Cursor) args(req Request) []string {
	// ask + sandbox + no --force: the CLI's --print path otherwise has write and shell.
	argv := []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--mode", "ask",
		"--sandbox", "enabled",
		"--trust",
	}
	if slug := cursorModelSlug(req.Model, req.Effort); slug != "" {
		argv = append(argv, "--model", slug)
	}
	if c.opts.WorkDir != "" {
		argv = append(argv, "--workspace", c.opts.WorkDir)
	}
	return argv
}

// cursorModelSlug joins the roster model and effort the way cursor-agent names them.
// A model that already ends in -<effort> is left alone, so `cursor-grok-4.6-high` + high
// does not become `cursor-grok-4.6-high-high`.
func cursorModelSlug(model, effort string) string {
	if model == "" {
		return ""
	}
	if effort == "" {
		return model
	}
	suffix := "-" + effort
	if strings.HasSuffix(model, suffix) {
		return model
	}
	return model + suffix
}

func (c *Cursor) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result {
	res := Result{}
	_ = c.readLines(ctx, r, func(line string) {
		ev, ok := c.event(line)
		if !ok {
			return
		}
		switch ev.Type {
		case "system":
			if ev.Model != "" {
				res.ActualModel = ev.Model
			}
		case "thinking":
			if ev.Subtype == "delta" && ev.Text != "" {
				c.emit(sink, Event{Kind: EventActivity, Text: flattenLines(ev.Text)})
			}
		case "assistant":
			if text := ev.activity(); text != "" {
				c.emit(sink, Event{Kind: EventActivity, Text: text})
			}
		case "tool_call":
			if ev.Subtype == "started" {
				if note := cursorToolProgress(ev.ToolCall); note != "" {
					c.emit(sink, Event{Kind: EventProgress, Text: note})
				}
			}
		case "result":
			res.Tokens = ev.cursorTokens()
			if ev.Result != "" {
				if out, err := extractJSONObject(ev.Result); err == nil {
					res.StructuredOutput = out
				}
			}
		}
	})
	return res
}

func (c *Cursor) event(line string) (cursorEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return cursorEvent{}, false
	}
	var ev cursorEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return cursorEvent{}, false
	}
	return ev, ev.Type != ""
}

// cursorEvent is one line of cursor-agent stream-json. The field names follow that CLI, not claude's.
type cursorEvent struct {
	Type     string          `json:"type"`
	Subtype  string          `json:"subtype"`
	Text     string          `json:"text"`
	Model    string          `json:"model"`
	Result   string          `json:"result"`
	Usage    *cursorUsage    `json:"usage"`
	Message  *streamMessage  `json:"message"`
	ToolCall json.RawMessage `json:"tool_call"`
}

type cursorUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
}

func (e cursorEvent) cursorTokens() int {
	if e.Usage == nil {
		return 0
	}
	return e.Usage.InputTokens + e.Usage.OutputTokens + e.Usage.CacheReadTokens + e.Usage.CacheWriteTokens
}

func (e cursorEvent) activity() string {
	if e.Message == nil {
		return ""
	}
	for _, b := range e.Message.Content {
		if b.Type != "text" {
			continue
		}
		text := flattenLines(b.Text)
		if text == "" {
			continue
		}
		return clampRunes(text)
	}
	return ""
}

// cursorToolProgress names the tool that just started, plus the path or command it is acting on.
// The payload is keyed by kind (`readToolCall`, …) rather than a flat name field.
func cursorToolProgress(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for k, v := range m {
		if k == "hookAdditionalContexts" {
			continue
		}
		name := strings.TrimSuffix(k, "ToolCall")
		if name == "" {
			continue
		}
		var body struct {
			Args map[string]any `json:"args"`
		}
		_ = json.Unmarshal(v, &body)
		if p, ok := body.Args["path"].(string); ok && strings.TrimSpace(p) != "" {
			return name + " " + shortPathLastTwo(p)
		}
		if cmd, ok := body.Args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
			return name + " " + clampRunes(flattenLines(cmd))
		}
		return name
	}
	return ""
}

func shortPathLastTwo(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// extractJSONObject pulls the first complete JSON object out of text that may carry prose around it.
func extractJSONObject(raw string) (json.RawMessage, error) {
	for i, ch := range raw {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		err := json.NewDecoder(strings.NewReader(raw[i:])).Decode(&out)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil, errors.New("no JSON object in cursor-agent output")
}
