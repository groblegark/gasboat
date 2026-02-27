package advice

import (
	"context"

	"gasboat/controller/internal/beadsapi"
)

// MatchedAdvice pairs an advice bead with the labels that caused it to match.
type MatchedAdvice struct {
	Bead          *beadsapi.BeadDetail
	MatchedLabels []string
	Scope         string
	ScopeTarget   string
	ScopeHeader   string
}

// ListAdviceForAgent fetches open advice beads and filters them by the agent's
// subscriptions. Returns matched advice and the computed subscription list.
func ListAdviceForAgent(ctx context.Context, daemon *beadsapi.Client, agentID string) ([]MatchedAdvice, []string, error) {
	allAdvice, err := ListAllAdvice(ctx, daemon)
	if err != nil {
		return nil, nil, err
	}

	subs := BuildAgentSubscriptions(agentID, nil)
	subs = EnrichAgentSubscriptions(ctx, daemon, agentID, subs)

	var matched []MatchedAdvice
	for _, bead := range allAdvice {
		if MatchesSubscriptions(bead.Labels, subs) {
			ml := FindMatchedLabels(bead.Labels, subs)
			scope, target := CategorizeScope(bead.Labels)
			matched = append(matched, MatchedAdvice{
				Bead:          bead,
				MatchedLabels: ml,
				Scope:         scope,
				ScopeTarget:   target,
				ScopeHeader:   BuildScopeHeader(scope, target),
			})
		}
	}

	return matched, subs, nil
}

// ListAllAdvice fetches all open advice beads from the daemon.
func ListAllAdvice(ctx context.Context, daemon *beadsapi.Client) ([]*beadsapi.BeadDetail, error) {
	result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"advice"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		return nil, err
	}
	return result.Beads, nil
}
