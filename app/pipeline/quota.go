package pipeline

import (
	"strings"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/prompt"
)

// Spend is the per-binary spawn gate. A disabled binary is not launched: a grok/claude/codex spec
// is rewritten onto cursor-agent when cursor itself is allowed, and a spent meter of those three
// only retries on cursor when Spend.Cursor is true. The roster is not rewritten — a profile that
// never names grok never spends grok quota, however the flags are set.
type Spend struct {
	Claude bool
	Codex  bool
	Cursor bool
	Grok   bool
}

func (s *Spend) allowed(exec string) bool {
	if s == nil {
		return true
	}
	switch exec {
	case "claude":
		return s.Claude
	case "codex":
		return s.Codex
	case executorCursor:
		return s.Cursor
	case executorGrok:
		return s.Grok
	default:
		return true
	}
}

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
	if res.IdleTimedOut {
		return false
	}
	var b strings.Builder
	b.WriteString(executor.OutputTail(res.Raw))
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

// applySpend rewrites a spec whose binary is not allowed onto cursor-agent. Nil Spend is a no-op,
// so tests that do not set it keep the authored runner. A spec that is still not allowed after the
// rewrite — cursor itself disabled, and this spec was cursor or had nowhere to go — is returned
// unchanged; the caller degrades rather than spawning the disabled binary.
func applySpend(spend *Spend, spec RunnerSpec) (RunnerSpec, bool) {
	if spend == nil || spend.allowed(spec.Executor) {
		return spec, false
	}
	if spec.Executor != executorCursor && spend.allowed(executorCursor) {
		spec.Model = cursorFallbackModel(spec.Executor, spec.Model)
		spec.Executor = executorCursor
		return spec, true
	}
	return spec, false
}

func applySpendSpec(spend *Spend, spec prompt.AgentSpec) (prompt.AgentSpec, bool) {
	next, ok := applySpend(spend, RunnerSpec{Executor: spec.Executor, Model: spec.Model, Effort: spec.Effort})
	if !ok {
		return spec, false
	}
	spec.Executor, spec.Model, spec.Effort = next.Executor, next.Model, next.Effort
	return spec, true
}

// applyQuotaFallback rewrites a claude, codex or grok spec onto cursor when the last attempt was a
// spent meter. A cursor spec, a disabled cursor spend, or any other fault, is left alone.
func applyQuotaFallback(spend *Spend, spec RunnerSpec, res executor.Result, err error) (RunnerSpec, bool) {
	switch spec.Executor {
	case "claude", "codex", executorGrok:
	default:
		return spec, false
	}
	if spend != nil && !spend.allowed(executorCursor) {
		return spec, false
	}
	if !quotaExhausted(res, err) {
		return spec, false
	}
	spec.Model = cursorFallbackModel(spec.Executor, spec.Model)
	spec.Executor = executorCursor
	return spec, true
}

func applyQuotaFallbackSpec(spend *Spend, spec prompt.AgentSpec, res executor.Result, err error) (prompt.AgentSpec, bool) {
	next, ok := applyQuotaFallback(spend, RunnerSpec{Executor: spec.Executor, Model: spec.Model, Effort: spec.Effort}, res, err)
	if !ok {
		return spec, false
	}
	spec.Executor, spec.Model, spec.Effort = next.Executor, next.Model, next.Effort
	return spec, true
}

// cursorFallbackModel is the cursor-agent slug for a claude, codex or grok model. Short names the
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
	case executorGrok:
		switch model {
		case "", "grok-4.6":
			return "cursor-grok-4.6"
		case "grok-4.5":
			return "cursor-grok-4.5"
		default:
			return model
		}
	}
	return model
}
