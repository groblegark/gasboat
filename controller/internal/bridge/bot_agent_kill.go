package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// killAgent closes an agent bead and removes its Slack card.
// It is called by both the /kill slash command and the Clear button handler.
// If force is false, it attempts a graceful shutdown via coop before closing
// the bead. If coop is unreachable (pod already dead), it falls back to an
// immediate hard-close.
func (b *Bot) killAgent(ctx context.Context, agentName string, force bool) error {
	// Canonicalize to short name so map lookups are consistent.
	agentName = extractAgentName(agentName)

	// Use a detached context for the kill operation — Slack's slash command
	// context expires after ~3s, but graceful shutdown + bead close can take longer.
	killCtx, killCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer killCancel()

	bead, err := b.daemon.FindAgentBead(killCtx, agentName)
	if err != nil {
		return fmt.Errorf("find agent bead: %w", err)
	}

	if !force {
		coopURL := beadsapi.ParseNotes(bead.Notes)["coop_url"]
		if coopURL != "" {
			b.logger.Info("attempting graceful shutdown via coop", "agent", agentName, "coop_url", coopURL)
			shutdownCtx, shutdownCancel := context.WithTimeout(killCtx, 30*time.Second)
			if ok := gracefulShutdownCoop(shutdownCtx, coopURL); ok {
				b.logger.Info("graceful coop shutdown confirmed", "agent", agentName)
			} else {
				b.logger.Warn("graceful shutdown failed or timed out, falling back to hard-close", "agent", agentName)
			}
			shutdownCancel()
		}
	}

	if err := b.daemon.CloseBead(killCtx, bead.ID, nil); err != nil {
		return fmt.Errorf("close agent bead: %w", err)
	}

	// Compress the agent card to a compact single-line format.
	b.mu.Lock()
	ref, hasCard := b.agentCards[agentName]
	if hasCard {
		delete(b.agentPending, agentName)
		delete(b.agentPodName, agentName)
		delete(b.agentImageTag, agentName)
	}
	b.mu.Unlock()

	if hasCard {
		blocks := buildCompactAgentCardBlocks(agentName, "done")
		_, _, _, err := b.api.UpdateMessageContext(killCtx, ref.ChannelID, ref.Timestamp,
			slack.MsgOptionText(fmt.Sprintf("Agent: %s (done)", extractAgentName(agentName)), false),
			slack.MsgOptionBlocks(blocks...),
		)
		if err != nil {
			b.logger.Error("kill agent: failed to compress card", "agent", agentName, "error", err)
		}
	}

	// Clean up any thread→agent mappings for this agent.
	if b.state != nil {
		_ = b.state.RemoveThreadAgentByAgent(agentName)
	}

	return nil
}

// handleClearAgent handles the "Clear" button on a done/failed agent card.
// It deletes the Slack message entirely and cleans up state. This is the only
// path that fully removes a card — kill and prune now compress cards instead.
func (b *Bot) handleClearAgent(ctx context.Context, agentIdentity string, callback slack.InteractionCallback) {
	agentIdentity = extractAgentName(agentIdentity)

	b.mu.Lock()
	ref, hasCard := b.agentCards[agentIdentity]
	if hasCard {
		delete(b.agentCards, agentIdentity)
		delete(b.agentPending, agentIdentity)
		delete(b.agentState, agentIdentity)
		delete(b.agentPodName, agentIdentity)
		delete(b.agentImageTag, agentIdentity)
	}
	b.mu.Unlock()

	if hasCard {
		if b.state != nil {
			_ = b.state.RemoveAgentCard(agentIdentity)
		}
		if _, _, err := b.api.DeleteMessageContext(ctx, ref.ChannelID, ref.Timestamp); err != nil {
			b.logger.Error("clear agent: failed to delete card",
				"agent", agentIdentity, "error", err)
			_, _ = b.api.PostEphemeral(callback.Channel.ID, callback.User.ID,
				slack.MsgOptionText(fmt.Sprintf(":x: Failed to clear agent %q: %s", agentIdentity, err.Error()), false))
			return
		}
	}

	// Clean up any thread→agent mappings for this agent.
	if b.state != nil {
		_ = b.state.RemoveThreadAgentByAgent(agentIdentity)
	}

	b.logger.Info("cleared agent via Slack", "agent", agentIdentity, "user", callback.User.ID)
}

// gracefulShutdownCoop sends ESC to the coop agent endpoint in a loop until the
// agent transitions to state=exited. Returns true if the agent exited cleanly,
// false if coop became unreachable (pod already dead).
//
// This mirrors the ESC loop in agent_k8s_lifecycle.go (autoBypassStartup) but
// blocks on "exited" rather than "idle", and runs from the bridge process using
// the remote coop_url from the agent bead's notes.
func gracefulShutdownCoop(ctx context.Context, coopURL string) bool {
	base := strings.TrimRight(coopURL, "/") + "/api/v1"
	client := &http.Client{Timeout: 3 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		// Check current agent state.
		state, err := getCoopAgentState(ctx, client, base)
		if err != nil {
			// Coop unreachable — pod already dead, treat as complete.
			return false
		}
		if state == "exited" {
			return true
		}

		// Send ESC to interrupt the current Claude turn.
		postCoopKeys(ctx, client, base, "Escape")

		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// getCoopAgentState fetches the agent's current state from the coop HTTP API.
func getCoopAgentState(ctx context.Context, client *http.Client, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/agent", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.State, nil
}

// postCoopKeys posts a key sequence to the coop input endpoint.
func postCoopKeys(ctx context.Context, client *http.Client, base string, keys ...string) {
	payload, _ := json.Marshal(map[string][]string{"keys": keys})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/input/keys", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}
