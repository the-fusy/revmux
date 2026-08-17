package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
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
		"--sandbox", "disabled",
		"--trust",
		"--model", "cursor-grok-4.6-high",
		"--workspace", workdir,
	}, call.Args)
	assert.NotContains(t, call.Args, "--force")
	assert.NotContains(t, call.Args, "--yolo")
}

func TestCursor_args_modelAlreadyHasEffort(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{
		Prompt: "x", Model: "cursor-grok-4.6-high", Effort: "high",
	}, discardSink())
	require.NoError(t, err)

	args := runner.CommandCalls()[0].Args
	assert.Contains(t, args, "cursor-grok-4.6-high")
	assert.NotContains(t, args, "cursor-grok-4.6-high-high")
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

	texts := []string{}
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

func TestCursor_noForceOnPrint(t *testing.T) {
	path := writeFixture(t, cursorCapture(t))
	runner := fakeRunner("emit", path)
	c := executor.NewCursor(runner, executor.Opts{})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	for _, a := range runner.CommandCalls()[0].Args {
		assert.NotEqual(t, "--force", a)
		assert.NotEqual(t, "--yolo", a)
	}
}
