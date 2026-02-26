package bridge

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"
)

// handleClearAgent handles the "Clear" button on a done/failed agent card.
// It closes the agent bead and removes the card from Slack.
func (b *Bot) handleClearAgent(ctx context.Context, agentIdentity string, callback slack.InteractionCallback) {
	// Look up the agent bead to get its ID.
	bead, err := b.daemon.FindAgentBead(ctx, agentIdentity)
	if err != nil {
		b.logger.Error("clear agent: failed to find agent bead",
			"agent", agentIdentity, "error", err)
		_, _ = b.api.PostEphemeral(callback.Channel.ID, callback.User.ID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to find agent bead for %q", extractAgentName(agentIdentity)), false))
		return
	}

	// Close the agent bead.
	if err := b.daemon.CloseBead(ctx, bead.ID, nil); err != nil {
		b.logger.Error("clear agent: failed to close agent bead",
			"agent", agentIdentity, "bead", bead.ID, "error", err)
		_, _ = b.api.PostEphemeral(callback.Channel.ID, callback.User.ID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to clear agent %q: %s", extractAgentName(agentIdentity), err.Error()), false))
		return
	}

	b.logger.Info("cleared agent via Slack", "agent", agentIdentity, "bead", bead.ID, "user", callback.User.ID)

	// Remove the agent card from Slack by deleting the message.
	b.mu.Lock()
	ref, hasCard := b.agentCards[agentIdentity]
	if hasCard {
		delete(b.agentCards, agentIdentity)
		delete(b.agentPending, agentIdentity)
		delete(b.agentState, agentIdentity)
	}
	b.mu.Unlock()

	if hasCard {
		if b.state != nil {
			_ = b.state.RemoveAgentCard(agentIdentity)
		}
		_, _, err := b.api.DeleteMessageContext(ctx, ref.ChannelID, ref.Timestamp)
		if err != nil {
			b.logger.Error("clear agent: failed to delete card", "agent", agentIdentity, "error", err)
		}
	}
}
