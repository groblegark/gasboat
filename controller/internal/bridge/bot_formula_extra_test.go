package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// --- handleFormulaShow tests ---

func TestHandleFormulaShow_ByID(t *testing.T) {
	daemon := newMockDaemon()

	vars := []formulaVarDef{
		{Name: "env", Required: true, Description: "Target environment", Enum: []string{"staging", "prod"}},
		{Name: "version", Default: "latest"},
	}
	steps := []formulaStep{
		{ID: "build", Title: "Build service", Type: "task"},
		{ID: "deploy", Title: "Deploy to {{env}}", DependsOn: []string{"build"}, Role: "devops", Project: "infra", SuggestNewAgent: true},
	}
	varsJSON, _ := json.Marshal(vars)
	stepsJSON, _ := json.Marshal(steps)

	daemon.mu.Lock()
	daemon.beads["kd-formula-show1"] = &beadsapi.BeadDetail{
		ID:          "kd-formula-show1",
		Title:       "Deploy Pipeline",
		Type:        "formula",
		Description: "A formula for deploying services",
		Fields: map[string]string{
			"vars":  string(varsJSON),
			"steps": string(stepsJSON),
		},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Should not panic; posts ephemeral with formula details.
	bot.handleFormulaShow(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	}, "kd-formula-show1")
}

func TestHandleFormulaShow_ByID_NotFound(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// GetBead on mock returns an empty bead (type != "formula"), so resolveFormula
	// will return an error about wrong type.
	bot.handleFormulaShow(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	}, "kd-nonexistent")
}

func TestHandleFormulaShow_ByName_SingleMatch(t *testing.T) {
	daemon := newMockDaemon()

	daemon.mu.Lock()
	daemon.beads["kd-formula-named"] = &beadsapi.BeadDetail{
		ID:     "kd-formula-named",
		Title:  "My Deploy Formula",
		Type:   "formula",
		Status: "open",
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Search by name (not starting with kd-).
	bot.handleFormulaShow(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	}, "deploy")
}

func TestHandleFormulaShow_NoVarsNoSteps(t *testing.T) {
	daemon := newMockDaemon()

	daemon.mu.Lock()
	daemon.beads["kd-formula-empty"] = &beadsapi.BeadDetail{
		ID:     "kd-formula-empty",
		Title:  "Empty Formula",
		Type:   "formula",
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleFormulaShow(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	}, "kd-formula-empty")
}

func TestHandleFormulaShow_LongDescription(t *testing.T) {
	daemon := newMockDaemon()

	longDesc := ""
	for i := 0; i < 250; i++ {
		longDesc += "x"
	}

	daemon.mu.Lock()
	daemon.beads["kd-formula-long"] = &beadsapi.BeadDetail{
		ID:          "kd-formula-long",
		Title:       "Long Desc Formula",
		Type:        "formula",
		Description: longDesc,
		Fields:      map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Should truncate description to 200 chars without panic.
	bot.handleFormulaShow(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	}, "kd-formula-long")
}

func TestHandleFormulaShow_StepWithConditions(t *testing.T) {
	daemon := newMockDaemon()

	steps := []formulaStep{
		{ID: "s1", Title: "Step One", Type: "bug", DependsOn: []string{"s0"}},
		{ID: "s2", Title: "Step Two", Project: "infra"},
		{ID: "s3", Title: "Step Three", Role: "devops"},
		{ID: "s4", Title: "Step Four", Project: "infra", Role: "devops", SuggestNewAgent: true},
	}
	stepsJSON, _ := json.Marshal(steps)

	daemon.mu.Lock()
	daemon.beads["kd-formula-steps"] = &beadsapi.BeadDetail{
		ID:    "kd-formula-steps",
		Title: "Steps Formula",
		Type:  "formula",
		Fields: map[string]string{
			"steps": string(stepsJSON),
		},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleFormulaShow(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	}, "kd-formula-steps")
}

// --- handleFormulaCommand routing tests ---

func TestHandleFormulaCommand_ShowSubcommand(t *testing.T) {
	daemon := newMockDaemon()

	daemon.mu.Lock()
	daemon.beads["kd-formula-cmd"] = &beadsapi.BeadDetail{
		ID:     "kd-formula-cmd",
		Title:  "Test Formula",
		Type:   "formula",
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleFormulaCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
		Text:      "show kd-formula-cmd",
	})
}

func TestHandleFormulaCommand_ShowMissingArg(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// "show" without ID should post usage error.
	bot.handleFormulaCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
		Text:      "show",
	})
}

func TestHandleFormulaCommand_Help(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleFormulaCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
		Text:      "help",
	})
}

func TestHandleFormulaCommand_Unknown(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleFormulaCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
		Text:      "bogus",
	})
}

// --- resolveFormula tests ---

func TestResolveFormula_ByID_WrongType(t *testing.T) {
	daemon := newMockDaemon()

	daemon.mu.Lock()
	daemon.beads["kd-not-formula"] = &beadsapi.BeadDetail{
		ID:   "kd-not-formula",
		Type: "task",
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	_, err := bot.resolveFormula(context.Background(), "kd-not-formula")
	if err == nil {
		t.Fatal("expected error for non-formula bead type")
	}
}

func TestResolveFormula_ByID_TemplateType(t *testing.T) {
	daemon := newMockDaemon()

	daemon.mu.Lock()
	daemon.beads["kd-template-1"] = &beadsapi.BeadDetail{
		ID:   "kd-template-1",
		Type: "template",
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	f, err := bot.resolveFormula(context.Background(), "kd-template-1")
	if err != nil {
		t.Fatalf("expected template type to be accepted, got error: %v", err)
	}
	if f.ID != "kd-template-1" {
		t.Errorf("expected ID kd-template-1, got %s", f.ID)
	}
}

func TestResolveFormula_ByName_NoMatches(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	_, err := bot.resolveFormula(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error when no formula matches")
	}
}

func TestResolveFormula_ByName_MultipleMatches(t *testing.T) {
	daemon := newMockDaemon()

	daemon.mu.Lock()
	daemon.beads["kd-f1"] = &beadsapi.BeadDetail{ID: "kd-f1", Title: "Deploy A", Type: "formula", Status: "open"}
	daemon.beads["kd-f2"] = &beadsapi.BeadDetail{ID: "kd-f2", Title: "Deploy B", Type: "formula", Status: "open"}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	_, err := bot.resolveFormula(context.Background(), "deploy")
	if err == nil {
		t.Fatal("expected error for multiple matching formulas")
	}
}
