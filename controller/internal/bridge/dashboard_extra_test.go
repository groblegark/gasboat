package bridge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// --- Block builder tests ---

func TestDashboardAgentWorkingBlock_Basic(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "hq",
		Project:   "gasboat",
		Mode:      "crew",
		Role:      "lead",
		Metadata:  map[string]string{},
	}
	block := dashboardAgentWorkingBlock(a, "")
	section, ok := block.(*slack.SectionBlock)
	if !ok {
		t.Fatalf("expected *slack.SectionBlock, got %T", block)
	}
	text := section.Text.Text
	if !strings.Contains(text, ":large_green_circle:") {
		t.Error("expected green circle emoji in working block")
	}
	if !strings.Contains(text, "hq") {
		t.Error("expected agent name in working block")
	}
	if !strings.Contains(text, "gasboat") {
		t.Error("expected project in working block")
	}
	if !strings.Contains(text, "crew/lead") {
		t.Error("expected mode/role in working block")
	}
}

func TestDashboardAgentWorkingBlock_WithCoopmuxURL(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "hq",
		Project:   "gasboat",
		Metadata:  map[string]string{"pod_name": "agent-hq-abc"},
	}
	block := dashboardAgentWorkingBlock(a, "https://coopmux.example.com")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, "https://coopmux.example.com#agent-hq-abc") {
		t.Errorf("expected coopmux link in working block, got %s", text)
	}
}

func TestDashboardAgentWorkingBlock_NoProjectNoRole(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "hq",
		Metadata:  map[string]string{},
	}
	block := dashboardAgentWorkingBlock(a, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if strings.Contains(text, "·") {
		t.Errorf("expected no separator when no project/role, got %s", text)
	}
}

func TestDashboardAgentStartingBlock_Basic(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "new-bot",
		Project:   "gasboat",
	}
	block := dashboardAgentStartingBlock(a)
	section, ok := block.(*slack.SectionBlock)
	if !ok {
		t.Fatalf("expected *slack.SectionBlock, got %T", block)
	}
	text := section.Text.Text
	if !strings.Contains(text, ":hourglass_flowing_sand:") {
		t.Error("expected hourglass emoji in starting block")
	}
	if !strings.Contains(text, "new-bot") {
		t.Error("expected agent name in starting block")
	}
	if !strings.Contains(text, "gasboat") {
		t.Error("expected project in starting block")
	}
}

func TestDashboardAgentStartingBlock_NoProject(t *testing.T) {
	a := beadsapi.AgentBead{AgentName: "new-bot"}
	block := dashboardAgentStartingBlock(a)
	section := block.(*slack.SectionBlock)
	if strings.Contains(section.Text.Text, "·") {
		t.Error("expected no separator when no project")
	}
}

func TestDashboardAgentIdleBlock_NoPending(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "idle-bot",
		Project:   "gasboat",
		Metadata:  map[string]string{},
	}
	block := dashboardAgentIdleBlock(a, 0, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, ":white_circle:") {
		t.Error("expected white circle for idle agent with no pending decisions")
	}
	if strings.Contains(text, "pending") {
		t.Error("expected no 'pending' text when pendingCount=0")
	}
}

func TestDashboardAgentIdleBlock_WithPending(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "idle-bot",
		Project:   "gasboat",
		Metadata:  map[string]string{},
	}
	block := dashboardAgentIdleBlock(a, 3, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, ":large_blue_circle:") {
		t.Error("expected blue circle for idle agent with pending decisions")
	}
	if !strings.Contains(text, "3 pending") {
		t.Error("expected '3 pending' in idle block with pending decisions")
	}
}

func TestDashboardAgentIdleBlock_WithCoopmuxURL(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName: "idle-bot",
		Metadata:  map[string]string{"pod_name": "pod-idle"},
	}
	block := dashboardAgentIdleBlock(a, 0, "https://coopmux.example.com")
	section := block.(*slack.SectionBlock)
	if !strings.Contains(section.Text.Text, "https://coopmux.example.com#pod-idle") {
		t.Error("expected coopmux link in idle block")
	}
}

func TestDashboardAgentDeadBlock_Done(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName:  "done-bot",
		AgentState: "done",
		Project:    "gasboat",
		Metadata:   map[string]string{},
	}
	block := dashboardAgentDeadBlock(a, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, ":white_check_mark:") {
		t.Error("expected checkmark emoji for done agent")
	}
	if !strings.Contains(text, "done") {
		t.Error("expected 'done' state text")
	}
}

func TestDashboardAgentDeadBlock_RateLimited(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName:  "limited-bot",
		AgentState: "rate_limited",
		Metadata:   map[string]string{},
	}
	block := dashboardAgentDeadBlock(a, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, ":warning:") {
		t.Error("expected warning emoji for rate_limited agent")
	}
}

func TestDashboardAgentDeadBlock_Failed(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName:  "dead-bot",
		AgentState: "failed",
		Project:    "gasboat",
		Metadata:   map[string]string{},
	}
	block := dashboardAgentDeadBlock(a, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, ":red_circle:") {
		t.Error("expected red circle emoji for failed agent")
	}
}

func TestDashboardAgentDeadBlock_EmptyState_FallsToPodPhase(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName:  "dead-bot",
		AgentState: "",
		PodPhase:   "failed",
		Metadata:   map[string]string{},
	}
	block := dashboardAgentDeadBlock(a, "")
	section := block.(*slack.SectionBlock)
	text := section.Text.Text
	if !strings.Contains(text, "failed") {
		t.Error("expected pod phase 'failed' as fallback state text")
	}
}

func TestDashboardAgentDeadBlock_WithCoopmuxURL(t *testing.T) {
	a := beadsapi.AgentBead{
		AgentName:  "dead-bot",
		AgentState: "done",
		Metadata:   map[string]string{"pod_name": "pod-dead"},
	}
	block := dashboardAgentDeadBlock(a, "https://coopmux.example.com")
	section := block.(*slack.SectionBlock)
	if !strings.Contains(section.Text.Text, "https://coopmux.example.com#pod-dead") {
		t.Error("expected coopmux link in dead block")
	}
}

// --- renderBlocks tests ---

func TestRenderBlocks_EmptyAgents(t *testing.T) {
	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, hash := d.renderBlocks(nil, nil)

	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks (header + summary), got %d", len(blocks))
	}
	if hash != "" {
		t.Errorf("expected empty hash for no agents, got %q", hash)
	}

	section, ok := blocks[0].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("expected header to be *slack.SectionBlock, got %T", blocks[0])
	}
	if !strings.Contains(section.Text.Text, dashboardMarker) {
		t.Error("expected dashboard marker in header block")
	}
}

func TestRenderBlocks_MixedAgents(t *testing.T) {
	agents := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "worker-1", AgentState: "working", Project: "gasboat", Metadata: map[string]string{}},
		{ID: "bd-2", AgentName: "idle-1", AgentState: "", Project: "gasboat", Metadata: map[string]string{}},
		{ID: "bd-3", AgentName: "dead-1", AgentState: "done", Project: "gasboat", Metadata: map[string]string{}},
		{ID: "bd-4", AgentName: "spawn-1", AgentState: "spawning", Project: "gasboat", Metadata: map[string]string{}},
	}

	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, hash := d.renderBlocks(agents, nil)

	if len(blocks) < 4 {
		t.Fatalf("expected many blocks for mixed agents, got %d", len(blocks))
	}
	if hash == "" {
		t.Error("expected non-empty hash for agents")
	}

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "Working (1)") {
		t.Error("expected Working section header")
	}
	if !strings.Contains(allText, "Idle (1)") {
		t.Error("expected Idle section header")
	}
	if !strings.Contains(allText, "Stopped (1)") {
		t.Error("expected Stopped section header")
	}
	if !strings.Contains(allText, "Starting (1)") {
		t.Error("expected Starting section header")
	}
}

func TestRenderBlocks_WithDecisions(t *testing.T) {
	agents := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "worker-1", AgentState: "working", Project: "gasboat", Metadata: map[string]string{}},
	}
	decisions := []*beadsapi.BeadDetail{
		{
			ID:       "dec-1",
			Title:    "Approve deployment",
			Fields:   map[string]string{"requesting_agent_bead_id": "bd-1"},
			Assignee: "worker-1",
		},
	}

	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, _ := d.renderBlocks(agents, decisions)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "Pending Decisions (1)") {
		t.Error("expected Pending Decisions section")
	}
	if !strings.Contains(allText, "dec-1") {
		t.Error("expected decision ID in output")
	}
}

func TestRenderBlocks_DecisionWithEscalatedLabel(t *testing.T) {
	decisions := []*beadsapi.BeadDetail{
		{
			ID:     "dec-esc",
			Title:  "Urgent approval",
			Fields: map[string]string{},
			Labels: []string{"escalated"},
		},
	}

	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, _ := d.renderBlocks(nil, decisions)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, ":rotating_light:") {
		t.Error("expected rotating_light emoji for escalated decision")
	}
}

func TestRenderBlocks_DecisionQuestionTruncation(t *testing.T) {
	longQuestion := strings.Repeat("x", 100)
	decisions := []*beadsapi.BeadDetail{
		{
			ID:     "dec-long",
			Title:  "fallback title",
			Fields: map[string]string{"question": longQuestion},
		},
	}

	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, _ := d.renderBlocks(nil, decisions)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "...") {
		t.Error("expected truncation ellipsis for long question")
	}
}

func TestRenderBlocks_DecisionFallsBackToTitle(t *testing.T) {
	decisions := []*beadsapi.BeadDetail{
		{
			ID:     "dec-notitle",
			Title:  "My title",
			Fields: map[string]string{},
		},
	}

	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, _ := d.renderBlocks(nil, decisions)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "My title") {
		t.Error("expected decision title as fallback when no question field")
	}
}

func TestRenderBlocks_OverflowMessages(t *testing.T) {
	var agents []beadsapi.AgentBead
	for i := 0; i < 15; i++ {
		agents = append(agents, beadsapi.AgentBead{
			ID:         "bd-" + string(rune('a'+i)),
			AgentName:  "worker-" + string(rune('a'+i)),
			AgentState: "working",
			Project:    "gasboat",
			Metadata:   map[string]string{},
		})
	}

	d := &Dashboard{cfg: DashboardConfig{MaxWorkingShown: 3}}
	blocks, _ := d.renderBlocks(agents, nil)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "+12 more working agents") {
		t.Errorf("expected overflow message for working agents, got: %s", allText)
	}
}

func TestRenderBlocks_IdleOverflow(t *testing.T) {
	var agents []beadsapi.AgentBead
	for i := 0; i < 10; i++ {
		agents = append(agents, beadsapi.AgentBead{
			ID:        "bd-idle-" + string(rune('a'+i)),
			AgentName: "idle-" + string(rune('a'+i)),
			Metadata:  map[string]string{},
		})
	}

	d := &Dashboard{cfg: DashboardConfig{MaxIdleShown: 2}}
	blocks, _ := d.renderBlocks(agents, nil)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "+8 more idle agents") {
		t.Errorf("expected idle overflow message, got: %s", allText)
	}
}

func TestRenderBlocks_DeadOverflow(t *testing.T) {
	var agents []beadsapi.AgentBead
	for i := 0; i < 8; i++ {
		agents = append(agents, beadsapi.AgentBead{
			ID:         "bd-dead-" + string(rune('a'+i)),
			AgentName:  "dead-" + string(rune('a'+i)),
			AgentState: "done",
			Metadata:   map[string]string{},
		})
	}

	d := &Dashboard{cfg: DashboardConfig{MaxDeadShown: 2}}
	blocks, _ := d.renderBlocks(agents, nil)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "+6 more dead agents") {
		t.Errorf("expected dead overflow message, got: %s", allText)
	}
}

func TestRenderBlocks_DecisionsOverflow(t *testing.T) {
	var decisions []*beadsapi.BeadDetail
	for i := 0; i < 8; i++ {
		decisions = append(decisions, &beadsapi.BeadDetail{
			ID:     "dec-" + string(rune('a'+i)),
			Title:  "Decision " + string(rune('a'+i)),
			Fields: map[string]string{},
		})
	}

	d := &Dashboard{cfg: DashboardConfig{MaxDecisionsShown: 3}}
	blocks, _ := d.renderBlocks(nil, decisions)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "+5 more pending decisions") {
		t.Errorf("expected decisions overflow message, got: %s", allText)
	}
}

// --- buildBlocks tests (requires real beadsapi.Client via httptest) ---

func TestBuildBlocks_Success(t *testing.T) {
	// Mock beads daemon that returns agent and decision listings.
	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"beads": []map[string]any{
				{
					"id":    "bd-agent-1",
					"title": "hq",
					"type":  "agent",
					"fields": map[string]any{
						"agent":       "hq",
						"project":     "gasboat",
						"role":        "crew",
						"agent_state": "working",
					},
				},
			},
			"total": 1,
		})
	}))
	defer daemonSrv.Close()

	daemon, err := beadsapi.New(beadsapi.Config{HTTPAddr: daemonSrv.URL})
	if err != nil {
		t.Fatal(err)
	}

	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	api := slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))
	d := &Dashboard{
		api:    api,
		daemon: daemon,
		cfg:    DashboardConfig{ChannelID: "C123"},
	}

	blocks, hash, buildErr := d.buildBlocks(t.Context())
	if buildErr != nil {
		t.Fatalf("buildBlocks returned error: %v", buildErr)
	}
	if len(blocks) == 0 {
		t.Error("expected non-empty blocks from buildBlocks")
	}
	if hash == "" {
		t.Error("expected non-empty hash from buildBlocks")
	}
}

// --- Hash tests ---

func TestBuildDashboardHash_WithDecisions(t *testing.T) {
	agents := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "", Project: "gasboat"},
	}
	decisions := []*beadsapi.BeadDetail{
		{ID: "dec-1", Fields: map[string]string{"requesting_agent_bead_id": "bd-1"}},
	}

	hashWithDec := buildDashboardHash(agents, decisions)
	hashWithoutDec := buildDashboardHash(agents, nil)

	if hashWithDec == hashWithoutDec {
		t.Error("hash with decisions should differ from hash without")
	}
	if !strings.Contains(hashWithDec, "decision:1") {
		t.Errorf("expected 'decision:1' in hash, got %q", hashWithDec)
	}
}

func TestBuildDashboardHash_SpawningState(t *testing.T) {
	agents := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "spawning", Project: "gasboat"},
	}

	hash := buildDashboardHash(agents, nil)
	if !strings.Contains(hash, "starting") {
		t.Errorf("expected 'starting' in hash for spawning agent, got %q", hash)
	}
}

func TestBuildDashboardHash_PodPhaseFailed(t *testing.T) {
	agents := []beadsapi.AgentBead{
		{ID: "bd-1", AgentName: "bot-1", AgentState: "", PodPhase: "failed", Project: "gasboat"},
	}

	hash := buildDashboardHash(agents, nil)
	if !strings.Contains(hash, "dead") {
		t.Errorf("expected 'dead' in hash for pod phase failed, got %q", hash)
	}
}

// --- MarkDirty test ---

func TestDashboard_MarkDirty(t *testing.T) {
	d := &Dashboard{}
	d.MarkDirty()

	d.mu.Lock()
	dirty := d.dirty
	d.mu.Unlock()

	if !dirty {
		t.Error("expected dirty=true after MarkDirty()")
	}
}

// --- NewDashboard tests ---

func TestNewDashboard_NoChannel_DisablesItself(t *testing.T) {
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	api := slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))
	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"beads": []any{}, "total": 0})
	}))
	defer daemonSrv.Close()
	daemon, _ := beadsapi.New(beadsapi.Config{HTTPAddr: daemonSrv.URL})

	d := NewDashboard(api, daemon, nil, slog.Default(), DashboardConfig{
		Enabled:   true,
		ChannelID: "",
	})
	if d.cfg.Enabled {
		t.Error("expected dashboard to be disabled when no channel configured")
	}
}

func TestNewDashboard_WithChannel_StaysEnabled(t *testing.T) {
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	api := slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))
	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"beads": []any{}, "total": 0})
	}))
	defer daemonSrv.Close()
	daemon, _ := beadsapi.New(beadsapi.Config{HTTPAddr: daemonSrv.URL})

	d := NewDashboard(api, daemon, nil, slog.Default(), DashboardConfig{
		Enabled:   true,
		ChannelID: "C123",
	})
	if !d.cfg.Enabled {
		t.Error("expected dashboard to stay enabled when channel configured")
	}
}

// --- Classification sorting tests ---

func TestRenderBlocks_AgentClassification(t *testing.T) {
	agents := []beadsapi.AgentBead{
		{ID: "1", AgentName: "a-failed", AgentState: "failed", Metadata: map[string]string{}},
		{ID: "2", AgentName: "b-pod-failed", PodPhase: "failed", Metadata: map[string]string{}},
		{ID: "3", AgentName: "c-rate-limited", AgentState: "rate_limited", Metadata: map[string]string{}},
	}

	d := &Dashboard{cfg: DashboardConfig{}}
	blocks, _ := d.renderBlocks(agents, nil)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "Stopped (3)") {
		t.Errorf("expected 3 stopped agents, got: %s", allText)
	}
}

// --- StartingOverflow test ---

func TestRenderBlocks_StartingOverflow(t *testing.T) {
	var agents []beadsapi.AgentBead
	for i := 0; i < 8; i++ {
		agents = append(agents, beadsapi.AgentBead{
			ID:         "bd-start-" + string(rune('a'+i)),
			AgentName:  "start-" + string(rune('a'+i)),
			AgentState: "spawning",
			Metadata:   map[string]string{},
		})
	}

	d := &Dashboard{cfg: DashboardConfig{MaxIdleShown: 2}} // starting uses MaxIdleShown
	blocks, _ := d.renderBlocks(agents, nil)

	allText := blocksToText(blocks)
	if !strings.Contains(allText, "+6 more starting agents") {
		t.Errorf("expected starting overflow message, got: %s", allText)
	}
}

// --- helpers ---

func blocksToText(blocks []slack.Block) string {
	var parts []string
	for _, b := range blocks {
		switch v := b.(type) {
		case *slack.SectionBlock:
			if v.Text != nil {
				parts = append(parts, v.Text.Text)
			}
		case *slack.ContextBlock:
			for _, el := range v.ContextElements.Elements {
				if txt, ok := el.(*slack.TextBlockObject); ok {
					parts = append(parts, txt.Text)
				}
			}
		}
	}
	return strings.Join(parts, " | ")
}
