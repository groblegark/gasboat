package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

func TestResolveOptionLabel_WithOptions(t *testing.T) {
	daemon := newMockDaemon()
	opts := []map[string]string{
		{"id": "a", "short": "Alpha", "label": "Option Alpha"},
		{"id": "b", "short": "Beta", "label": ""},
		{"id": "c", "short": "", "label": ""},
	}
	optsJSON, _ := json.Marshal(opts)
	daemon.mu.Lock()
	daemon.beads["dec-1"] = &beadsapi.BeadDetail{
		ID:     "dec-1",
		Type:   "decision",
		Fields: map[string]string{"options": string(optsJSON)},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	tests := []struct {
		name     string
		beadID   string
		optIndex string
		want     string
	}{
		{"label present", "dec-1", "1", "Option Alpha"},
		{"falls back to short", "dec-1", "2", "Beta"},
		{"falls back to id", "dec-1", "3", "c"},
		{"out of range index", "dec-1", "99", "Option 99"},
		{"zero index", "dec-1", "0", "Option 0"},
		{"non-numeric index", "dec-1", "abc", "Option abc"},
		{"unknown bead (returns default from GetBead)", "unknown-bead", "1", "Option 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bot.resolveOptionLabel(context.Background(), tt.beadID, tt.optIndex)
			if got != tt.want {
				t.Errorf("resolveOptionLabel(%q, %q) = %q, want %q", tt.beadID, tt.optIndex, got, tt.want)
			}
		})
	}
}

func TestLookupArtifactType(t *testing.T) {
	daemon := newMockDaemon()
	opts := []map[string]string{
		{"id": "a", "label": "Option Alpha", "artifact_type": "report"},
		{"id": "b", "short": "Beta", "artifact_type": "plan"},
		{"id": "c", "label": "No Artifact"},
	}
	optsJSON, _ := json.Marshal(opts)
	daemon.mu.Lock()
	daemon.beads["dec-2"] = &beadsapi.BeadDetail{
		ID:     "dec-2",
		Type:   "decision",
		Fields: map[string]string{"options": string(optsJSON)},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	tests := []struct {
		name        string
		beadID      string
		chosenLabel string
		want        string
	}{
		{"match by label", "dec-2", "Option Alpha", "report"},
		{"match by short fallback", "dec-2", "Beta", "plan"},
		{"no artifact_type", "dec-2", "No Artifact", ""},
		{"no match", "dec-2", "Unknown", ""},
		{"unknown bead", "nonexistent", "anything", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bot.lookupArtifactType(context.Background(), tt.beadID, tt.chosenLabel)
			if got != tt.want {
				t.Errorf("lookupArtifactType(%q, %q) = %q, want %q", tt.beadID, tt.chosenLabel, got, tt.want)
			}
		})
	}
}

func TestHandleDismiss(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		Channel: slack.Channel{
			GroupConversation: slack.GroupConversation{
				Conversation: slack.Conversation{ID: "C123"},
			},
		},
		User: slack.User{Name: "testuser"},
	}
	callback.MessageTs = "1234.5678"

	bot.handleDismiss(context.Background(), "dec-dismiss-1", callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].BeadID != "dec-dismiss-1" {
		t.Errorf("expected beadID=dec-dismiss-1, got %s", closed[0].BeadID)
	}
	if closed[0].Fields["chosen"] != "dismissed" {
		t.Errorf("expected chosen=dismissed, got %s", closed[0].Fields["chosen"])
	}
	if closed[0].Fields["rationale"] != "Dismissed by @testuser via Slack" {
		t.Errorf("unexpected rationale: %s", closed[0].Fields["rationale"])
	}
}

func TestHandleBlockActions_DismissAction(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		Channel: slack.Channel{
			GroupConversation: slack.GroupConversation{
				Conversation: slack.Conversation{ID: "C123"},
			},
		},
		User: slack.User{Name: "alice"},
	}
	callback.MessageTs = "1111.2222"
	callback.ActionCallback.BlockActions = []*slack.BlockAction{
		{ActionID: "dismiss_decision", Value: "dec-block-1"},
	}

	bot.handleBlockActions(context.Background(), callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].BeadID != "dec-block-1" {
		t.Errorf("expected beadID=dec-block-1, got %s", closed[0].BeadID)
	}
}

func TestHandleBlockActions_ResolveOptionOpensModal(t *testing.T) {
	daemon := newMockDaemon()
	opts := []map[string]string{
		{"id": "opt1", "label": "First Option"},
	}
	optsJSON, _ := json.Marshal(opts)
	daemon.mu.Lock()
	daemon.beads["dec-modal-1"] = &beadsapi.BeadDetail{
		ID:     "dec-modal-1",
		Type:   "decision",
		Fields: map[string]string{
			"options":  string(optsJSON),
			"question": "Which path?",
		},
	}
	daemon.mu.Unlock()

	// Track calls to views.open
	var openViewCalled bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/views.open" {
			openViewCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"view": map[string]any{"id": "V123"},
		})
	}))
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		Channel: slack.Channel{
			GroupConversation: slack.GroupConversation{
				Conversation: slack.Conversation{ID: "C123"},
			},
		},
		User:    slack.User{Name: "bob"},
		Message: slack.Message{Msg: slack.Msg{Timestamp: "9999.0000"}},
	}
	callback.TriggerID = "trigger-123"
	callback.ActionCallback.BlockActions = []*slack.BlockAction{
		{ActionID: "resolve_dec-modal-1_1", Value: "dec-modal-1:1"},
	}

	bot.handleBlockActions(context.Background(), callback)

	if !openViewCalled {
		t.Error("expected views.open to be called for resolve option")
	}
}

func TestHandleBlockActions_NoActions(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{}
	callback.ActionCallback.BlockActions = []*slack.BlockAction{}

	// Should not panic or error with empty actions.
	bot.handleBlockActions(context.Background(), callback)

	if len(daemon.getClosed()) != 0 {
		t.Error("expected no close calls for empty actions")
	}
}

func TestHandleBlockActions_ResolveOtherOpensModal(t *testing.T) {
	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["dec-other-1"] = &beadsapi.BeadDetail{
		ID:     "dec-other-1",
		Type:   "decision",
		Fields: map[string]string{"question": "What do you think?"},
	}
	daemon.mu.Unlock()

	var openViewCalled bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/views.open" {
			openViewCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"view": map[string]any{"id": "V456"},
		})
	}))
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		Channel: slack.Channel{
			GroupConversation: slack.GroupConversation{
				Conversation: slack.Conversation{ID: "C123"},
			},
		},
		User: slack.User{Name: "carol"},
	}
	callback.TriggerID = "trigger-456"
	callback.ActionCallback.BlockActions = []*slack.BlockAction{
		{ActionID: "resolve_other_dec-other-1"},
	}

	bot.handleBlockActions(context.Background(), callback)

	if !openViewCalled {
		t.Error("expected views.open to be called for other option")
	}
}

func TestHandleBlockActions_InvalidResolveValue(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{}
	callback.ActionCallback.BlockActions = []*slack.BlockAction{
		// Value missing the ":" separator — should be skipped.
		{ActionID: "resolve_dec-bad_1", Value: "no-colon-here"},
	}

	bot.handleBlockActions(context.Background(), callback)

	if len(daemon.getClosed()) != 0 {
		t.Error("expected no close calls for invalid resolve value")
	}
}

func TestHandleViewSubmission_ResolveDecision(t *testing.T) {
	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["dec-resolve-1"] = &beadsapi.BeadDetail{
		ID:     "dec-resolve-1",
		Type:   "decision",
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	// Pre-populate message cache so updateMessageResolved can find it.
	bot.messages["dec-resolve-1"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	callback := slack.InteractionCallback{
		User: slack.User{Name: "dave"},
		View: slack.View{
			CallbackID:      "resolve_decision",
			PrivateMetadata: "dec-resolve-1|Option Alpha|C123|1111.2222",
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{
					"rationale": {
						"rationale_input": {Value: "Looks correct"},
					},
				},
			},
		},
	}

	bot.handleViewSubmission(context.Background(), callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].BeadID != "dec-resolve-1" {
		t.Errorf("expected beadID=dec-resolve-1, got %s", closed[0].BeadID)
	}
	if closed[0].Fields["chosen"] != "Option Alpha" {
		t.Errorf("expected chosen=Option Alpha, got %s", closed[0].Fields["chosen"])
	}
	if closed[0].Fields["rationale"] != "Looks correct — @dave via Slack" {
		t.Errorf("unexpected rationale: %s", closed[0].Fields["rationale"])
	}
}

func TestHandleViewSubmission_ResolveDecisionNoRationale(t *testing.T) {
	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["dec-nr-1"] = &beadsapi.BeadDetail{
		ID:     "dec-nr-1",
		Type:   "decision",
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)
	bot.messages["dec-nr-1"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	callback := slack.InteractionCallback{
		User: slack.User{Name: "eve"},
		View: slack.View{
			CallbackID:      "resolve_decision",
			PrivateMetadata: "dec-nr-1|Beta|C123|1111.2222",
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{
					"rationale": {
						"rationale_input": {Value: ""},
					},
				},
			},
		},
	}

	bot.handleViewSubmission(context.Background(), callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].Fields["rationale"] != "Chosen by @eve via Slack" {
		t.Errorf("unexpected rationale: %s", closed[0].Fields["rationale"])
	}
}

func TestHandleViewSubmission_ResolveDecisionWithArtifactType(t *testing.T) {
	daemon := newMockDaemon()
	opts := []map[string]string{
		{"id": "a", "label": "Plan It", "artifact_type": "plan"},
	}
	optsJSON, _ := json.Marshal(opts)
	daemon.mu.Lock()
	daemon.beads["dec-art-1"] = &beadsapi.BeadDetail{
		ID:     "dec-art-1",
		Type:   "decision",
		Fields: map[string]string{"options": string(optsJSON)},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)
	bot.messages["dec-art-1"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	callback := slack.InteractionCallback{
		User: slack.User{Name: "frank"},
		View: slack.View{
			CallbackID:      "resolve_decision",
			PrivateMetadata: "dec-art-1|Plan It|C123|1111.2222",
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{
					"rationale": {
						"rationale_input": {Value: ""},
					},
				},
			},
		},
	}

	bot.handleViewSubmission(context.Background(), callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].Fields["required_artifact"] != "plan" {
		t.Errorf("expected required_artifact=plan, got %s", closed[0].Fields["required_artifact"])
	}
	if closed[0].Fields["artifact_status"] != "pending" {
		t.Errorf("expected artifact_status=pending, got %s", closed[0].Fields["artifact_status"])
	}
}

func TestHandleViewSubmission_ResolveOther(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)
	bot.messages["dec-other-sub-1"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	callback := slack.InteractionCallback{
		User: slack.User{Name: "grace"},
		View: slack.View{
			CallbackID:      "resolve_other",
			PrivateMetadata: "dec-other-sub-1|C123|1111.2222",
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{
					"response": {
						"response_input": {Value: "Custom answer here"},
					},
					"artifact_type": {
						"artifact_type_input": {
							SelectedOption: slack.OptionBlockObject{
								Value: "report",
							},
						},
					},
				},
			},
		},
	}

	bot.handleViewSubmission(context.Background(), callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].BeadID != "dec-other-sub-1" {
		t.Errorf("expected beadID=dec-other-sub-1, got %s", closed[0].BeadID)
	}
	if closed[0].Fields["chosen"] != "Custom answer here" {
		t.Errorf("expected chosen='Custom answer here', got %s", closed[0].Fields["chosen"])
	}
	if closed[0].Fields["required_artifact"] != "report" {
		t.Errorf("expected required_artifact=report, got %s", closed[0].Fields["required_artifact"])
	}
}

func TestHandleViewSubmission_ResolveOtherNoArtifact(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)
	bot.messages["dec-other-noart"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	callback := slack.InteractionCallback{
		User: slack.User{Name: "heidi"},
		View: slack.View{
			CallbackID:      "resolve_other",
			PrivateMetadata: "dec-other-noart|C123|1111.2222",
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{
					"response": {
						"response_input": {Value: "My response"},
					},
					"artifact_type": {
						"artifact_type_input": {
							SelectedOption: slack.OptionBlockObject{
								Value: "none",
							},
						},
					},
				},
			},
		},
	}

	bot.handleViewSubmission(context.Background(), callback)

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if _, ok := closed[0].Fields["required_artifact"]; ok {
		t.Errorf("expected no required_artifact for 'none', got %s", closed[0].Fields["required_artifact"])
	}
}

func TestHandleViewSubmission_ResolveOtherEmptyResponse(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		User: slack.User{Name: "ivan"},
		View: slack.View{
			CallbackID:      "resolve_other",
			PrivateMetadata: "dec-empty|C123|1111.2222",
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{
					"response": {
						"response_input": {Value: ""},
					},
				},
			},
		},
	}

	bot.handleViewSubmission(context.Background(), callback)

	// Empty response should be a no-op.
	if len(daemon.getClosed()) != 0 {
		t.Error("expected no close calls for empty response")
	}
}

func TestHandleViewSubmission_InvalidMetadata(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		User: slack.User{Name: "judy"},
		View: slack.View{
			CallbackID:      "resolve_decision",
			PrivateMetadata: "", // empty metadata
			State: &slack.ViewState{
				Values: map[string]map[string]slack.BlockAction{},
			},
		},
	}

	// Should not panic with invalid metadata.
	bot.handleViewSubmission(context.Background(), callback)

	if len(daemon.getClosed()) != 0 {
		t.Error("expected no close calls for invalid metadata")
	}
}

func TestHandleViewSubmission_UnknownCallbackID(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		View: slack.View{
			CallbackID: "unknown_callback",
			State:      &slack.ViewState{Values: map[string]map[string]slack.BlockAction{}},
		},
	}

	// Should not panic.
	bot.handleViewSubmission(context.Background(), callback)
	if len(daemon.getClosed()) != 0 {
		t.Error("expected no close calls for unknown callback")
	}
}

func TestNudgeDecisionRequestingAgent(t *testing.T) {
	// Set up a coop server to receive the nudge.
	var nudgeReceived bool
	coopSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			nudgeReceived = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopSrv.Close()

	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["dec-nudge-1"] = &beadsapi.BeadDetail{
		ID:     "dec-nudge-1",
		Type:   "decision",
		Fields: map[string]string{"requesting_agent_bead_id": "agent-bead-1"},
	}
	daemon.beads["agent-bead-1"] = &beadsapi.BeadDetail{
		ID:    "agent-bead-1",
		Type:  "agent",
		Notes: "coop_url: " + coopSrv.URL,
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	bot.nudgeDecisionRequestingAgent(context.Background(), "dec-nudge-1")

	if !nudgeReceived {
		t.Error("expected nudge to be sent to coop server")
	}
}

func TestNudgeDecisionRequestingAgent_NoAgentBeadID(t *testing.T) {
	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["dec-no-agent"] = &beadsapi.BeadDetail{
		ID:     "dec-no-agent",
		Type:   "decision",
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	// Should not panic with no requesting_agent_bead_id.
	bot.nudgeDecisionRequestingAgent(context.Background(), "dec-no-agent")
}

func TestNudgeDecisionRequestingAgent_NoCoopURL(t *testing.T) {
	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["dec-no-coop"] = &beadsapi.BeadDetail{
		ID:     "dec-no-coop",
		Type:   "decision",
		Fields: map[string]string{"requesting_agent_bead_id": "agent-no-coop"},
	}
	daemon.beads["agent-no-coop"] = &beadsapi.BeadDetail{
		ID:    "agent-no-coop",
		Type:  "agent",
		Notes: "",
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()
	bot := newTestBot(daemon, slackSrv)

	// Should not panic with no coop_url.
	bot.nudgeDecisionRequestingAgent(context.Background(), "dec-no-coop")
}
