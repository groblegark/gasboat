package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// decisionQuestion returns the question text from a decision bead's fields.
// Prefers the canonical "prompt" field, falling back to the legacy "question"
// field for backwards compatibility with older beads.
func decisionQuestion(fields map[string]string) string {
	if v := fields["prompt"]; v != "" {
		return v
	}
	return fields["question"]
}

// NotifyDecision posts a Block Kit message to Slack for a new decision.
// Layout matches the beads implementation: each option is a Section block
// with numbered label, description, and right-aligned accessory button.
func (b *Bot) NotifyDecision(ctx context.Context, bead BeadEvent) error {
	question := decisionQuestion(bead.Fields)
	optionsRaw := bead.Fields["options"]
	agent := bead.Assignee

	// Parse options — try JSON array of objects first, then strings.
	type optionObj struct {
		ID           string `json:"id"`
		Short        string `json:"short"`
		Label        string `json:"label"`
		Description  string `json:"description"`
		ArtifactType string `json:"artifact_type,omitempty"`
	}
	var optObjs []optionObj
	var optStrings []string

	if err := json.Unmarshal([]byte(optionsRaw), &optObjs); err != nil || len(optObjs) == 0 {
		if err := json.Unmarshal([]byte(optionsRaw), &optStrings); err != nil {
			optStrings = []string{optionsRaw}
		}
	}

	// Build Block Kit blocks — header section with question.
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf(":white_circle: *Decision Needed*\n%s", question), false, false),
			nil, nil,
		),
	}

	// Predecessor chain info.
	predecessorID := bead.Fields["predecessor_id"]
	if predecessorID != "" {
		chainText := fmt.Sprintf(":link: _Chained from: %s_", predecessorID)
		if iterStr := bead.Fields["iteration"]; iterStr != "" && iterStr != "1" {
			chainText = fmt.Sprintf(":link: _Iteration %s — chained from: %s_", iterStr, predecessorID)
		}
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", chainText, false, false),
		))
	}

	// Context block with agent info.
	if agent != "" {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Agent: `%s` | Bead: `%s`", agent, bead.ID), false, false),
		))
	} else {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Bead: `%s`", bead.ID), false, false),
		))
	}

	// Option blocks — each option is a Section with accessory button.
	if len(optObjs) > 0 || len(optStrings) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		if len(optObjs) > 0 {
			for i, opt := range optObjs {
				label := opt.Label
				if label == "" {
					label = opt.Short
				}
				if label == "" {
					label = opt.ID
				}

				optText := fmt.Sprintf("*%d. %s*", i+1, label)
				if opt.Description != "" {
					desc := opt.Description
					if len(desc) > 150 {
						desc = desc[:147] + "..."
					}
					optText += fmt.Sprintf("\n%s", desc)
				}
				if opt.ArtifactType != "" {
					optText += fmt.Sprintf("\n_Requires: %s_", opt.ArtifactType)
				}

				buttonLabel := "Choose"
				if len(optObjs) <= 4 {
					buttonLabel = fmt.Sprintf("Choose %d", i+1)
				}

				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", optText, false, false),
						nil,
						slack.NewAccessory(
							slack.NewButtonBlockElement(
								fmt.Sprintf("resolve_%s_%d", bead.ID, i+1),
								fmt.Sprintf("%s:%d", bead.ID, i+1),
								slack.NewTextBlockObject("plain_text", buttonLabel, false, false)))))
			}
		} else {
			for i, opt := range optStrings {
				optText := fmt.Sprintf("*%d. %s*", i+1, opt)

				buttonLabel := "Choose"
				if len(optStrings) <= 4 {
					buttonLabel = fmt.Sprintf("Choose %d", i+1)
				}

				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", optText, false, false),
						nil,
						slack.NewAccessory(
							slack.NewButtonBlockElement(
								fmt.Sprintf("resolve_%s_%d", bead.ID, i+1),
								fmt.Sprintf("%s:%d", bead.ID, i+1),
								slack.NewTextBlockObject("plain_text", buttonLabel, false, false)))))
			}
		}

		// "Other" option — own section with accessory button.
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn",
					"*Other*\n_None of the above? Provide a custom response and choose the required artifact type._", false, false),
				nil,
				slack.NewAccessory(
					slack.NewButtonBlockElement(
						fmt.Sprintf("resolve_other_%s", bead.ID),
						bead.ID,
						slack.NewTextBlockObject("plain_text", "Other...", false, false)))))
	}

	// Action buttons: Dismiss at the bottom.
	dismissBtn := slack.NewButtonBlockElement("dismiss_decision", bead.ID,
		slack.NewTextBlockObject("plain_text", "Dismiss", false, false))
	blocks = append(blocks,
		slack.NewActionBlock("", dismissBtn))

	// Build message options.
	msgOpts := []slack.MsgOption{
		slack.MsgOptionText(fmt.Sprintf("Decision needed: %s", question), false),
		slack.MsgOptionBlocks(blocks...),
	}

	// Resolve target channel for this agent.
	targetChannel := b.resolveChannel(agent)

	// Thread under agent card or predecessor decision.
	var threadTS string
	var threadSource string

	if b.agentThreadingEnabled() && agent != "" {
		// Agent threading mode: thread under the agent's status card.
		cardTS, err := b.ensureAgentCard(ctx, agent, targetChannel)
		if err != nil {
			b.logger.Error("failed to ensure agent card", "agent", agent, "error", err)
			// Fall through to flat posting.
		} else {
			threadTS = cardTS
			threadSource = "agent_card"
		}
	}

	// Predecessor threading (within the agent thread or top-level).
	if predecessorID != "" {
		if ref, ok := b.lookupMessage(predecessorID); ok && ref.ChannelID == targetChannel {
			// In agent mode, predecessor still chains within the thread.
			// In flat mode, predecessor creates the thread.
			if threadTS == "" {
				threadTS = ref.Timestamp
				threadSource = "predecessor"
			}
		}
	}

	if threadTS != "" {
		msgOpts = append(msgOpts, slack.MsgOptionTS(threadTS))
	}

	// Post the message.
	channelID, ts, err := b.api.PostMessageContext(ctx, targetChannel, msgOpts...)
	if err != nil {
		return fmt.Errorf("post decision to Slack: %w", err)
	}

	// Track the message and update pending count.
	ref := MessageRef{ChannelID: channelID, Timestamp: ts, Agent: agent}
	b.mu.Lock()
	b.messages[bead.ID] = ref
	if b.agentThreadingEnabled() && agent != "" {
		b.agentPending[agent]++
	}
	b.mu.Unlock()

	if b.state != nil {
		_ = b.state.SetDecisionMessage(bead.ID, ref)
	}

	// Update agent card with new pending count.
	if threadSource == "agent_card" {
		b.updateAgentCard(ctx, agent)
	}

	// Mark predecessor as superseded if we threaded under it (flat mode only).
	if threadSource == "predecessor" {
		b.markDecisionSuperseded(ctx, predecessorID, bead.ID, targetChannel, threadTS)
	}

	b.logger.Info("posted decision to Slack",
		"bead", bead.ID, "channel", channelID, "ts", ts,
		"thread_source", threadSource, "predecessor", predecessorID)
	return nil
}

// UpdateDecision edits the Slack message to show resolved state.
// Called via SSE close event. The modal submission handler may have already
// updated the message directly, so this serves as a fallback.
func (b *Bot) UpdateDecision(ctx context.Context, beadID, chosen string) error {
	b.updateMessageResolved(ctx, beadID, chosen, "", "", "")
	return nil
}

// NotifyEscalation posts a highlighted notification for an escalated decision.
func (b *Bot) NotifyEscalation(ctx context.Context, bead BeadEvent) error {
	question := decisionQuestion(bead.Fields)
	agent := bead.Assignee

	displayID := bead.ID
	text := fmt.Sprintf(":rotating_light: *ESCALATED: %s*\n%s", displayID, question)

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil, nil),
	}

	// Add context with agent and requester info.
	contextParts := []string{fmt.Sprintf("Bead: `%s`", bead.ID)}
	if agent != "" {
		contextParts = append([]string{fmt.Sprintf("Agent: `%s`", agent)}, contextParts...)
	}
	if requestedBy := bead.Fields["requested_by"]; requestedBy != "" {
		contextParts = append(contextParts, fmt.Sprintf("Requested by: %s", requestedBy))
	}
	blocks = append(blocks, slack.NewContextBlock("",
		slack.NewTextBlockObject("mrkdwn", strings.Join(contextParts, " | "), false, false)))

	targetChannel := b.resolveChannel(agent)

	msgOpts := []slack.MsgOption{
		slack.MsgOptionText(fmt.Sprintf("ESCALATED: %s — %s", displayID, question), false),
		slack.MsgOptionBlocks(blocks...),
	}

	// In agent threading mode, thread escalation under the agent's card.
	if b.agentThreadingEnabled() && agent != "" {
		if cardTS, err := b.ensureAgentCard(ctx, agent, targetChannel); err == nil {
			msgOpts = append(msgOpts, slack.MsgOptionTS(cardTS))
		}
	}

	_, _, err := b.api.PostMessageContext(ctx, targetChannel, msgOpts...)
	if err != nil {
		return fmt.Errorf("post escalation to Slack: %w", err)
	}

	b.logger.Info("posted escalation to Slack",
		"bead", bead.ID, "channel", targetChannel)
	return nil
}

// DismissDecision deletes the Slack message for an expired/dismissed decision.
func (b *Bot) DismissDecision(ctx context.Context, beadID string) error {
	ref, ok := b.lookupMessage(beadID)
	if !ok {
		b.logger.Debug("no Slack message found for dismissed decision", "bead", beadID)
		return nil
	}

	_, _, err := b.api.DeleteMessageContext(ctx, ref.ChannelID, ref.Timestamp)
	if err != nil {
		return fmt.Errorf("delete dismissed decision from Slack: %w", err)
	}

	// Clean up tracking and update agent card.
	b.mu.Lock()
	delete(b.messages, beadID)
	if b.agentThreadingEnabled() && ref.Agent != "" {
		if b.agentPending[ref.Agent] > 0 {
			b.agentPending[ref.Agent]--
		}
	}
	agent := ref.Agent
	b.mu.Unlock()

	if b.state != nil {
		_ = b.state.RemoveDecisionMessage(beadID)
	}

	if b.agentThreadingEnabled() && agent != "" {
		b.updateAgentCard(ctx, agent)
	}

	b.logger.Info("dismissed decision from Slack",
		"bead", beadID, "channel", ref.ChannelID)
	return nil
}

// reportEmoji returns an emoji for the given artifact/report type.
func reportEmoji(reportType string) string {
	switch reportType {
	case "plan":
		return ":clipboard:"
	case "checklist":
		return ":ballot_box_with_check:"
	case "diff-summary":
		return ":mag:"
	case "epic":
		return ":rocket:"
	case "bug":
		return ":bug:"
	default:
		return ":page_facing_up:"
	}
}

// PostReport posts a report as a thread reply on the linked decision's Slack message,
// and updates the resolved decision message to include a truncated inline preview.
func (b *Bot) PostReport(ctx context.Context, decisionID, reportType, content string) error {
	ref, ok := b.lookupMessage(decisionID)
	if !ok {
		b.logger.Debug("no Slack message found for report's decision", "decision", decisionID)
		return nil
	}

	// Truncate content for Slack block limits (3000 char max for text blocks).
	displayContent := content
	if len(displayContent) > 3000 {
		displayContent = displayContent[:2997] + "..."
	}

	emoji := reportEmoji(reportType)

	// 1. Post the full report as a thread reply.
	threadText := fmt.Sprintf("%s *Report (%s)*\n\n%s", emoji, reportType, displayContent)
	_, _, err := b.api.PostMessageContext(ctx, ref.ChannelID,
		slack.MsgOptionText(threadText, false),
		slack.MsgOptionTS(ref.Timestamp),
	)
	if err != nil {
		return fmt.Errorf("post report to Slack thread: %w", err)
	}

	// 2. Update the resolved decision message with an inline preview.
	preview := reportPreview(content, 4)
	previewText := fmt.Sprintf("\n\n%s *Report (%s)*\n%s", emoji, reportType, preview)

	// Fetch existing message text to append the preview.
	msgs, err := b.api.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: ref.ChannelID,
		Latest:    ref.Timestamp,
		Inclusive: true,
		Limit:     1,
	})
	if err == nil && len(msgs.Messages) > 0 {
		existing := msgs.Messages[0]
		var blocks []slack.Block
		// Preserve existing blocks and append report preview.
		if len(existing.Blocks.BlockSet) > 0 {
			blocks = existing.Blocks.BlockSet
		} else {
			blocks = []slack.Block{
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", existing.Text, false, false),
					nil, nil),
			}
		}
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", previewText, false, false),
			nil, nil))

		_, _, _, updateErr := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
			slack.MsgOptionBlocks(blocks...),
		)
		if updateErr != nil {
			b.logger.Warn("failed to inline report preview", "decision", decisionID, "error", updateErr)
		}
	}

	b.logger.Info("posted report to decision Slack thread",
		"decision", decisionID, "report_type", reportType, "channel", ref.ChannelID)
	return nil
}

// reportPreview returns the first N lines of content, with a "show more" hint if truncated.
func reportPreview(content string, maxLines int) string {
	lines := strings.SplitN(content, "\n", maxLines+1)
	if len(lines) <= maxLines {
		return "> " + strings.Join(lines, "\n> ")
	}
	remaining := strings.Count(content, "\n") - maxLines + 1
	preview := "> " + strings.Join(lines[:maxLines], "\n> ")
	preview += fmt.Sprintf("\n_%d more lines — see thread_ :thread:", remaining)
	return preview
}
