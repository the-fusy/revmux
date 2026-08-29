package pipeline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

func TestEvent_jsonRoundTrip(t *testing.T) {
	in := Event{
		Kind:  EventFindings,
		Agent: "bugs+impl",
		Text:  "2 findings",
		Findings: []finding.Finding{
			{ID: "bugs+impl-1", File: "a.go", Line: 10, Severity: finding.Major, Confidence: 85,
				Title: "leak", Sources: []string{"bugs+impl"}, Lenses: []string{"bugs"}},
		},
		At: time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC),
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out Event
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out, "events.jsonl is the run's decision record and must survive a round trip")
}

func TestEvent_kindsAreDistinct(t *testing.T) {
	kinds := []EventKind{
		EventAgentStarted, EventAgentActivity, EventAgentState, EventAgentDone,
		EventAgentRetried, EventAgentDegraded, EventSession, EventFindings, EventStage, EventRateLimit,
	}

	seen := map[EventKind]struct{}{}
	for _, k := range kinds {
		assert.NotEmpty(t, string(k))
		_, dup := seen[k]
		assert.False(t, dup, "duplicate kind %q would make one renderer case unreachable", k)
		seen[k] = struct{}{}
	}
}

func TestEvent_sessionIdentityRoundTrip(t *testing.T) {
	in := Event{Kind: EventSession, Agent: "synthesis", Executor: "codex",
		SessionID: "019f9d7b-8724-7610-883e-0153227b0396"}
	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out Event
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out)
}

func TestEvent_emptyFieldsAreOmitted(t *testing.T) {
	data, err := json.Marshal(Event{Kind: EventStage, Stage: stageFind})
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"stage","stage":"find","at":"0001-01-01T00:00:00Z"}`, string(data))
}
