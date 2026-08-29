package pipeline

import (
	"errors"
	"strings"
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
		{"stall whose tee quotes a limit phrase is not quota", executor.Result{IdleTimedOut: true, Raw: "quota exceeded\n"}, nil, false},
		{"rate-limited stall is still quota", executor.Result{RateLimited: true, IdleTimedOut: true}, nil, true},
		{"phrase only in the prefix of a long tee is not quota", executor.Result{Raw: "quota exceeded\n" + strings.Repeat("x", 3000)}, nil, false},
		{"phrase in the tail of a long tee is quota", executor.Result{Raw: strings.Repeat("x", 3000) + "\nquota exceeded"}, nil, true},
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
		got, ok := applyQuotaFallback(nil, RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}, limited, nil)
		assert.True(t, ok)
		assert.Equal(t, RunnerSpec{Executor: "cursor-agent", Model: "claude-opus-5-thinking", Effort: "high"}, got)
	})
	t.Run("codex sol keeps the slug", func(t *testing.T) {
		got, ok := applyQuotaFallback(nil, RunnerSpec{Executor: "codex", Model: "gpt-5.6-sol", Effort: "high"}, limited, nil)
		assert.True(t, ok)
		assert.Equal(t, "cursor-agent", got.Executor)
		assert.Equal(t, "gpt-5.6-sol", got.Model)
	})
	t.Run("grok becomes cursor grok", func(t *testing.T) {
		got, ok := applyQuotaFallback(nil, RunnerSpec{Executor: "grok", Model: "grok-4.6", Effort: "high"}, limited, nil)
		assert.True(t, ok)
		assert.Equal(t, RunnerSpec{Executor: "cursor-agent", Model: "cursor-grok-4.6", Effort: "high"}, got)
	})
	t.Run("cursor does not switch", func(t *testing.T) {
		in := RunnerSpec{Executor: "cursor-agent", Model: "cursor-grok-4.6", Effort: "high"}
		got, ok := applyQuotaFallback(nil, in, limited, nil)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
	t.Run("a stall does not switch", func(t *testing.T) {
		in := RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}
		got, ok := applyQuotaFallback(nil, in, executor.Result{IdleTimedOut: true}, nil)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
	t.Run("a stall whose tee quotes a limit phrase does not switch", func(t *testing.T) {
		in := RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}
		got, ok := applyQuotaFallback(nil, in, executor.Result{IdleTimedOut: true, Raw: "quota exceeded"}, nil)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
	t.Run("cursor spend off blocks the fallback", func(t *testing.T) {
		spend := &Spend{Claude: true, Codex: true, Cursor: false, Grok: true}
		got, ok := applyQuotaFallback(spend, RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}, limited, nil)
		assert.False(t, ok)
		assert.Equal(t, "claude", got.Executor)
	})
}

func TestApplySpend(t *testing.T) {
	t.Run("nil spend does not rewrite", func(t *testing.T) {
		in := RunnerSpec{Executor: "grok", Model: "grok-4.6", Effort: "high"}
		got, ok := applySpend(nil, in)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
	t.Run("grok off rewrites onto cursor", func(t *testing.T) {
		spend := &Spend{Claude: true, Codex: true, Cursor: true, Grok: false}
		got, ok := applySpend(spend, RunnerSpec{Executor: "grok", Model: "grok-4.6", Effort: "high"})
		assert.True(t, ok)
		assert.Equal(t, RunnerSpec{Executor: "cursor-agent", Model: "cursor-grok-4.6", Effort: "high"}, got)
	})
	t.Run("grok on leaves the spec", func(t *testing.T) {
		spend := &Spend{Claude: true, Codex: true, Cursor: true, Grok: true}
		in := RunnerSpec{Executor: "grok", Model: "grok-4.6", Effort: "high"}
		got, ok := applySpend(spend, in)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
	t.Run("claude off rewrites onto cursor", func(t *testing.T) {
		spend := &Spend{Claude: false, Codex: true, Cursor: true, Grok: false}
		got, ok := applySpend(spend, RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"})
		assert.True(t, ok)
		assert.Equal(t, "cursor-agent", got.Executor)
		assert.Equal(t, "claude-opus-5-thinking", got.Model)
	})
	t.Run("cursor off does not rewrite a grok spec onto cursor", func(t *testing.T) {
		spend := &Spend{Claude: true, Codex: true, Cursor: false, Grok: false}
		in := RunnerSpec{Executor: "grok", Model: "grok-4.6", Effort: "high"}
		got, ok := applySpend(spend, in)
		assert.False(t, ok)
		assert.Equal(t, in, got)
	})
}
