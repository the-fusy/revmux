package pipeline

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/revmux/app/executor"
)

func TestQuotaExhausted(t *testing.T) {
	tests := []struct {
		name string
		res  executor.Result
		err  error
		want bool
	}{
		{"claude rate_limit rejected", executor.Result{RateLimited: true, RateLimit: executor.RateLimitInfo{Status: "rejected"}}, nil, true},
		{"credit balance", executor.Result{Raw: "Credit balance is too low\n"}, nil, true},
		{"out of extra usage", executor.Result{Raw: `API Error: 400 {"message":"You're out of extra usage. Add more and keep going."}`}, nil, true},
		{"you've hit your limit", executor.Result{}, errors.New("You've hit your limit"), true},
		{"codex usage limit", executor.Result{Raw: "ERROR: You've hit your usage limit.\n"}, nil, true},
		{"quota exceeded", executor.Result{Raw: "quota exceeded"}, nil, true},
		{"insufficient_quota", executor.Result{Raw: "insufficient_quota"}, nil, true},
		{"stall is not quota", executor.Result{IdleTimedOut: true}, nil, false},
		{"exit 1 is not quota", executor.Result{ExitCode: 1}, nil, false},
		{"findings mentioning quota are not themselves a limit", executor.Result{StructuredOutput: []byte(`{"title":"you've hit your usage limit"}`)}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, quotaExhausted(tt.res, tt.err))
		})
	}
}

func TestApplyQuotaFallback(t *testing.T) {
	limited := executor.Result{RateLimited: true}
	t.Run("claude opus becomes cursor thinking", func(t *testing.T) {
		got, ok := applyQuotaFallback(RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}, limited, nil)
		assert.True(t, ok)
		assert.Equal(t, RunnerSpec{Executor: "cursor-agent", Model: "claude-opus-5-thinking", Effort: "high"}, got)
	})
	t.Run("codex sol keeps the slug", func(t *testing.T) {
		got, ok := applyQuotaFallback(RunnerSpec{Executor: "codex", Model: "gpt-5.6-sol", Effort: "high"}, limited, nil)
		assert.True(t, ok)
		assert.Equal(t, "cursor-agent", got.Executor)
		assert.Equal(t, "gpt-5.6-sol", got.Model)
	})
	t.Run("cursor does not switch", func(t *testing.T) {
		in := RunnerSpec{Executor: "cursor-agent", Model: "cursor-grok-4.6", Effort: "high"}
		got, ok := applyQuotaFallback(in, limited, nil)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
	t.Run("a stall does not switch", func(t *testing.T) {
		in := RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}
		got, ok := applyQuotaFallback(in, executor.Result{IdleTimedOut: true}, nil)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
}
