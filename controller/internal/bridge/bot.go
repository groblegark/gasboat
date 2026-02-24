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
		blockID := action.BlockID

		// Decision option buttons: block_id = "decision_{beadID}".
		beadID := strings.TrimPrefix(blockID, "decision_")
		if beadID == blockID {
			continue // Not a decision action
		}

		// Check for special actions.
		switch {
		case strings.HasPrefix(action.ActionID, "dismiss_"):
			b.handleDismiss(ctx, beadID, callback)
			return

		case strings.HasPrefix(action.ActionID, "resolve_other_"):
			b.openOtherModal(ctx, beadID, callback)
			return

		default:
			// Regular option selection — open resolve modal.
			b.openResolveModal(ctx, beadID, action.Value, callback)
		}
	}
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

	// Encode metadata: beadID:chosen
	metadata := fmt.Sprintf("%s|%s", beadID, chosen)

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

	modal := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           titleText,
		Submit:          submitText,
		Close:           closeText,
		Blocks:          blocks,
		PrivateMetadata: beadID,
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
	parts := strings.SplitN(metadata, "|", 2)
	if len(parts) != 2 {
		b.logger.Error("invalid resolve modal metadata", "metadata", metadata)
		return
	}
	beadID := parts[0]
	chosen := parts[1]

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

	b.logger.Info("decision resolved via modal",
		"bead", beadID, "chosen", chosen, "user", user)
}

// handleOtherSubmission processes the custom response modal submission.
func (b *Bot) handleOtherSubmission(ctx context.Context, callback slack.InteractionCallback) {
	beadID := callback.View.PrivateMetadata

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

	b.logger.Info("decision resolved via custom response",
		"bead", beadID, "response", response, "user", user)
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
func (b *Bot) NotifyDecision(ctx context.Context, bead BeadEvent) error {
	question := bead.Fields["question"]
	optionsRaw := bead.Fields["options"]
	agent := bead.Assignee

	// Parse options — try JSON array of objects first, then strings.
	type optionObj struct {
		ID    string `json:"id"`
		Short string `json:"short"`
		Label string `json:"label"`
	}
	var optObjs []optionObj
	var optStrings []string

	if err := json.Unmarshal([]byte(optionsRaw), &optObjs); err != nil || len(optObjs) == 0 {
		if err := json.Unmarshal([]byte(optionsRaw), &optStrings); err != nil {
			optStrings = []string{optionsRaw}
		}
	}

	// Build Block Kit blocks.
	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "Decision Needed", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*", question), false, false),
			nil, nil,
		),
	}

	// Context block with agent info.
	if agent != "" {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Agent: `%s` | Bead: `%s`", agent, bead.ID), false, false),
		))
	}

	// Action buttons.
	var buttons []slack.BlockElement
	if len(optObjs) > 0 {
		for i, opt := range optObjs {
			label := opt.Short
			if label == "" {
				label = opt.Label
			}
			if label == "" {
				label = opt.ID
			}
			btn := slack.NewButtonBlockElement(
				fmt.Sprintf("decision_%s_%d", bead.ID, i),
				label,
				slack.NewTextBlockObject("plain_text", label, false, false),
			)
			if i == 0 {
				btn.Style = slack.StylePrimary
			}
			buttons = append(buttons, btn)
		}
	} else {
		for i, opt := range optStrings {
			btn := slack.NewButtonBlockElement(
				fmt.Sprintf("decision_%s_%d", bead.ID, i),
				opt,
				slack.NewTextBlockObject("plain_text", opt, false, false),
			)
			if i == 0 {
				btn.Style = slack.StylePrimary
			}
			buttons = append(buttons, btn)
		}
	}

	// Add "Other..." and "Dismiss" buttons.
	otherBtn := slack.NewButtonBlockElement(
		fmt.Sprintf("resolve_other_%s", bead.ID),
		"other",
		slack.NewTextBlockObject("plain_text", "Other...", false, false),
	)
	buttons = append(buttons, otherBtn)

	dismissBtn := slack.NewButtonBlockElement(
		fmt.Sprintf("dismiss_%s", bead.ID),
		"dismiss",
		slack.NewTextBlockObject("plain_text", "Dismiss", false, false),
	)
	dismissBtn.Style = slack.StyleDanger
	buttons = append(buttons, dismissBtn)

	blocks = append(blocks, slack.NewActionBlock(
		"decision_"+bead.ID,
		buttons...,
	))

	// Post the message.
	channelID, ts, err := b.api.PostMessageContext(ctx, b.channel,
		slack.MsgOptionText(fmt.Sprintf("Decision needed: %s", question), false),
		slack.MsgOptionBlocks(blocks...),
	)
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

	b.logger.Info("posted decision to Slack",
		"bead", bead.ID, "channel", channelID, "ts", ts)
	return nil
}

// UpdateDecision edits the Slack message to show resolved state.
func (b *Bot) UpdateDecision(ctx context.Context, beadID, chosen string) error {
	b.mu.Lock()
	ts, ok := b.messages[beadID]
	b.mu.Unlock()

	if !ok {
		b.logger.Debug("no Slack message found for resolved decision", "bead", beadID)
		return nil
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("~Decision needed~ — :white_check_mark: *Resolved*: %s", chosen), false, false),
			nil, nil,
		),
	}

	_, _, _, err := b.api.UpdateMessageContext(ctx, b.channel, ts,
		slack.MsgOptionText(fmt.Sprintf("Decision resolved: %s", chosen), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		return fmt.Errorf("update decision in Slack: %w", err)
	}

	// Clean up tracking.
	b.mu.Lock()
	delete(b.messages, beadID)
	b.mu.Unlock()

	if b.state != nil {
		b.state.RemoveDecisionMessage(beadID)
	}

	return nil
}

// Ensure Bot implements Notifier.
var _ Notifier = (*Bot)(nil)
