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
	"github.com/slack-go/slack/slackevents"
)

// handleThreadSpawn spawns an ephemeral agent bound to a Slack thread when
// @gasboat is mentioned in a thread with no existing agent binding.
func (b *Bot) handleThreadSpawn(ctx context.Context, ev *slackevents.AppMentionEvent, text string) {
	channel := ev.Channel
	threadTS := ev.ThreadTimeStamp

	// Guard: prevent spawning duplicate agents for the same thread.
	// Another mention may have raced us, or the state may have been set
	// between the caller's check and this point.
	if b.state != nil {
		if agent, ok := b.state.GetThreadAgent(channel, threadTS); ok {
			b.logger.Info("thread-spawn: agent already bound to thread, skipping",
				"channel", channel, "thread_ts", threadTS, "agent", agent)
			if b.api != nil {
				_, _, _ = b.api.PostMessage(channel,
					slack.MsgOptionText(
						fmt.Sprintf(":information_source: An agent (*%s*) is already working in this thread.", extractAgentName(agent)),
						false),
					slack.MsgOptionTS(threadTS),
				)
			}
			return
		}
	}

	// Check for explicit project override in the mention text.
	// Supports "project:<name>" and "--project <name>" syntax.
	projectOverride, text := parseProjectOverride(text)

	// Fetch thread context from Slack.
	threadContext := b.fetchThreadContext(ctx, channel, threadTS)

	// Generate a unique agent name based on the thread timestamp.
	agentName := "thread-" + sanitizeTS(threadTS)

	// Use explicit project override if provided, otherwise infer from channel.
	project := projectOverride
	if project == "" {
		project = b.projectFromChannel(ctx, channel)
	}
	b.logger.Info("thread-spawn: project resolution",
		"channel", channel, "project", project, "override", projectOverride)
	if project == "" && b.router != nil {
		if mapped := b.router.GetAgentByChannel(channel); mapped != "" {
			project = projectFromAgentIdentity(mapped)
		}
	}

	// Build agent description with thread context.
	description := fmt.Sprintf("Thread-spawned agent for Slack thread.\n\n"+
		"## Thread Context\n\n%s\n\n---\n"+
		"Triggered by: %s", threadContext, text)
	description = truncateText(description, 4000)

	// Try to assign a prewarmed agent from the pool first.
	// This avoids the ~60-120s cold-start time for new agent pods.
	if beadID, assignedAgent := b.tryPoolAssign(ctx, channel, threadTS, description, project); beadID != "" {
		b.logger.Info("thread-spawn: assigned prewarmed agent",
			"bead", beadID, "agent", assignedAgent,
			"channel", channel, "thread_ts", threadTS)

		if b.state != nil {
			_ = b.state.SetThreadAgent(channel, threadTS, assignedAgent)
		}
		if b.api != nil {
			_, _, _ = b.api.PostMessage(channel,
				slack.MsgOptionText(
					fmt.Sprintf(":zap: Assigned a prewarmed agent — should be ready in seconds! (tracking: `%s`)", beadID),
					false),
				slack.MsgOptionTS(threadTS),
			)
		}
		return
	}

	// Fallback: cold-start a new agent pod.
	// Build fields including thread metadata.
	fields := map[string]string{
		"agent":                agentName,
		"mode":                 "job",
		"role":                 "thread",
		"project":              project,
		"slack_thread_channel": channel,
		"slack_thread_ts":      threadTS,
		"spawn_source":         "slack-thread",
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		b.logger.Error("failed to marshal agent fields", "error", err)
		return
	}

	labels := []string{"slack-thread"}
	if project != "" {
		labels = append(labels, "project:"+project)
	}

	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       agentName,
		Type:        "agent",
		Description: description,
		Fields:      json.RawMessage(fieldsJSON),
		Labels:      labels,
	})
	if err != nil {
		b.logger.Error("failed to create thread-spawned agent bead",
			"channel", channel, "thread_ts", threadTS, "error", err)
		return
	}

	b.logger.Info("thread-spawn: created agent bead (cold-start)",
		"bead", beadID, "agent", agentName,
		"channel", channel, "thread_ts", threadTS)

	// Record thread→agent mapping in state.
	if b.state != nil {
		_ = b.state.SetThreadAgent(channel, threadTS, agentName)
	}

	// Post confirmation reply in thread.
	if b.api != nil {
		_, _, _ = b.api.PostMessage(channel,
			slack.MsgOptionText(
				fmt.Sprintf(":zap: Spinning up an agent to help here... (tracking: `%s`)", beadID),
				false),
			slack.MsgOptionTS(threadTS),
		)
	}
}

// tryPoolAssign attempts to assign a prewarmed agent from the controller's pool.
// Returns (beadID, agentName) on success, or ("", "") if the pool is unavailable
// or empty. This is a best-effort optimization — callers should fall back to
// cold-start on failure.
func (b *Bot) tryPoolAssign(ctx context.Context, channel, threadTS, description, project string) (string, string) {
	if b.controllerURL == "" {
		return "", ""
	}

	reqBody, err := json.Marshal(map[string]string{
		"channel":     channel,
		"thread_ts":   threadTS,
		"description": description,
		"project":     project,
	})
	if err != nil {
		return "", ""
	}

	assignURL := strings.TrimRight(b.controllerURL, "/") + "/api/v1/pool/assign"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, assignURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", ""
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		b.logger.Debug("pool assign request failed", "error", err)
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b.logger.Debug("pool assign returned non-200", "status", resp.StatusCode)
		return "", ""
	}

	var result struct {
		BeadID    string `json:"bead_id"`
		AgentName string `json:"agent_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.logger.Warn("pool assign: failed to decode response", "error", err)
		return "", ""
	}

	return result.BeadID, result.AgentName
}

// fetchThreadContext retrieves thread messages from Slack, filtering out bot
// messages to keep the context clean for the new agent.
func (b *Bot) fetchThreadContext(ctx context.Context, channel, threadTS string) string {
	msgs, _, _, err := b.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: threadTS,
		Limit:     50,
	})
	if err != nil {
		b.logger.Error("failed to fetch thread context",
			"channel", channel, "thread_ts", threadTS, "error", err)
		return "(could not fetch thread context)"
	}

	var buf strings.Builder
	for _, msg := range msgs {
		// Skip bot messages to keep the prompt clean.
		if msg.BotID != "" || msg.SubType == "bot_message" {
			continue
		}
		author := msg.User
		if author == "" {
			author = msg.Username
		}
		line := fmt.Sprintf("**%s**: %s\n", author, msg.Text)
		// Annotate messages that have file attachments.
		for _, f := range msg.Files {
			line += fmt.Sprintf("  [attachment: %s (%s) — /api/slack/files/%s]\n", f.Name, f.Mimetype, f.ID)
		}
		if buf.Len()+len(line) > 3000 {
			buf.WriteString("\n_(thread truncated)_\n")
			break
		}
		buf.WriteString(line)
	}

	if buf.Len() == 0 {
		return "(empty thread)"
	}
	return buf.String()
}

// sanitizeTS converts a Slack timestamp like "1234567890.123456" to a safe
// identifier fragment "1234567890-123456".
func sanitizeTS(ts string) string {
	return strings.ReplaceAll(ts, ".", "-")
}

// projectFromAgentIdentity extracts the project name from an agent identity
// like "gasboat/crew/agent-name" → "gasboat". Returns "" if not project-qualified.
func projectFromAgentIdentity(identity string) string {
	parts := strings.Split(identity, "/")
	if len(parts) >= 2 {
		return parts[0]
	}
	return ""
}

// threadNudgeInterval is the minimum time between nudges for the same agent+thread.
const threadNudgeInterval = 30 * time.Second

// handleThreadForward creates a tracking bead and nudges the bound agent when
// a non-mention message is posted in an agent thread.
func (b *Bot) handleThreadForward(ctx context.Context, ev *slackevents.MessageEvent, agent string) {
	agentName := extractAgentName(agent)

	// Validate the agent is still active.
	if _, err := b.daemon.FindAgentBead(ctx, agentName); err != nil {
		b.logger.Debug("thread-forward: agent no longer active",
			"agent", agentName, "channel", ev.Channel, "thread_ts", ev.ThreadTimeStamp)
		// Clear stale mapping.
		if b.state != nil {
			_ = b.state.RemoveThreadAgent(ev.Channel, ev.ThreadTimeStamp)
		}
		return
	}

	// Resolve sender display name.
	username := ev.User
	if b.api != nil {
		if user, err := b.api.GetUserInfo(ev.User); err == nil {
			if user.RealName != "" {
				username = user.RealName
			} else if user.Name != "" {
				username = user.Name
			}
		}
	}

	// Build bead description.
	title := truncateText(fmt.Sprintf("Thread: %s", ev.Text), 80)
	slackTag := fmt.Sprintf("[slack:%s:%s]", ev.Channel, ev.ThreadTimeStamp)
	description := fmt.Sprintf("Thread reply from %s in Slack:\n\n%s\n\n---\n%s", username, ev.Text, slackTag)

	// Enrich with file attachments.
	files := b.fetchMessageFiles(ctx, ev.Channel, ev.TimeStamp)
	description += formatAttachmentsSection(files)

	var fieldsJSON json.RawMessage
	if fileFields := slackFilesToFields(files); fileFields != nil {
		fieldsJSON, _ = json.Marshal(fileFields)
	}

	// Create tracking bead.
	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Kind:        "issue",
		Description: description,
		Assignee:    agentName,
		Labels:      []string{"slack-thread-reply"},
		Priority:    3,
		Fields:      fieldsJSON,
	})
	if err != nil {
		b.logger.Error("failed to create thread-forward bead",
			"channel", ev.Channel, "agent", agentName, "error", err)
		return
	}

	b.logger.Info("thread-forward: created tracking bead",
		"bead", beadID, "agent", agentName, "user", username)

	// Persist message ref for response relay.
	if b.state != nil {
		_ = b.state.SetChatMessage(beadID, MessageRef{
			ChannelID: ev.Channel,
			Timestamp: ev.ThreadTimeStamp,
			Agent:     agent,
		})
	}

	// Nudge with throttling — avoid flooding the agent in active threads.
	if !b.shouldThrottleNudge(agentName, ev.ThreadTimeStamp) {
		message := fmt.Sprintf("Slack thread reply (bead %s): %s", beadID, truncateText(ev.Text, 200))
		client := &http.Client{Timeout: 10 * time.Second}
		if err := NudgeAgent(ctx, b.daemon, client, b.logger, agentName, message); err != nil {
			b.logger.Error("failed to nudge agent for thread forward",
				"agent", agentName, "bead", beadID, "error", err)
		}
	}
}

// shouldThrottleNudge returns true if a nudge was sent recently for this agent+thread.
// Updates the last nudge time if not throttled.
func (b *Bot) shouldThrottleNudge(agent, threadTS string) bool {
	key := agent + ":" + threadTS
	b.mu.Lock()
	defer b.mu.Unlock()
	if last, ok := b.lastThreadNudge[key]; ok && time.Since(last) < threadNudgeInterval {
		b.logger.Debug("thread-forward: nudge throttled",
			"agent", agent, "thread_ts", threadTS, "last_nudge_ago", time.Since(last))
		return true
	}
	b.lastThreadNudge[key] = time.Now()
	return false
}
