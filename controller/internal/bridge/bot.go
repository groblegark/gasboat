// Package bridge provides the Slack Socket Mode bot for the slack-bridge.
//
// Bot wraps the SlackNotifier and adds Socket Mode for real-time events,
// slash commands, and interactive modals. It runs alongside the SSE stream
// for bead lifecycle events.
//
// The Bot implementation is split across five files:
//   - bot.go — core struct, event dispatch, helpers
//   - bot_commands.go — slash command handlers (/spawn, /decisions, /roster)
//   - bot_decisions.go — decision notifications, modals, resolve/dismiss
//   - bot_mentions.go — @mention handling in agent threads
//   - bot_notifications.go — agent crash, jack on/off/expired alerts
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gasboat/controller/internal/beadsapi"

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

	// Threading mode: "" / "flat" = flat messages, "agent" = threaded under agent cards.
	threadingMode string

	// In-memory decision tracking (augments StateManager).
	mu           sync.Mutex
	messages     map[string]MessageRef // bead ID → Slack message ref (hot cache)
	agentCards   map[string]MessageRef // agent identity → status card ref (hot cache)
	agentPending map[string]int        // agent identity → pending decision count
	agentState   map[string]string     // agent identity → last known agent_state
	agentSeen    map[string]time.Time  // agent identity → last activity timestamp
}

// BotConfig holds configuration for the Socket Mode bot.
type BotConfig struct {
	BotToken       string
	AppToken       string
	Channel        string
	ThreadingMode  string // "agent" (default) or "flat" — controls decision threading
	Daemon         BeadClient
	State          *StateManager
	Router         *Router // optional channel router; nil = all to Channel
	Logger         *slog.Logger
	Debug          bool
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
		api:           api,
		socket:        socket,
		state:         cfg.State,
		daemon:        cfg.Daemon,
		router:        cfg.Router,
		logger:        cfg.Logger,
		channel:       cfg.Channel,
		threadingMode: cfg.ThreadingMode,
		messages:      make(map[string]MessageRef),
		agentCards:    make(map[string]MessageRef),
		agentPending:  make(map[string]int),
		agentState:    make(map[string]string),
		agentSeen:     make(map[string]time.Time),
	}

	// Hydrate hot caches from persisted state.
	if cfg.State != nil {
		for id, ref := range cfg.State.AllDecisionMessages() {
			b.messages[id] = ref
		}
		for agent, ref := range cfg.State.AllAgentCards() {
			b.agentCards[agent] = ref
		}
	}

	// Count pending decisions per agent from hydrated messages.
	if b.agentThreadingEnabled() {
		for _, ref := range b.messages {
			if ref.Agent != "" {
				b.agentPending[ref.Agent]++
			}
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
		case *slackevents.AppMentionEvent:
			b.handleAppMention(ctx, ev)
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

	// Channel-to-agent chat forwarding (bd-b0pnp).
	b.handleChatForward(ctx, ev)
}

// handleChatForward creates a tracking bead for a Slack message directed at an agent.
// The router's override mapping determines which channel maps to which agent.
func (b *Bot) handleChatForward(ctx context.Context, ev *slackevents.MessageEvent) {
	if b.router == nil {
		return
	}
	agent := b.router.GetAgentByChannel(ev.Channel)
	if agent == "" {
		return // Not a mapped agent channel
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

	// Build bead title (truncated) and description with slack metadata tag.
	title := truncateText(fmt.Sprintf("Slack: %s", ev.Text), 80)
	slackTag := fmt.Sprintf("[slack:%s:%s]", ev.Channel, ev.TimeStamp)
	description := fmt.Sprintf("Message from %s in Slack:\n\n%s\n\n---\n%s", username, ev.Text, slackTag)

	// Create tracking bead assigned to the agent.
	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Description: description,
		Assignee:    extractAgentName(agent),
		Labels:      []string{"slack-chat"},
		Priority:    2,
	})
	if err != nil {
		b.logger.Error("failed to create chat bead",
			"channel", ev.Channel, "agent", agent, "error", err)
		return
	}

	b.logger.Info("chat forwarding: created tracking bead",
		"bead", beadID, "agent", agent, "user", username)

	// Persist message ref for response relay.
	if b.state != nil {
		_ = b.state.SetChatMessage(beadID, MessageRef{
			ChannelID: ev.Channel,
			Timestamp: ev.TimeStamp,
			Agent:     agent,
		})
	}

	// Post confirmation in thread.
	_, _, _ = b.api.PostMessage(ev.Channel,
		slack.MsgOptionText(
			fmt.Sprintf(":speech_balloon: Forwarded to *%s* (tracking: `%s`)", extractAgentName(agent), beadID),
			false),
		slack.MsgOptionTS(ev.TimeStamp),
	)
}

// extractAgentName returns the last segment of an agent identity.
// "gasboat/crew/test-bot" → "test-bot", "test-bot" → "test-bot"
func extractAgentName(identity string) string {
	if i := strings.LastIndex(identity, "/"); i >= 0 {
		return identity[i+1:]
	}
	return identity
}

// formatAge formats the duration since t as a compact human-readable string.
// Examples: "just now", "2m ago", "1h ago", "3d ago".
func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// truncateText truncates s to maxLen, appending "..." if truncated.
func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
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

// agentThreadingEnabled returns true if agent card threading is active.
func (b *Bot) agentThreadingEnabled() bool {
	return b.threadingMode == "agent"
}

// agentTaskTitle fetches the title of the task currently claimed by agent.
// Returns "" if none is found or on error.
func (b *Bot) agentTaskTitle(ctx context.Context, agent string) string {
	bead, err := b.daemon.ListAssignedTask(ctx, agent)
	if err != nil || bead == nil {
		return ""
	}
	return bead.Title
}

// ensureAgentCard posts or retrieves the agent status card for threading.
// Returns the card's message timestamp for use as threadTS.
func (b *Bot) ensureAgentCard(ctx context.Context, agent, channelID string) (string, error) {
	b.mu.Lock()
	if ref, ok := b.agentCards[agent]; ok && ref.ChannelID == channelID {
		b.mu.Unlock()
		return ref.Timestamp, nil
	}
	b.mu.Unlock()

	// Post a new status card.
	b.mu.Lock()
	pending := b.agentPending[agent]
	state := b.agentState[agent]
	seen := b.agentSeen[agent]
	b.mu.Unlock()

	taskTitle := b.agentTaskTitle(ctx, agent)
	blocks := buildAgentCardBlocks(agent, pending, state, taskTitle, seen)
	cardChannel, ts, err := b.api.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(fmt.Sprintf("Agent: %s", extractAgentName(agent)), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		return "", fmt.Errorf("post agent card: %w", err)
	}

	ref := MessageRef{ChannelID: cardChannel, Timestamp: ts, Agent: agent}

	b.mu.Lock()
	b.agentCards[agent] = ref
	b.mu.Unlock()

	if b.state != nil {
		_ = b.state.SetAgentCard(agent, ref)
	}

	b.logger.Info("posted agent status card", "agent", agent, "channel", cardChannel, "ts", ts)
	return ts, nil
}

// NotifyAgentState is called when an agent bead's agent_state changes.
// It records the new state and refreshes the agent card in Slack.
func (b *Bot) NotifyAgentState(_ context.Context, bead BeadEvent) {
	agent := bead.Assignee
	if agent == "" {
		return
	}
	state := bead.Fields["agent_state"]
	b.mu.Lock()
	b.agentState[agent] = state
	b.agentSeen[agent] = time.Now()
	b.mu.Unlock()

	// Refresh the card if one exists for this agent.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b.updateAgentCard(ctx, agent)
}

// updateAgentCard updates the agent status card with the current pending count, state, and task.
func (b *Bot) updateAgentCard(ctx context.Context, agent string) {
	b.mu.Lock()
	ref, ok := b.agentCards[agent]
	pending := b.agentPending[agent]
	state := b.agentState[agent]
	seen := b.agentSeen[agent]
	b.mu.Unlock()

	if !ok {
		return
	}

	taskTitle := b.agentTaskTitle(ctx, agent)
	blocks := buildAgentCardBlocks(agent, pending, state, taskTitle, seen)
	_, _, _, err := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
		slack.MsgOptionText(fmt.Sprintf("Agent: %s", extractAgentName(agent)), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to update agent card", "agent", agent, "error", err)
	}
}

// buildAgentCardBlocks constructs Block Kit blocks for an agent status card.
// agentState is the agent's current lifecycle state (spawning, working, etc.).
// taskTitle is the title of the bead the agent currently has in_progress ("" if idle).
// seen is the last time activity was recorded for this agent (zero = unknown).
func buildAgentCardBlocks(agent string, pendingCount int, agentState, taskTitle string, seen time.Time) []slack.Block {
	name := extractAgentName(agent)
	project := extractAgentProject(agent)

	var indicator, status string
	switch {
	case pendingCount > 0:
		indicator = ":large_blue_circle:"
		status = fmt.Sprintf("%d pending", pendingCount)
	case agentState == "working":
		indicator = ":large_green_circle:"
		status = "working"
	case agentState == "spawning":
		indicator = ":hourglass_flowing_sand:"
		status = "starting"
	default:
		indicator = ":white_circle:"
		status = "idle"
	}

	headerText := fmt.Sprintf("%s *%s*", indicator, name)
	if project != "" {
		headerText += fmt.Sprintf(" \u00b7 _%s_", project)
	}
	headerText += fmt.Sprintf(" \u00b7 %s", status)

	contextText := fmt.Sprintf("`%s` \u00b7 Decisions thread below", agent)
	if !seen.IsZero() {
		contextText += fmt.Sprintf(" \u00b7 _%s_", formatAge(seen))
	}
	if taskTitle != "" {
		contextText += fmt.Sprintf("\n:wrench: %s", truncateText(taskTitle, 80))
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", contextText, false, false)),
	}

	// Show a Clear button for terminated agents so humans can dismiss them from Slack.
	if agentState == "done" || agentState == "failed" {
		clearBtn := slack.NewButtonBlockElement(
			"clear_agent",
			agent,
			slack.NewTextBlockObject("plain_text", "Clear", false, false),
		)
		blocks = append(blocks, slack.NewActionBlock("", clearBtn))
	}

	return blocks
}

// extractAgentProject returns the first segment (project) of an agent identity.
// "gasboat/crew/test-bot" → "gasboat", "test-bot" → ""
func extractAgentProject(identity string) string {
	if i := strings.Index(identity, "/"); i >= 0 {
		return identity[:i]
	}
	return ""
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
	ref, ok := b.messages[beadID]
	b.mu.Unlock()
	if ok {
		return ref, true
	}
	if b.state != nil {
		return b.state.GetDecisionMessage(beadID)
	}
	return MessageRef{}, false
}

// updateMessageResolved updates the original Slack message to show resolved state.
// It tries the provided channelID/messageTS first (from modal metadata), then falls
// back to the hot cache / state manager.
func (b *Bot) updateMessageResolved(ctx context.Context, beadID, chosen, rationale, channelID, messageTS string) {
	// Try hot cache first.
	if messageTS == "" {
		b.mu.Lock()
		if ref, ok := b.messages[beadID]; ok {
			messageTS = ref.Timestamp
			channelID = ref.ChannelID
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

	// Decrement agent pending count and update card, but keep the message
	// ref so that PostReport can still thread under the resolved decision.
	b.mu.Lock()
	ref, hadRef := b.messages[beadID]
	if hadRef && b.agentThreadingEnabled() && ref.Agent != "" {
		if b.agentPending[ref.Agent] > 0 {
			b.agentPending[ref.Agent]--
		}
	}
	agent := ref.Agent
	b.mu.Unlock()

	if hadRef && b.agentThreadingEnabled() && agent != "" {
		b.updateAgentCard(ctx, agent)
	}
}

// Ensure Bot implements Notifier, AgentNotifier, and JackNotifier.
var _ Notifier = (*Bot)(nil)
var _ AgentNotifier = (*Bot)(nil)
var _ JackNotifier = (*Bot)(nil)
