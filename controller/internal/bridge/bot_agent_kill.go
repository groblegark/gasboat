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
		delete(b.agentRole, agentName)
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

// respawnThreadAgent re-creates an agent bead with the SAME name so the
// entrypoint finds the existing session JSONL and PVC workspace, passing
// --resume to coop for session continuity. Used when a thread reply arrives
// for a dead/completed agent. The triggerText is included in the description
// so the agent knows why it was woken up.
func (b *Bot) respawnThreadAgent(ctx context.Context, channel, threadTS, agentName, triggerText string) {
	agentName = extractAgentName(agentName)

	// Infer project from channel (same logic as handleThreadSpawn).
	project := b.projectFromChannel(ctx, channel)
	if project == "" && b.router != nil {
		if mapped := b.router.GetAgentByChannel(channel); mapped != "" {
			project = projectFromAgentIdentity(mapped)
		}
	}

	// Build agent bead fields.
	fields := map[string]string{
		"agent":                agentName,
		"mode":                 "job",
		"role":                 "thread",
		"project":              project,
		"slack_thread_channel": channel,
		"slack_thread_ts":      threadTS,
		"spawn_source":         "slack-thread-resume",
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		b.logger.Error("respawn-thread-agent: marshal fields", "error", err)
		return
	}

	labels := []string{"slack-thread"}
	if project != "" {
		labels = append(labels, "project:"+project)
	}

	description := fmt.Sprintf("Session-resumed thread agent.\n\nTriggered by thread reply:\n%s",
		truncateText(triggerText, 2000))

	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       agentName,
		Type:        "agent",
		Description: description,
		Fields:      json.RawMessage(fieldsJSON),
		Labels:      labels,
	})
	if err != nil {
		b.logger.Error("respawn-thread-agent: failed to create bead",
			"agent", agentName, "channel", channel, "thread_ts", threadTS, "error", err)
		return
	}

	// Re-establish thread→agent mapping.
	if b.state != nil {
		_ = b.state.SetThreadAgent(channel, threadTS, agentName)
	}

	b.logger.Info("respawned thread agent with session resume",
		"agent", agentName, "bead", beadID, "channel", channel, "thread_ts", threadTS)

	// Post confirmation in thread.
	if b.api != nil {
		msg := fmt.Sprintf(":arrows_counterclockwise: Resuming agent *%s* (tracking: `%s`)", agentName, beadID)
		_, msgTS, _ := b.api.PostMessage(channel,
			slack.MsgOptionText(msg, false),
			slack.MsgOptionTS(threadTS),
		)
		if msgTS != "" {
			b.mu.Lock()
			b.threadSpawnMsgs[agentName] = MessageRef{
				ChannelID: channel, Timestamp: msgTS, ThreadTS: threadTS,
			}
			b.mu.Unlock()
		}
	}
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
		delete(b.agentRole, agentIdentity)
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

// handleKillThreadAgent handles the "Kill Agent" button posted in thread spawn messages.
// The button's value carries the agent identity. The interaction callback provides
// the channel and thread context that slash commands cannot access.
func (b *Bot) handleKillThreadAgent(ctx context.Context, agentName string, callback slack.InteractionCallback) {
	agentName = extractAgentName(agentName)
	channelID := callback.Channel.ID
	userID := callback.User.ID

	// Acknowledge immediately — kill can take 30s+.
	_, _ = b.api.PostEphemeral(channelID, userID,
		slack.MsgOptionText(fmt.Sprintf(":hourglass_flowing_sand: Killing thread agent *%s*…", agentName), false))

	go func() {
		if err := b.killAgent(context.Background(), agentName, false); err != nil {
			b.logger.Error("kill-thread-agent button: failed", "agent", agentName, "error", err)
			_, _ = b.api.PostEphemeral(channelID, userID,
				slack.MsgOptionText(fmt.Sprintf(":x: Failed to kill thread agent %q: %s", agentName, err.Error()), false))
			return
		}
		b.logger.Info("killed thread agent via button", "agent", agentName, "user", userID)
		_, _ = b.api.PostEphemeral(channelID, userID,
			slack.MsgOptionText(fmt.Sprintf(":skull: Thread agent *%s* terminated.", agentName), false))
	}()
}

// handleRestartThreadAgent handles the "Restart Agent" button on thread spawn messages.
// It kills the current agent then re-spawns a new one with the same name, project,
// and thread metadata — preserving the session JSONL for coop --resume.
func (b *Bot) handleRestartThreadAgent(ctx context.Context, agentName string, callback slack.InteractionCallback) {
	agentName = extractAgentName(agentName)
	channelID := callback.Channel.ID
	threadTS := callback.Container.ThreadTs
	userID := callback.User.ID

	if threadTS == "" {
		_, _ = b.api.PostEphemeral(channelID, userID,
			slack.MsgOptionText(":x: Restart is only available in threads.", false))
		return
	}

	_, _ = b.api.PostEphemeral(channelID, userID,
		slack.MsgOptionText(fmt.Sprintf(":arrows_counterclockwise: Restarting thread agent *%s*…", agentName), false))

	go func() {
		bgCtx := context.Background()

		// Look up the existing agent bead to capture project and metadata before killing.
		bead, err := b.daemon.FindAgentBead(bgCtx, agentName)
		if err != nil {
			b.logger.Error("restart-thread-agent: agent bead not found", "agent", agentName, "error", err)
			_, _ = b.api.PostEphemeral(channelID, userID,
				slack.MsgOptionText(fmt.Sprintf(":x: Agent %q not found: %s", agentName, err), false))
			return
		}
		project := bead.Fields["project"]

		// Kill the agent (graceful shutdown + close bead + clean up state).
		if err := b.killAgent(bgCtx, agentName, false); err != nil {
			b.logger.Error("restart-thread-agent: kill failed", "agent", agentName, "error", err)
			_, _ = b.api.PostEphemeral(channelID, userID,
				slack.MsgOptionText(fmt.Sprintf(":x: Failed to kill agent %q: %s", agentName, err), false))
			return
		}

		// Re-spawn with the SAME agent name so the entrypoint finds the existing
		// session JSONL and PVC workspace for session continuity.
		fields := map[string]string{
			"agent":                agentName,
			"mode":                 "job",
			"role":                 "thread",
			"project":              project,
			"slack_thread_channel": channelID,
			"slack_thread_ts":      threadTS,
			"spawn_source":         "slack-thread",
		}
		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			b.logger.Error("restart-thread-agent: marshal fields", "error", err)
			return
		}

		labels := []string{"slack-thread"}
		if project != "" {
			labels = append(labels, "project:"+project)
		}

		newBeadID, err := b.daemon.CreateBead(bgCtx, beadsapi.CreateBeadRequest{
			Title:       agentName,
			Type:        "agent",
			Description: "Restarted thread agent (session resume).",
			Fields:      json.RawMessage(fieldsJSON),
			Labels:      labels,
		})
		if err != nil {
			b.logger.Error("restart-thread-agent: failed to create new bead", "agent", agentName, "error", err)
			_, _ = b.api.PostEphemeral(channelID, userID,
				slack.MsgOptionText(fmt.Sprintf(":x: Killed agent but failed to re-spawn: %s", err), false))
			return
		}

		// Re-establish thread→agent mapping.
		if b.state != nil {
			_ = b.state.SetThreadAgent(channelID, threadTS, agentName)
		}

		b.logger.Info("restarted thread agent", "agent", agentName, "new_bead", newBeadID,
			"user", userID, "channel", channelID, "thread_ts", threadTS)

		if b.api != nil {
			msg := fmt.Sprintf(":arrows_counterclockwise: Agent *%s* restarted with session resume. (tracking: `%s`)", agentName, newBeadID)
			_, _, _ = b.api.PostMessage(channelID,
				slack.MsgOptionText(msg, false),
				slack.MsgOptionBlocks(
					slack.NewSectionBlock(slack.NewTextBlockObject("mrkdwn", msg, false, false), nil, nil),
					slack.NewActionBlock("thread_agent_actions",
						slack.NewButtonBlockElement("restart_thread_agent", agentName,
							slack.NewTextBlockObject("plain_text", "Restart Agent", false, false)),
						slack.NewButtonBlockElement("kill_thread_agent", agentName,
							slack.NewTextBlockObject("plain_text", "Kill Agent", false, false)).
							WithStyle(slack.StyleDanger),
					),
				),
				slack.MsgOptionTS(threadTS),
			)
		}
	}()
}
