package pipeline

import (
	"strings"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/prompt"
)

// quotaPhrases is the closed set that means this binary will not serve another request.
// Claude: error reference at code.claude.com/docs/en/errors (2026-08-18) — "Credit balance is too
// low", request rejected 429 — plus the extra-usage 400 body "You're out of extra usage" and the
// CLI line "You've hit your limit".
// Codex: the same limit tier already classified in app/executor/codex.go (codexLimitPatterns).
// Matching is case-insensitive substring on the failed attempt's tail, never the whole findings body.
var quotaPhrases = []string{
	"credit balance is too low",
	"you're out of extra usage",
	"out of extra usage",
	"you've hit your limit",
	"you've hit your usage limit",
	"quota exceeded",
	"insufficient_quota",
}

// quotaExhausted reports whether this attempt is a spent meter, not a stall or a parse miss.
func quotaExhausted(res executor.Result, err error) bool {
	if res.RateLimited {
		return true
	}
	var b strings.Builder
	b.WriteString(res.Raw)
	b.WriteByte('\n')
	b.WriteString(res.RateLimit.Status)
	b.WriteByte('\n')
	b.WriteString(res.RateLimit.RateLimitType)
	if err != nil {
		b.WriteByte('\n')
		b.WriteString(err.Error())
	}
	blob := strings.ToLower(b.String())
	for _, p := range quotaPhrases {
		if strings.Contains(blob, p) {
			return true
		}
	}
	return false
}

// applyQuotaFallback rewrites a claude or codex spec onto cursor when the last attempt was a
// spent meter. A cursor spec, or any other fault, is left alone.
func applyQuotaFallback(spec RunnerSpec, res executor.Result, err error) (RunnerSpec, bool) {
	switch spec.Executor {
	case "claude", "codex":
	default:
		return spec, false
	}
	if !quotaExhausted(res, err) {
		return spec, false
	}
	spec.Model = cursorFallbackModel(spec.Executor, spec.Model)
	spec.Executor = executorCursor
	return spec, true
}

func applyQuotaFallbackSpec(spec prompt.AgentSpec, res executor.Result, err error) (prompt.AgentSpec, bool) {
	next, ok := applyQuotaFallback(RunnerSpec{Executor: spec.Executor, Model: spec.Model, Effort: spec.Effort}, res, err)
	if !ok {
		return spec, false
	}
	spec.Executor, spec.Model, spec.Effort = next.Executor, next.Model, next.Effort
	return spec, true
}

// cursorFallbackModel is the cursor-agent slug for a claude or codex model. Short names the
// shipped profiles use become the thinking-line catalog ids; a name that is already a slug
// is kept. -fast is stripped later by cursorModelSlug.
func cursorFallbackModel(exec, model string) string {
	model = strings.TrimSpace(model)
	switch exec {
	case "claude":
		switch model {
		case "", "opus":
			return "claude-opus-5-thinking"
		case "sonnet":
			return "claude-sonnet-5-thinking"
		case "fable":
			return "claude-fable-5-thinking"
		default:
			return model
		}
	case "codex":
		if model == "" {
			return "gpt-5.6-sol"
		}
		return model
	}
	return model
}
