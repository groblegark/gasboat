package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// handleSlashCommand processes Slack slash commands.
func (b *Bot) handleSlashCommand(ctx context.Context, cmd slack.SlashCommand) {
	switch cmd.Command {
	case "/decisions", "/decide":
		b.handleDecisionsCommand(ctx, cmd)
	case "/roster":
		b.handleRosterCommand(ctx, cmd)
	case "/spawn":
		b.handleSpawnCommand(ctx, cmd)
	default:
		b.logger.Debug("unhandled slash command", "command", cmd.Command)
	}
}

// handleSpawnCommand processes the /spawn slash command.
// Usage: /spawn <agent> [project]
func (b *Bot) handleSpawnCommand(ctx context.Context, cmd slack.SlashCommand) {
	args := strings.Fields(strings.TrimSpace(cmd.Text))
	if len(args) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Usage: `/spawn <agent> [project]`", false))
		return
	}

	agentName := args[0]
	if !isValidAgentName(agentName) {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Invalid agent name %q — use lowercase letters, digits, and hyphens only", agentName), false))
		return
	}

	project := ""
	if len(args) >= 2 {
		project = args[1]
	}

	beadID, err := b.daemon.SpawnAgent(ctx, agentName, project)
	if err != nil {
		b.logger.Error("failed to spawn agent", "agent", agentName, "project", project, "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to spawn agent %q: %s", agentName, err.Error()), false))
		return
	}

	b.logger.Info("spawned agent via Slack", "agent", agentName, "project", project, "bead", beadID, "user", cmd.UserID)

	text := fmt.Sprintf(":rocket: Spawning agent *%s*", agentName)
	if project != "" {
		text += fmt.Sprintf(" in project *%s*", project)
	}
	text += fmt.Sprintf("\nBead: `%s` · Use `/roster` to check status.", beadID)
	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText(text, false))
}

// isValidAgentName reports whether s is a valid agent name.
// Valid names are non-empty and contain only lowercase letters, digits, and hyphens.
func isValidAgentName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// handleDecisionsCommand shows pending decisions as an ephemeral message.
func (b *Bot) handleDecisionsCommand(ctx context.Context, cmd slack.SlashCommand) {
	decisions, err := b.daemon.ListDecisionBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list decisions", "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Failed to fetch decisions", false))
		return
	}

	if len(decisions) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":white_check_mark: No pending decisions! All decisions have been resolved.", false))
		return
	}

	// Count escalated decisions.
	escalatedCount := 0
	for _, d := range decisions {
		for _, label := range d.Labels {
			if label == "escalated" {
				escalatedCount++
				break
			}
		}
	}

	// Build summary header.
	headerText := fmt.Sprintf(":clipboard: *%d Pending Decision", len(decisions))
	if len(decisions) != 1 {
		headerText += "s"
	}
	headerText += "*"
	if escalatedCount > 0 {
		headerText += fmt.Sprintf(" (%d :rotating_light: escalated)", escalatedCount)
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
		slack.NewDividerBlock(),
	}

	// Per-decision entries (limit to 15 to stay within Slack block limits).
	limit := 15
	if len(decisions) < limit {
		limit = len(decisions)
	}
	for _, d := range decisions[:limit] {
		question := d.Fields["question"]
		if question == "" {
			question = d.Title
		}
		if len(question) > 100 {
			question = question[:97] + "..."
		}

		// Urgency indicator.
		urgency := ":white_circle:"
		for _, label := range d.Labels {
			if label == "escalated" {
				urgency = ":rotating_light:"
				break
			}
		}

		// Build text line.
		line := fmt.Sprintf("%s `%s`", urgency, d.ID)
		if d.Assignee != "" {
			line += fmt.Sprintf(" — `%s`", d.Assignee)
		}
		line += fmt.Sprintf("\n%s", question)

		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", line, false, false),
				nil,
				slack.NewAccessory(
					slack.NewButtonBlockElement(
						"view_decision_"+d.ID,
						d.ID,
						slack.NewTextBlockObject("plain_text", "View", false, false)))))
	}

	if len(decisions) > limit {
		blocks = append(blocks,
			slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_...and %d more_", len(decisions)-limit), false, false)))
	}

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionBlocks(blocks...))
}

// handleRosterCommand shows the agent dashboard as an ephemeral message.
func (b *Bot) handleRosterCommand(ctx context.Context, cmd slack.SlashCommand) {
	agents, err := b.daemon.ListAgentBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list agents", "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Failed to fetch agent roster", false))
		return
	}

	if len(agents) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":busts_in_silhouette: No active agents", false))
		return
	}

	// Build roster blocks.
	headerText := fmt.Sprintf(":busts_in_silhouette: *Agent Roster* — %d active", len(agents))

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
		slack.NewDividerBlock(),
	}

	// Limit display to 20 agents.
	limit := 20
	if len(agents) < limit {
		limit = len(agents)
	}
	for _, a := range agents[:limit] {
		line := fmt.Sprintf(":large_green_circle: `%s`", a.ID)
		if a.Project != "" {
			line += fmt.Sprintf(" — *%s*", a.Project)
		}
		if a.Role != "" {
			line += fmt.Sprintf(" (%s/%s)", a.Mode, a.Role)
		}

		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", line, false, false),
				nil, nil))
	}

	if len(agents) > limit {
		blocks = append(blocks,
			slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_...and %d more_", len(agents)-limit), false, false)))
	}

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionBlocks(blocks...))
}
