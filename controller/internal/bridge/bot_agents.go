package bridge

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"
)

// killAgent closes an agent bead and removes its Slack card.
// It is called by both the /kill slash command and the Clear button handler.
func (b *Bot) killAgent(ctx context.Context, agentName string) error {
	bead, err := b.daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		return fmt.Errorf("find agent bead: %w", err)
	}
	if err := b.daemon.CloseBead(ctx, bead.ID, nil); err != nil {
		return fmt.Errorf("close agent bead: %w", err)
	}

	// Remove the agent card from Slack.
	b.mu.Lock()
	ref, hasCard := b.agentCards[agentName]
	if hasCard {
		delete(b.agentCards, agentName)
		delete(b.agentPending, agentName)
		delete(b.agentState, agentName)
	}
	b.mu.Unlock()

	if hasCard {
		if b.state != nil {
			_ = b.state.RemoveAgentCard(agentName)
		}
		if _, _, err := b.api.DeleteMessageContext(ctx, ref.ChannelID, ref.Timestamp); err != nil {
			b.logger.Error("kill agent: failed to delete card", "agent", agentName, "error", err)
		}
	}
	return nil
}

// handleClearAgent handles the "Clear" button on a done/failed agent card.
// It closes the agent bead and removes the card from Slack.
func (b *Bot) handleClearAgent(ctx context.Context, agentIdentity string, callback slack.InteractionCallback) {
	if err := b.killAgent(ctx, agentIdentity); err != nil {
		b.logger.Error("clear agent: failed",
			"agent", agentIdentity, "error", err)
		_, _ = b.api.PostEphemeral(callback.Channel.ID, callback.User.ID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to clear agent %q: %s", extractAgentName(agentIdentity), err.Error()), false))
		return
	}
	b.logger.Info("cleared agent via Slack", "agent", agentIdentity, "user", callback.User.ID)
}
