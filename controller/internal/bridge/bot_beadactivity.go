package bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/slack-go/slack"
)

// NotifyBeadCreated posts a notification in the creating agent's Slack thread.
func (b *Bot) NotifyBeadCreated(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.CreatedBy)
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	if !b.agentThreadingEnabled() {
		return
	}

	threadTS := b.getAgentThreadTS(agent)
	if threadTS == "" {
		return
	}

	channel := b.resolveChannel(agent)
	title := truncateText(bead.Title, 60)
	text := fmt.Sprintf(":pencil2: Created %s: *%s*", bead.Type, title)

	_, _, _ = b.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
}

// NotifyBeadClaimed posts a notification in the claiming agent's Slack thread.
func (b *Bot) NotifyBeadClaimed(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.Assignee)
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	if !b.agentThreadingEnabled() {
		return
	}

	threadTS := b.getAgentThreadTS(agent)
	if threadTS == "" {
		return
	}

	channel := b.resolveChannel(agent)
	title := truncateText(bead.Title, 60)
	text := fmt.Sprintf(":arrow_right: Claimed %s: *%s*", bead.Type, title)

	_, _, _ = b.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
}

// NotifyBeadClosed posts a notification in the assigned agent's Slack thread.
func (b *Bot) NotifyBeadClosed(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.Assignee)
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	if !b.agentThreadingEnabled() {
		return
	}

	threadTS := b.getAgentThreadTS(agent)
	if threadTS == "" {
		return
	}

	channel := b.resolveChannel(agent)
	title := truncateText(bead.Title, 60)
	text := fmt.Sprintf(":white_check_mark: Closed %s: *%s*", bead.Type, title)

	_, _, _ = b.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
}

// getAgentThreadTS returns the thread timestamp for an agent's card,
// or "" if no card exists. Does not create a card.
func (b *Bot) getAgentThreadTS(agent string) string {
	b.mu.Lock()
	ref, ok := b.agentCards[agent]
	b.mu.Unlock()
	if ok {
		return ref.Timestamp
	}
	return ""
}

// recordActivity updates the agent's last-seen timestamp so the agent card
// reflects recent activity.
func (b *Bot) recordActivity(agent string) {
	b.mu.Lock()
	b.agentSeen[agent] = time.Now()
	b.mu.Unlock()
}
