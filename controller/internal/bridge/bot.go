// Package bridge provides the Slack Socket Mode bot for the slack-bridge.
//
// Bot wraps the SlackNotifier and adds Socket Mode for real-time events,
// slash commands, and interactive modals. It runs alongside the SSE stream
// for bead lifecycle events.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Bot is the Slack Socket Mode bot that handles interactive events.
type Bot struct {
	api    *slack.Client
	socket *socketmode.Client
	state  *StateManager
	daemon BeadClient
	router *Router
	logger *slog.Logger

	channel   string // default channel ID
	botUserID string // bot's own user ID (set on connect)

	// Health state.
	connected        atomic.Bool
	numConnections   atomic.Int32

	// In-memory decision tracking (augments StateManager).
	mu       sync.Mutex
	messages map[string]string // bead ID → Slack message ts (hot cache)
}

// BotConfig holds configuration for the Socket Mode bot.
type BotConfig struct {
	BotToken  string
	AppToken  string
	Channel   string
	Daemon    BeadClient
	State     *StateManager
	Router    *Router // optional channel router; nil = all to Channel
	Logger    *slog.Logger
	Debug     bool
}

// NewBot creates a new Socket Mode bot.
func NewBot(cfg BotConfig) *Bot {
	api := slack.New(
		cfg.BotToken,
		slack.OptionAppLevelToken(cfg.AppToken),
	)

	socket := socketmode.New(
		api,
		socketmode.OptionDebug(cfg.Debug),
	)

	b := &Bot{
		api:      api,
		socket:   socket,
		state:    cfg.State,
		daemon:   cfg.Daemon,
		router:   cfg.Router,
		logger:   cfg.Logger,
		channel:  cfg.Channel,
		messages: make(map[string]string),
	}

	// Hydrate hot cache from persisted state.
	if cfg.State != nil {
		for id, ref := range cfg.State.AllDecisionMessages() {
			b.messages[id] = ref.Timestamp
			_ = ref // channel info available if needed
		}
	}

	return b
}

// API returns the underlying Slack API client for direct API calls.
func (b *Bot) API() *slack.Client {
	return b.api
}

// IsConnected returns the bot's connection status.
func (b *Bot) IsConnected() bool {
	return b.connected.Load()
}

// NumConnections returns the number of active socket connections.
func (b *Bot) NumConnections() int {
	return int(b.numConnections.Load())
}

// Run starts the Socket Mode event loop. Blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	// Get bot user ID.
	auth, err := b.api.AuthTest()
	if err != nil {
		return fmt.Errorf("Slack auth test: %w", err)
	}
	b.botUserID = auth.UserID
	b.logger.Info("Slack bot authenticated", "user_id", b.botUserID, "team", auth.Team)

	go b.handleEvents(ctx)

	return b.socket.RunContext(ctx)
}

// handleEvents processes Socket Mode events.
func (b *Bot) handleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-b.socket.Events:
			if !ok {
				return
			}
			b.handleEvent(ctx, evt)
		}
	}
}

func (b *Bot) handleEvent(ctx context.Context, evt socketmode.Event) {
	b.logger.Debug("socket mode event received", "type", string(evt.Type))

	switch evt.Type {
	case socketmode.EventTypeConnecting:
		b.logger.Info("Slack Socket Mode connecting")

	case socketmode.EventTypeHello:
		if evt.Request != nil && evt.Request.NumConnections > 0 {
			b.numConnections.Store(int32(evt.Request.NumConnections))
			if evt.Request.NumConnections > 1 {
				b.logger.Warn("multiple Socket Mode connections detected",
					"num_connections", evt.Request.NumConnections)
			}
		}

	case socketmode.EventTypeConnected:
		b.connected.Store(true)
		b.logger.Info("Slack Socket Mode connected")

	case socketmode.EventTypeConnectionError:
		b.connected.Store(false)
		b.logger.Error("Slack Socket Mode connection error")

	case socketmode.EventTypeEventsAPI:
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		b.socket.Ack(*evt.Request)
		b.handleEventsAPI(ctx, eventsAPIEvent)

	case socketmode.EventTypeInteractive:
		callback, ok := evt.Data.(slack.InteractionCallback)
		if !ok {
			return
		}
		b.handleInteraction(ctx, evt, callback)

	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(slack.SlashCommand)
		if !ok {
			return
		}
		b.socket.Ack(*evt.Request)
		b.handleSlashCommand(ctx, cmd)
	}
}

// handleEventsAPI processes Events API events received via Socket Mode.
func (b *Bot) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent) {
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent
		switch ev := innerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			b.handleMessageEvent(ctx, ev)
		}
	}
}

// handleMessageEvent processes Slack message events (for thread replies and chat forwarding).
func (b *Bot) handleMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) {
	// Ignore bot's own messages and message subtypes (edits, deletes, etc.)
	if ev.User == b.botUserID || ev.User == "" || ev.SubType != "" {
		return
	}

	// Thread reply to a decision message.
	if ev.ThreadTimeStamp != "" && ev.ThreadTimeStamp != ev.TimeStamp {
		b.handleThreadReply(ctx, ev)
		return
	}

	// TODO: Channel-to-agent chat forwarding (bd-b0pnp).
}

// handleThreadReply processes replies to decision threads.
func (b *Bot) handleThreadReply(ctx context.Context, ev *slackevents.MessageEvent) {
	// Find which decision this thread belongs to.
	beadID := b.getDecisionByThread(ev.Channel, ev.ThreadTimeStamp)
	if beadID == "" {
		return // Not a decision thread we're tracking
	}

	// Get user info for attribution.
	user, err := b.api.GetUserInfo(ev.User)
	username := ev.User
	if err == nil {
		if user.RealName != "" {
			username = user.RealName
		} else if user.Name != "" {
			username = user.Name
		}
	}

	// Try to resolve the decision with the thread reply text.
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err != nil {
		b.logger.Error("failed to get decision for thread reply", "bead", beadID, "error", err)
		return
	}

	// If decision is still open, resolve it with the reply text.
	if bead.Status == "open" || bead.Status == "in_progress" {
		fields := map[string]string{
			"chosen":    ev.Text,
			"rationale": fmt.Sprintf("Thread reply by %s via Slack", username),
		}
		if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
			b.logger.Error("failed to resolve decision via thread reply",
				"bead", beadID, "error", err)
			return
		}
		// Confirm in thread.
		_, _, _ = b.api.PostMessage(ev.Channel,
			slack.MsgOptionText(fmt.Sprintf(":white_check_mark: Decision resolved by %s", username), false),
			slack.MsgOptionTS(ev.ThreadTimeStamp),
		)
		b.logger.Info("decision resolved via thread reply",
			"bead", beadID, "user", username)
	}
}

// getDecisionByThread reverse-maps (channel, thread_ts) to a bead ID.
func (b *Bot) getDecisionByThread(channelID, threadTS string) string {
	if b.state == nil {
		return ""
	}
	for id, ref := range b.state.AllDecisionMessages() {
		if ref.ChannelID == channelID && ref.Timestamp == threadTS {
			return id
		}
	}
	return ""
}

// handleInteraction processes interactive component callbacks (buttons, modals).
func (b *Bot) handleInteraction(ctx context.Context, evt socketmode.Event, callback slack.InteractionCallback) {
	switch callback.Type {
	case slack.InteractionTypeBlockActions:
		b.socket.Ack(*evt.Request)
		b.handleBlockActions(ctx, callback)

	case slack.InteractionTypeViewSubmission:
		b.socket.Ack(*evt.Request)
		b.handleViewSubmission(ctx, callback)

	default:
		b.logger.Debug("unhandled interaction type", "type", callback.Type)
	}
}

// handleBlockActions processes button clicks on decision messages.
func (b *Bot) handleBlockActions(ctx context.Context, callback slack.InteractionCallback) {
	for _, action := range callback.ActionCallback.BlockActions {
		actionID := action.ActionID

		switch {
		// Dismiss button: action_id = "dismiss_decision", value = beadID.
		case actionID == "dismiss_decision":
			b.handleDismiss(ctx, action.Value, callback)
			return

		// "Other..." button: action_id = "resolve_other_{beadID}", value = beadID.
		case strings.HasPrefix(actionID, "resolve_other_"):
			beadID := strings.TrimPrefix(actionID, "resolve_other_")
			b.openOtherModal(ctx, beadID, callback)
			return

		// Option button: action_id = "resolve_{beadID}_{n}", value = "{beadID}:{n}".
		case strings.HasPrefix(actionID, "resolve_"):
			// value is "beadID:optionIndex" — extract beadID and resolve.
			parts := strings.SplitN(action.Value, ":", 2)
			if len(parts) != 2 {
				continue
			}
			beadID := parts[0]
			optIndex := parts[1]
			// Look up the option label from the bead.
			chosen := b.resolveOptionLabel(ctx, beadID, optIndex)
			b.openResolveModal(ctx, beadID, chosen, callback)
			return
		}
	}
}

// resolveOptionLabel looks up the label for an option by index from the bead's fields.
func (b *Bot) resolveOptionLabel(ctx context.Context, beadID, optIndex string) string {
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err != nil {
		return fmt.Sprintf("Option %s", optIndex)
	}
	type optionObj struct {
		ID    string `json:"id"`
		Short string `json:"short"`
		Label string `json:"label"`
	}
	var opts []optionObj
	if raw, ok := bead.Fields["options"]; ok {
		_ = json.Unmarshal([]byte(raw), &opts)
	}
	// optIndex is 1-based.
	idx := 0
	if _, err := fmt.Sscanf(optIndex, "%d", &idx); err == nil && idx >= 1 && idx <= len(opts) {
		opt := opts[idx-1]
		if opt.Label != "" {
			return opt.Label
		}
		if opt.Short != "" {
			return opt.Short
		}
		return opt.ID
	}
	return fmt.Sprintf("Option %s", optIndex)
}

// openResolveModal opens a modal for confirming a decision choice with optional rationale.
func (b *Bot) openResolveModal(ctx context.Context, beadID, chosen string, callback slack.InteractionCallback) {
	// Build and open the modal immediately (trigger_id expires in 3s).
	titleText := slack.NewTextBlockObject("plain_text", "Resolve Decision", false, false)
	submitText := slack.NewTextBlockObject("plain_text", "Confirm", false, false)
	closeText := slack.NewTextBlockObject("plain_text", "Cancel", false, false)

	// Fetch the decision question for display.
	question := beadID // fallback
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err == nil {
		if q, ok := bead.Fields["question"]; ok {
			question = q
		}
	}

	blocks := slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*", question), false, false),
				nil, nil,
			),
			slack.NewDividerBlock(),
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf(":white_check_mark: Selected: *%s*", chosen), false, false),
				nil, nil,
			),
			slack.NewInputBlock(
				"rationale",
				slack.NewTextBlockObject("plain_text", "Rationale (optional)", false, false),
				nil,
				slack.NewPlainTextInputBlockElement(
					slack.NewTextBlockObject("plain_text", "Why this choice?", false, false),
					"rationale_input",
				),
			),
		},
	}

	// Make rationale optional.
	inputBlock := blocks.BlockSet[3].(*slack.InputBlock)
	inputBlock.Optional = true

	// Encode metadata: beadID|chosen|channelID|messageTS
	metadata := fmt.Sprintf("%s|%s|%s|%s", beadID, chosen, callback.Channel.ID, callback.Message.Timestamp)

	modal := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           titleText,
		Submit:          submitText,
		Close:           closeText,
		Blocks:          blocks,
		PrivateMetadata: metadata,
		CallbackID:      "resolve_decision",
	}

	if _, err := b.api.OpenView(callback.TriggerID, modal); err != nil {
		b.logger.Error("failed to open resolve modal", "bead", beadID, "error", err)
	}
}

// openOtherModal opens a modal for custom freeform text response.
func (b *Bot) openOtherModal(ctx context.Context, beadID string, callback slack.InteractionCallback) {
	titleText := slack.NewTextBlockObject("plain_text", "Custom Response", false, false)
	submitText := slack.NewTextBlockObject("plain_text", "Submit", false, false)
	closeText := slack.NewTextBlockObject("plain_text", "Cancel", false, false)

	// Fetch the decision question for display.
	question := beadID
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err == nil {
		if q, ok := bead.Fields["question"]; ok {
			question = q
		}
	}

	blocks := slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*", question), false, false),
				nil, nil,
			),
			slack.NewDividerBlock(),
			slack.NewInputBlock(
				"response",
				slack.NewTextBlockObject("plain_text", "Your Response", false, false),
				nil,
				&slack.PlainTextInputBlockElement{
					Type:        slack.METPlainTextInput,
					ActionID:    "response_input",
					Multiline:   true,
					Placeholder: slack.NewTextBlockObject("plain_text", "Type your response...", false, false),
				},
			),
		},
	}

	// Encode metadata: beadID|channelID|messageTS
	otherMetadata := fmt.Sprintf("%s|%s|%s", beadID, callback.Channel.ID, callback.Message.Timestamp)

	modal := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           titleText,
		Submit:          submitText,
		Close:           closeText,
		Blocks:          blocks,
		PrivateMetadata: otherMetadata,
		CallbackID:      "resolve_other",
	}

	if _, err := b.api.OpenView(callback.TriggerID, modal); err != nil {
		b.logger.Error("failed to open other modal", "bead", beadID, "error", err)
	}
}

// handleDismiss dismisses a decision (deletes message, closes bead).
func (b *Bot) handleDismiss(ctx context.Context, beadID string, callback slack.InteractionCallback) {
	fields := map[string]string{
		"chosen":    "dismissed",
		"rationale": fmt.Sprintf("Dismissed by @%s via Slack", callback.User.Name),
	}
	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to dismiss decision", "bead", beadID, "error", err)
		return
	}

	// Delete the Slack message.
	_, _, _ = b.api.DeleteMessage(callback.Channel.ID, callback.MessageTs)

	b.logger.Info("decision dismissed", "bead", beadID, "user", callback.User.Name)
}

// handleViewSubmission processes modal form submissions.
func (b *Bot) handleViewSubmission(ctx context.Context, callback slack.InteractionCallback) {
	switch callback.View.CallbackID {
	case "resolve_decision":
		b.handleResolveSubmission(ctx, callback)
	case "resolve_other":
		b.handleOtherSubmission(ctx, callback)
	}
}

// handleResolveSubmission processes the resolve decision modal submission.
func (b *Bot) handleResolveSubmission(ctx context.Context, callback slack.InteractionCallback) {
	metadata := callback.View.PrivateMetadata
	// Metadata format: beadID|chosen|channelID|messageTS
	parts := strings.SplitN(metadata, "|", 4)
	if len(parts) < 2 {
		b.logger.Error("invalid resolve modal metadata", "metadata", metadata)
		return
	}
	beadID := parts[0]
	chosen := parts[1]
	channelID := ""
	messageTS := ""
	if len(parts) >= 4 {
		channelID = parts[2]
		messageTS = parts[3]
	}

	// Extract rationale from form values.
	rationale := ""
	if v, ok := callback.View.State.Values["rationale"]["rationale_input"]; ok {
		rationale = v.Value
	}

	// Build attribution.
	user := callback.User.Name
	if rationale != "" {
		rationale = fmt.Sprintf("%s — @%s via Slack", rationale, user)
	} else {
		rationale = fmt.Sprintf("Chosen by @%s via Slack", user)
	}

	fields := map[string]string{
		"chosen":    chosen,
		"rationale": rationale,
	}
	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to resolve decision from modal",
			"bead", beadID, "error", err)
		return
	}

	// Directly update the Slack message to show resolved state.
	b.updateMessageResolved(ctx, beadID, chosen, rationale, channelID, messageTS)

	b.logger.Info("decision resolved via modal",
		"bead", beadID, "chosen", chosen, "user", user)
}

// handleOtherSubmission processes the custom response modal submission.
func (b *Bot) handleOtherSubmission(ctx context.Context, callback slack.InteractionCallback) {
	metadata := callback.View.PrivateMetadata
	// Metadata format: beadID|channelID|messageTS
	parts := strings.SplitN(metadata, "|", 3)
	beadID := parts[0]
	channelID := ""
	messageTS := ""
	if len(parts) >= 3 {
		channelID = parts[1]
		messageTS = parts[2]
	}

	response := ""
	if v, ok := callback.View.State.Values["response"]["response_input"]; ok {
		response = v.Value
	}
	if response == "" {
		return
	}

	user := callback.User.Name
	rationale := fmt.Sprintf("Custom response by @%s via Slack", user)

	fields := map[string]string{
		"chosen":    response,
		"rationale": rationale,
	}
	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to resolve decision from other modal",
			"bead", beadID, "error", err)
		return
	}

	// Directly update the Slack message to show resolved state.
	b.updateMessageResolved(ctx, beadID, response, rationale, channelID, messageTS)

	b.logger.Info("decision resolved via custom response",
		"bead", beadID, "response", response, "user", user)
}

// updateMessageResolved updates the original Slack message to show resolved state.
// It tries the provided channelID/messageTS first (from modal metadata), then falls
// back to the hot cache / state manager.
func (b *Bot) updateMessageResolved(ctx context.Context, beadID, chosen, rationale, channelID, messageTS string) {
	// Try hot cache first.
	if messageTS == "" {
		b.mu.Lock()
		if ts, ok := b.messages[beadID]; ok {
			messageTS = ts
			channelID = b.channel
		}
		b.mu.Unlock()
	}
	// Fall back to state manager.
	if messageTS == "" && b.state != nil {
		if ref, ok := b.state.GetDecisionMessage(beadID); ok {
			messageTS = ref.Timestamp
			channelID = ref.ChannelID
		}
	}

	if messageTS == "" || channelID == "" {
		b.logger.Debug("no Slack message found for resolved decision", "bead", beadID)
		return
	}

	text := fmt.Sprintf(":white_check_mark: *Resolved*: %s", chosen)
	if rationale != "" {
		text += fmt.Sprintf("\n_%s_", rationale)
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil, nil,
		),
	}

	_, _, _, err := b.api.UpdateMessageContext(ctx, channelID, messageTS,
		slack.MsgOptionText(fmt.Sprintf("Decision resolved: %s", chosen), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to update Slack message", "bead", beadID, "error", err)
		return
	}

	// Clean up tracking.
	b.mu.Lock()
	delete(b.messages, beadID)
	b.mu.Unlock()

	if b.state != nil {
		b.state.RemoveDecisionMessage(beadID)
	}
}

// handleSlashCommand processes Slack slash commands.
func (b *Bot) handleSlashCommand(ctx context.Context, cmd slack.SlashCommand) {
	switch cmd.Command {
	case "/decisions", "/decide":
		b.handleDecisionsCommand(ctx, cmd)
	case "/roster":
		b.handleRosterCommand(ctx, cmd)
	default:
		b.logger.Debug("unhandled slash command", "command", cmd.Command)
	}
}

// handleDecisionsCommand shows pending decisions as an ephemeral message.
func (b *Bot) handleDecisionsCommand(ctx context.Context, cmd slack.SlashCommand) {
	// TODO: List pending decisions from daemon (bd-gpvp8).
	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText("Pending decisions: (not yet implemented)", false),
	)
}

// handleRosterCommand shows the agent dashboard as an ephemeral message.
func (b *Bot) handleRosterCommand(ctx context.Context, cmd slack.SlashCommand) {
	// TODO: Show agent roster from daemon (bd-gpvp8).
	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText("Agent roster: (not yet implemented)", false),
	)
}

// --- Notifier interface implementation ---

// NotifyDecision posts a Block Kit message to Slack for a new decision.
// Layout matches the beads implementation: each option is a Section block
// with numbered label, description, and right-aligned accessory button.
func (b *Bot) NotifyDecision(ctx context.Context, bead BeadEvent) error {
	question := bead.Fields["question"]
	optionsRaw := bead.Fields["options"]
	agent := bead.Assignee

	// Parse options — try JSON array of objects first, then strings.
	type optionObj struct {
		ID          string `json:"id"`
		Short       string `json:"short"`
		Label       string `json:"label"`
		Description string `json:"description"`
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
					"*Other*\n_None of the above? Provide your own response._", false, false),
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

	// Thread under predecessor if present and tracked.
	var predecessorThreadTS string
	if predecessorID != "" {
		if ref, ok := b.lookupMessage(predecessorID); ok && ref.ChannelID == targetChannel {
			predecessorThreadTS = ref.Timestamp
			msgOpts = append(msgOpts, slack.MsgOptionTS(predecessorThreadTS))
		}
	}

	// Post the message.
	channelID, ts, err := b.api.PostMessageContext(ctx, targetChannel, msgOpts...)
	if err != nil {
		return fmt.Errorf("post decision to Slack: %w", err)
	}

	// Track the message.
	b.mu.Lock()
	b.messages[bead.ID] = ts
	b.mu.Unlock()

	if b.state != nil {
		b.state.SetDecisionMessage(bead.ID, MessageRef{
			ChannelID: channelID,
			Timestamp: ts,
			Agent:     agent,
		})
	}

	// Mark predecessor as superseded if we threaded under it.
	if predecessorThreadTS != "" {
		b.markDecisionSuperseded(ctx, predecessorID, bead.ID, targetChannel, predecessorThreadTS)
	}

	b.logger.Info("posted decision to Slack",
		"bead", bead.ID, "channel", channelID, "ts", ts,
		"predecessor", predecessorID)
	return nil
}

// UpdateDecision edits the Slack message to show resolved state.
// Called via SSE close event. The modal submission handler may have already
// updated the message directly, so this serves as a fallback.
func (b *Bot) UpdateDecision(ctx context.Context, beadID, chosen string) error {
	b.updateMessageResolved(ctx, beadID, chosen, "", "", "")
	return nil
}

// resolveChannel returns the target Slack channel for an agent.
// Uses the router if configured, otherwise falls back to the default channel.
func (b *Bot) resolveChannel(agent string) string {
	if b.router != nil && agent != "" {
		result := b.router.Resolve(agent)
		if result.ChannelID != "" {
			return result.ChannelID
		}
	}
	return b.channel
}

// lookupMessage finds a tracked decision message by bead ID.
// Checks hot cache first, then falls back to state manager.
func (b *Bot) lookupMessage(beadID string) (MessageRef, bool) {
	b.mu.Lock()
	ts, ok := b.messages[beadID]
	b.mu.Unlock()
	if ok {
		return MessageRef{ChannelID: b.channel, Timestamp: ts}, true
	}
	if b.state != nil {
		return b.state.GetDecisionMessage(beadID)
	}
	return MessageRef{}, false
}

// markDecisionSuperseded replaces the predecessor decision message with a
// "Superseded" notice linking to the new follow-up decision.
func (b *Bot) markDecisionSuperseded(ctx context.Context, predecessorID, newDecisionID, channelID, messageTS string) {
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("*Superseded*\n\nA follow-up decision (`%s`) has been posted in this thread.\n_Please refer to the latest decision in the thread below._", newDecisionID),
				false, false),
			nil, nil),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Original decision: `%s`", predecessorID), false, false)),
	}

	_, _, _, err := b.api.UpdateMessageContext(ctx, channelID, messageTS,
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to mark decision as superseded",
			"predecessor", predecessorID, "successor", newDecisionID, "error", err)
	}
}

// Ensure Bot implements Notifier.
var _ Notifier = (*Bot)(nil)
