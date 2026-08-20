package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

func grokCleanCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "grok-clean.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.NotContains(t, string(data), "/Users/", "a grok capture must not publish the recorder's home")
	return data
}

func grokTruncatedCapture(t *testing.T) []byte {
	t.Helper()
	data := grokCleanCapture(t)
	cut := len(data) - 200
	require.Positive(t, cut)
	require.NotEqual(t, byte('\n'), data[cut-1], "the cut must land inside a line")
	return data[:cut]
}

func TestGrok_args(t *testing.T) {
	path := writeFixture(t, grokCleanCapture(t))
	var promptBody string
	var promptFile string
	base := fakeRunner("emit", path)
	runner := &mocks.CommandRunnerMock{
		CommandFunc: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if i := slices.Index(args, "--prompt-file"); i >= 0 && i+1 < len(args) {
				promptFile = args[i+1]
				data, err := os.ReadFile(promptFile) //nolint:gosec // path is the file this run just wrote
				require.NoError(t, err)
				promptBody = string(data)
			}
			return base.CommandFunc(ctx, name, args...)
		},
	}
	work := t.TempDir()
	g := executor.NewGrok(runner, executor.Opts{WorkDir: work})

	req := executor.Request{Prompt: "review this", Model: "grok-4.6", Effort: "high", Schema: json.RawMessage(`{"type":"object"}`)}
	_, err := g.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "grok", call.Name)

	args := call.Args
	assert.Equal(t, []string{
		"--no-leader", "--no-subagents", "--always-approve", "--no-auto-update",
		"--output-format", "streaming-messages-json",
		"--include-partial-messages",
		"--disallowed-tools", "write,search_replace",
	}, args[:9])
	assert.Equal(t, "--prompt-file", args[9])
	assert.Contains(t, promptBody, "review this")
	assert.Contains(t, promptBody, "narrate what you are doing")
	_, statErr := os.Stat(promptFile)
	require.Error(t, statErr, "the prompt file is removed after the run")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "grok-4.6")
	assert.Contains(t, args, "--effort")
	assert.Contains(t, args, "high")
	assert.Contains(t, args, "--json-schema")
	assert.Contains(t, args, `{"type":"object"}`)
	assert.Contains(t, args, "--cwd")
	assert.Contains(t, args, work)
	assert.NotContains(t, args, "-p", "the prompt is a file, not an argv positional")
	assert.False(t, slices.Contains(args, "--print"))
}

func TestGrok_args_promptNotOnStdin(t *testing.T) {
	g := executor.NewGrok(fakeRunner("echo", "-"), executor.Opts{})
	res, err := g.Run(context.Background(), executor.Request{
		Prompt: "SECRET_PROMPT", Schema: json.RawMessage(`{"type":"object"}`),
	}, discardSink())
	require.NoError(t, err)
	assert.NotContains(t, res.Raw, "SECRET_PROMPT", "grok headless does not read stdin as the prompt")
}

func TestGrok_Run_clean(t *testing.T) {
	path := writeFixture(t, grokCleanCapture(t))
	sink := discardSink()
	g := executor.NewGrok(fakeRunner("emit", path), executor.Opts{})

	res, err := g.Run(context.Background(), executor.Request{Prompt: "x", Model: "grok-4.6"}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.False(t, res.RateLimited)

	var out struct {
		OK bool `json:"ok"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	assert.True(t, out.OK)

	assert.Equal(t, "grok-4.6", res.RequestedModel)
	assert.Equal(t, "grok-4.6-build", res.ActualModel)
	assert.Positive(t, res.Tokens)
	assert.NotEmpty(t, sink.EmitCalls())
}

func TestGrok_Run_truncatedStream(t *testing.T) {
	path := writeFixture(t, grokTruncatedCapture(t))
	g := executor.NewGrok(fakeRunner("emit", path), executor.Opts{})

	res, err := g.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a malformed stream degrades rather than failing the run")
	assert.Empty(t, res.StructuredOutput, "the result event never arrived intact")
	assert.NotEmpty(t, res.Raw, "the partial stream is still available for the archive")
}

func TestGrok_Run_activityFromAssistant(t *testing.T) {
	path := writeFixture(t, grokCleanCapture(t))
	sink := discardSink()
	g := executor.NewGrok(fakeRunner("emit", path), executor.Opts{})

	_, err := g.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	var texts []string
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == executor.EventActivity {
			texts = append(texts, call.Event.Text)
		}
	}
	require.NotEmpty(t, texts)
	assert.True(t, strings.Contains(texts[0], "ok") || strings.Contains(texts[0], "{"),
		"the assistant text block is activity, not thinking")
}
