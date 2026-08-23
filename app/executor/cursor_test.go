package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

func cursorCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cursor-clean.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

func TestCursor_args(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	runner := fakeRunner("emit", path)
	workdir := t.TempDir()
	c := executor.NewCursor(runner, executor.Opts{WorkDir: workdir})

	req := executor.Request{Prompt: "review this", Model: "cursor-grok-4.6", Effort: "high", Schema: json.RawMessage(`{"type":"object"}`)}
	_, err := c.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "cursor-agent", call.Name)
	assert.Equal(t, []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--mode", "ask",
		"--force",
		"--sandbox", "disabled",
		"--trust",
		"--model", "cursor-grok-4.6-high",
		"--workspace", workdir,
	}, call.Args)
	assert.NotContains(t, call.Args, "--yolo")
}

func TestCursor_args_modelAlreadyHasEffort(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))

	tests := []struct {
		name, model, effort, want string
	}{
		{"stem plus effort", "cursor-grok-4.6", "high", "cursor-grok-4.6-high"},
		{"already ends in effort", "cursor-grok-4.6-high", "high", "cursor-grok-4.6-high"},
		{"fast suffix after effort", "cursor-grok-4.6-high-fast", "high", "cursor-grok-4.6-high"},
		{"xhigh slug plus inherited high", "cursor-grok-4.6-xhigh", "high", "cursor-grok-4.6-xhigh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakeRunner("emit", path)
			c := executor.NewCursor(runner, executor.Opts{})
			_, err := c.Run(context.Background(), executor.Request{
				Prompt: "x", Model: tt.model, Effort: tt.effort,
			}, discardSink())
			require.NoError(t, err)
			args := runner.CommandCalls()[0].Args
			assert.Contains(t, args, tt.want)
			assert.NotContains(t, args, tt.want+"-"+tt.effort)
			assert.NotContains(t, args, tt.want+"-high")
		})
	}
}

func TestCursor_args_neverFast(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	tests := []struct {
		name, model, effort, want string
	}{
		{"catalog fast after high", "cursor-grok-4.6-high-fast", "high", "cursor-grok-4.6-high"},
		{"fast stem plus effort", "cursor-grok-4.6-fast", "high", "cursor-grok-4.6-high"},
		{"thinking-high-fast", "claude-opus-5-thinking-high-fast", "", "claude-opus-5-thinking-high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakeRunner("emit", path)
			c := executor.NewCursor(runner, executor.Opts{})
			_, err := c.Run(context.Background(), executor.Request{
				Prompt: "x", Model: tt.model, Effort: tt.effort,
			}, discardSink())
			require.NoError(t, err)
			args := runner.CommandCalls()[0].Args
			assert.Contains(t, args, tt.want)
			for _, a := range args {
				assert.NotContains(t, a, "-fast")
			}
		})
	}
}

func TestCursor_args_optionalFlagsOmitted(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)

	args := runner.CommandCalls()[0].Args
	assert.NotContains(t, args, "--model")
	assert.NotContains(t, args, "--workspace")
}

func TestCursor_thinkingDeltasAreCoalescedProgress(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "cursor-think-burst.jsonl"))
	require.NoError(t, err)
	clk := &mocks.ClockMock{
		NowFunc:       func() time.Time { return time.Unix(0, 0) },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer { panic("idle timer unused") },
	}
	sink := discardSink()
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, data)), executor.Opts{Clock: clk})
	_, err = c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	var activity, progress []string
	for _, call := range sink.EmitCalls() {
		switch call.Event.Kind {
		case executor.EventActivity:
			activity = append(activity, call.Event.Text)
		case executor.EventProgress:
			progress = append(progress, call.Event.Text)
		case executor.EventInfo, executor.EventRateLimit, executor.EventFinished:
		}
	}
	assert.Empty(t, activity, "token fragments must not become log lines")
	require.NotEmpty(t, progress)
	assert.Contains(t, strings.Join(progress, "\n"), "Reading the diff next")
	assert.Contains(t, strings.Join(progress, "\n"), "read app/foo.go")
	assert.Less(t, len(progress), 6, "a burst at one clock instant is one flush plus the tool")
}

func TestCursor_Run_clean(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	sink := discardSink()
	c := executor.NewCursor(fakeRunner("emit", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "cursor-grok-4.6"}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.Equal(t, "cursor-grok-4.6", res.RequestedModel)
	assert.Equal(t, "Cursor Grok 4.6 High", res.ActualModel)
	assert.Equal(t, 13400, res.Tokens)

	var out struct {
		Findings []struct {
			File     string   `json:"file"`
			Severity string   `json:"severity"`
			Lenses   []string `json:"lenses"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "app/foo.go", out.Findings[0].File)
	assert.Equal(t, "minor", out.Findings[0].Severity)
	assert.Equal(t, []string{"bugs"}, out.Findings[0].Lenses)

	texts := make([]string, 0, len(sink.EmitCalls()))
	for _, call := range sink.EmitCalls() {
		texts = append(texts, call.Event.Text)
	}
	joined := strings.Join(texts, "\n")
	assert.Contains(t, joined, "reading the diff")
	assert.Contains(t, joined, "read app/foo.go")
}

func TestCursor_Run_jsonWrappedInProse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "cursor-wrapped.jsonl"))
	require.NoError(t, err)
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, data)), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Contains(t, string(res.StructuredOutput), `"file":"app/foo.go"`)
}

func TestCursor_Run_truncatedStream(t *testing.T) {
	data := cursorCapture(t)
	cut := len(data) - 80
	require.Positive(t, cut)
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, data[:cut])), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a truncated stream degrades rather than failing the run")
	assert.Empty(t, res.StructuredOutput)
	assert.NotEmpty(t, res.Raw)
}

func TestCursor_appendsOutputContract(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	c := executor.NewCursor(fakeRunner("echo", path), executor.Opts{})

	res, err := c.Run(context.Background(), executor.Request{
		Prompt: "review this", Schema: json.RawMessage(`{"type":"object"}`),
	}, discardSink())
	require.NoError(t, err)
	assert.Contains(t, res.Raw, "Return ONLY a JSON object")
}

func TestCursor_toolProgressIgnoresMetadataKeys(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "cursor-tool-metadata.jsonl"))
	require.NoError(t, err)
	sink := discardSink()
	c := executor.NewCursor(fakeRunner("emit", writeFixture(t, data)), executor.Opts{})
	_, err = c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	var progress []string
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == executor.EventProgress {
			progress = append(progress, call.Event.Text)
		}
	}
	joined := strings.Join(progress, "\n")
	assert.Contains(t, joined, "read app/foo.go")
	assert.NotContains(t, joined, "toolCallId")
	assert.NotContains(t, joined, "startedAtMs")
	assert.NotContains(t, joined, "hookAdditionalContexts")
}

func TestCursor_forceWithAskMode(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	args := runner.CommandCalls()[0].Args
	assert.Contains(t, args, "--force")
	assert.Contains(t, args, "--mode")
	assert.Contains(t, args, "ask")
	assert.NotContains(t, args, "--yolo")
}
