package executor

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Grok runs the grok CLI in headless mode and decodes its streaming-messages-json output.
// That stream is the Anthropic Messages wire format, so the same parser as Claude reads it.
// The prompt cannot go on stdin: grok headless ignores piped input and takes --prompt-file.
type Grok struct {
	proc
}

// NewGrok builds a grok executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewGrok(runner CommandRunner, opts Opts) *Grok {
	return &Grok{proc: newProc("grok", runner, opts)}
}

// Run writes the prompt to a temp file, executes one request, and reports what happened. A non-zero
// exit or an idle timeout comes back on the Result, not as an error — whether that degrades the
// source is the pipeline's call.
func (g *Grok) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	prompt := req.Prompt + ClaudeNarrationContract(req.Schema)
	promptFile, err := g.writePrompt(prompt)
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(promptFile) //nolint:errcheck // a leftover temp file is not a failed review

	spec := runSpec{
		argv: g.args(req, promptFile),
		sink: sink,
		parse: func(ctx context.Context, r io.Reader) Result {
			return parseMessagesStream(&g.proc, ctx, r, sink)
		},
	}
	// grok headless ignores piped stdin; emptying Prompt keeps the composed text off argv and the pipe
	req.Prompt = ""
	res, err := g.run(ctx, req, spec)
	res.RequestedModel = req.Model
	return res, err
}

func (g *Grok) writePrompt(prompt string) (string, error) {
	f, err := os.CreateTemp("", "revmux-grok-*.txt")
	if err != nil {
		return "", fmt.Errorf("create grok prompt file: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write grok prompt file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close grok prompt file: %w", err)
	}
	return path, nil
}

// args builds the invocation. --include-partial-messages is there for the idle watchdog, the same
// reason Claude carries it: without the deltas a large StructuredOutput call arrives as one line
// written only once it is complete.
//
// --prompt-file is required. grok -p puts the prompt on argv, and a composed lens blows past the
// Windows command-line cap; grok also does not read piped stdin into the prompt.
//
// --always-approve is the measured headless permission mode. --permission-mode auto exists but
// collapses to default on the init line; default in headless would hang on a confirmation.
// --disallowed-tools keeps the edit tools out of context. write and search_replace are the grok
// names; do not grow this into a shell denylist — a redirect can still write.
//
// --no-leader so a review launched from an interactive grok session does not attach to that
// session's leader. --no-subagents is the same role as claude's --disable-slash-commands.
func (g *Grok) args(req Request, promptFile string) []string {
	argv := []string{
		"--no-leader",
		"--no-subagents",
		"--always-approve",
		"--no-auto-update",
		"--output-format", "streaming-messages-json",
		"--include-partial-messages",
		"--disallowed-tools", "write,search_replace",
		"--prompt-file", promptFile,
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.Effort != "" {
		argv = append(argv, "--effort", req.Effort)
	}
	if len(req.Schema) > 0 {
		argv = append(argv, "--json-schema", string(req.Schema))
	}
	if g.opts.WorkDir != "" {
		argv = append(argv, "--cwd", g.opts.WorkDir)
	}
	return argv
}
