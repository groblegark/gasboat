package bridge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// handleAppMention processes @mention events.
// When a user mentions the bot in an agent's thread or in an agent-specific
// channel, this creates a tracking bead and nudges the agent with the message.
func (b *Bot) handleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent) {
	// Ignore bot-triggered mentions.
	if ev.BotID != "" {
		return
	}

	var agent string
	replyTS := ev.ThreadTimeStamp // timestamp to thread the confirmation reply under

	if ev.ThreadTimeStamp == "" {
		// Not in a thread — check if this channel is mapped to an agent via the router.
		// This handles "@gasboat <message>" in a dedicated agent channel.
		if b.router != nil {
			agent = b.router.GetAgentByChannel(ev.Channel)
		}
		if agent == "" {
			b.logger.Debug("app_mention ignored: not in a thread and no channel mapping",
				"channel", ev.Channel, "user", ev.User)
			return
		}
		// Use the mention's own timestamp as the reply anchor so the response
		// threads back to this message.
		replyTS = ev.TimeStamp
	} else {
		// In a thread — reverse-lookup which agent owns this thread.
		agent = b.getAgentByThread(ev.Channel, ev.ThreadTimeStamp)
		if agent == "" {
			b.logger.Debug("app_mention ignored: not an agent thread",
				"channel", ev.Channel, "thread_ts", ev.ThreadTimeStamp)
			return
		}
	}

	// Strip the bot mention from the message text.
	text := stripBotMention(ev.Text, b.botUserID)
	if text == "" {
		b.logger.Debug("app_mention ignored: empty after stripping mention",
			"channel", ev.Channel, "agent", agent)
		return
	}

	// Resolve sender display name.
	username := ev.User
	if user, err := b.api.GetUserInfo(ev.User); err == nil {
		if user.RealName != "" {
			username = user.RealName
		} else if user.Name != "" {
			username = user.Name
		}
	}

	// Build bead title and description with slack metadata tag.
	title := truncateText(fmt.Sprintf("Mention: %s", text), 80)
	slackTag := fmt.Sprintf("[slack:%s:%s]", ev.Channel, replyTS)
	description := fmt.Sprintf("Mention from %s in Slack:\n\n%s\n\n---\n%s", username, text, slackTag)

	// Create tracking bead assigned to the agent.
	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Description: description,
		Assignee:    extractAgentName(agent),
		Labels:      []string{"slack-mention"},
		Priority:    2,
	})
	if err != nil {
		b.logger.Error("failed to create mention bead",
			"channel", ev.Channel, "agent", agent, "error", err)
		return
	}

	b.logger.Info("mention: created tracking bead",
		"bead", beadID, "agent", agent, "user", username)

	// Persist message ref for response relay.
	if b.state != nil {
		_ = b.state.SetChatMessage(beadID, MessageRef{
			ChannelID: ev.Channel,
			Timestamp: replyTS,
			Agent:     agent,
		})
	}

	// Nudge the agent.
	b.nudgeAgentForMention(ctx, agent, text, beadID)

	// Post confirmation threaded under the original message.
	_, _, _ = b.api.PostMessage(ev.Channel,
		slack.MsgOptionText(
			fmt.Sprintf(":mega: Forwarded to *%s* (tracking: `%s`)", extractAgentName(agent), beadID),
			false),
		slack.MsgOptionTS(replyTS),
	)
}

// getAgentByThread reverse-maps (channel, thread_ts) to an agent identity
// by checking the agentCards hot cache and falling back to persisted state.
func (b *Bot) getAgentByThread(channelID, threadTS string) string {
	b.mu.Lock()
	for agent, ref := range b.agentCards {
		if ref.ChannelID == channelID && ref.Timestamp == threadTS {
			b.mu.Unlock()
			return agent
		}
	}
	b.mu.Unlock()

	// Fall back to persisted state.
	if b.state != nil {
		for agent, ref := range b.state.AllAgentCards() {
			if ref.ChannelID == channelID && ref.Timestamp == threadTS {
				return agent
			}
		}
	}
	return ""
}

// stripBotMention removes all <@BOTID> occurrences from text and trims whitespace.
func stripBotMention(text, botUserID string) string {
	mention := fmt.Sprintf("<@%s>", botUserID)
	text = strings.ReplaceAll(text, mention, "")
	return strings.TrimSpace(text)
}

// nudgeAgentForMention looks up the agent's coop_url and sends a nudge with the mention text.
func (b *Bot) nudgeAgentForMention(ctx context.Context, agent, text, beadID string) {
	agentName := extractAgentName(agent)

	agentBead, err := b.daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		b.logger.Debug("could not find agent bead for mention nudge",
			"agent", agentName, "bead", beadID)
		return
	}

	coopURL := beadsapi.ParseNotes(agentBead.Notes)["coop_url"]
	if coopURL == "" {
		b.logger.Debug("agent bead has no coop_url for mention nudge",
			"agent", agentName, "bead", beadID)
		return
	}

	message := fmt.Sprintf("Slack mention (bead %s): %s", beadID, text)
	client := &http.Client{Timeout: 10 * time.Second}
	if err := nudgeCoop(ctx, client, coopURL, message); err != nil {
		b.logger.Error("failed to nudge agent for mention",
			"agent", agentName, "coop_url", coopURL, "error", err)
		return
	}

	b.logger.Info("nudged agent for mention",
		"agent", agentName, "bead", beadID)
}
