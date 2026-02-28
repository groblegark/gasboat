package statusreporter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"gasboat/controller/internal/podmanager"
)

// ── PhaseToAgentState ──────────────────────────────────────────────────────

func TestPhaseToAgentState(t *testing.T) {
	tests := []struct {
		phase string
		want  string
	}{
		{"Pending", "spawning"},
		{"Running", "working"},
		{"Succeeded", "done"},
		{"Failed", "failed"},
		{"Stopped", "done"},
		{"Unknown", ""},
		{"", ""},
		{"garbage", ""},
	}
	for _, tt := range tests {
		if got := PhaseToAgentState(tt.phase); got != tt.want {
			t.Errorf("PhaseToAgentState(%q) = %q, want %q", tt.phase, got, tt.want)
		}
	}
}

// ── agentBeadID ────────────────────────────────────────────────────────────

func TestAgentBeadID_FromAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				podmanager.AnnotationBeadID: "custom-bead-id",
			},
		},
	}
	if got := agentBeadID(pod); got != "custom-bead-id" {
		t.Errorf("agentBeadID = %q, want custom-bead-id", got)
	}
}

func TestAgentBeadID_FromLabels(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				podmanager.LabelProject: "gasboat",
				podmanager.LabelRole:    "crew",
				podmanager.LabelAgent:   "test-1",
				podmanager.LabelMode:    "crew",
			},
		},
	}
	want := "crew-gasboat-crew-test-1"
	if got := agentBeadID(pod); got != want {
		t.Errorf("agentBeadID = %q, want %q", got, want)
	}
}

func TestAgentBeadID_DefaultMode(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				podmanager.LabelProject: "acme",
				podmanager.LabelRole:    "dev",
				podmanager.LabelAgent:   "bot-7",
			},
		},
	}
	want := "crew-acme-dev-bot-7"
	if got := agentBeadID(pod); got != want {
		t.Errorf("agentBeadID = %q, want %q (default mode should be crew)", got, want)
	}
}

// ── detectCoopPort ─────────────────────────────────────────────────────────

func TestDetectCoopPort_AgentContainer(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "agent",
					Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
				},
			},
		},
	}
	if got := detectCoopPort(pod); got != 8080 {
		t.Errorf("detectCoopPort = %d, want 8080", got)
	}
}

func TestDetectCoopPort_CoopSidecar(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "agent"},
				{
					Name:  "coop",
					Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
				},
			},
		},
	}
	if got := detectCoopPort(pod); got != 8080 {
		t.Errorf("detectCoopPort = %d, want 8080", got)
	}
}

func TestDetectCoopPort_CoopSidecarDefaultPort(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "coop"}, // no ports declared
			},
		},
	}
	if got := detectCoopPort(pod); got != 8080 {
		t.Errorf("detectCoopPort = %d, want 8080 (coop default)", got)
	}
}

func TestDetectCoopPort_NoCoop(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "agent",
					Ports: []corev1.ContainerPort{{ContainerPort: 3000}},
				},
			},
		},
	}
	if got := detectCoopPort(pod); got != 0 {
		t.Errorf("detectCoopPort = %d, want 0", got)
	}
}

func TestDetectCoopPort_NoPods(t *testing.T) {
	pod := &corev1.Pod{}
	if got := detectCoopPort(pod); got != 0 {
		t.Errorf("detectCoopPort = %d, want 0", got)
	}
}

// ── mockBeadUpdater ────────────────────────────────────────────────────────

type mockBeadUpdater struct {
	states map[string]string // beadID → last state
	notes  map[string]string // beadID → last notes
	err    error             // if set, all calls return this
}

func newMockUpdater() *mockBeadUpdater {
	return &mockBeadUpdater{
		states: make(map[string]string),
		notes:  make(map[string]string),
	}
}

func (m *mockBeadUpdater) UpdateAgentState(_ context.Context, beadID, state string) error {
	if m.err != nil {
		return m.err
	}
	m.states[beadID] = state
	return nil
}

func (m *mockBeadUpdater) UpdateBeadNotes(_ context.Context, beadID, notes string) error {
	if m.err != nil {
		return m.err
	}
	m.notes[beadID] = notes
	return nil
}

// ── ReportPodStatus ────────────────────────────────────────────────────────

func TestReportPodStatus_Running(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportPodStatus(context.Background(), "crew-gasboat-crew-test-1", PodStatus{
		PodName: "crew-gasboat-crew-test-1",
		Phase:   "Running",
		Ready:   true,
	})
	if err != nil {
		t.Fatalf("ReportPodStatus: %v", err)
	}
	if updater.states["crew-gasboat-crew-test-1"] != "working" {
		t.Errorf("state = %q, want working", updater.states["crew-gasboat-crew-test-1"])
	}
}

func TestReportPodStatus_Failed(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
		Phase: "Failed",
	})
	if err != nil {
		t.Fatalf("ReportPodStatus: %v", err)
	}
	if updater.states["agent-1"] != "failed" {
		t.Errorf("state = %q, want failed", updater.states["agent-1"])
	}
}

func TestReportPodStatus_UnknownPhase(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
		Phase: "Unknown",
	})
	if err != nil {
		t.Fatalf("ReportPodStatus: %v", err)
	}
	// Unknown phase should skip the update.
	if _, ok := updater.states["agent-1"]; ok {
		t.Error("should not update state for Unknown phase")
	}
}

func TestReportPodStatus_DaemonError(t *testing.T) {
	updater := newMockUpdater()
	updater.err = fmt.Errorf("connection refused")
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportPodStatus(context.Background(), "agent-1", PodStatus{
		Phase: "Running",
	})
	if err == nil {
		t.Fatal("expected error from daemon")
	}

	// Metrics should reflect the error.
	m := r.Metrics()
	if m.StatusReportErrors != 1 {
		t.Errorf("report errors = %d, want 1", m.StatusReportErrors)
	}
}

func TestReportPodStatus_Metrics(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	_ = r.ReportPodStatus(context.Background(), "a", PodStatus{Phase: "Running"})
	_ = r.ReportPodStatus(context.Background(), "b", PodStatus{Phase: "Pending"})
	_ = r.ReportPodStatus(context.Background(), "c", PodStatus{Phase: "Unknown"})

	m := r.Metrics()
	if m.StatusReportsTotal != 3 {
		t.Errorf("reports total = %d, want 3", m.StatusReportsTotal)
	}
}

// ── ReportBackendMetadata ──────────────────────────────────────────────────

func TestReportBackendMetadata_Full(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{
		PodName:   "crew-gasboat-crew-test-1",
		Namespace: "gasboat",
		Backend:   "coop",
		CoopURL:   "http://10.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("ReportBackendMetadata: %v", err)
	}

	notes := updater.notes["agent-1"]
	for _, want := range []string{"backend: coop", "pod_name: crew-gasboat-crew-test-1", "pod_namespace: gasboat", "coop_url: http://10.0.0.1:8080"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q, got:\n%s", want, notes)
		}
	}
}

func TestReportBackendMetadata_Empty(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{})
	if err != nil {
		t.Fatalf("ReportBackendMetadata: %v", err)
	}

	// Empty metadata should not call UpdateBeadNotes.
	if _, ok := updater.notes["agent-1"]; ok {
		t.Error("should not write notes for empty metadata")
	}
}

func TestReportBackendMetadata_WithToken(t *testing.T) {
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{
		Backend:   "coop",
		CoopURL:   "http://10.0.0.1:8080",
		CoopToken: "secret-token",
	})
	if err != nil {
		t.Fatalf("ReportBackendMetadata: %v", err)
	}

	notes := updater.notes["agent-1"]
	if !strings.Contains(notes, "coop_token: secret-token") {
		t.Errorf("notes missing coop_token, got:\n%s", notes)
	}
}

func TestReportBackendMetadata_DaemonError(t *testing.T) {
	updater := newMockUpdater()
	updater.err = fmt.Errorf("daemon down")
	r := NewHTTPReporter(updater, fake.NewClientset(), "default", slog.Default())

	err := r.ReportBackendMetadata(context.Background(), "agent-1", BackendMetadata{
		Backend: "coop",
	})
	if err == nil {
		t.Fatal("expected error from daemon")
	}
}

// ── SyncAll ────────────────────────────────────────────────────────────────

func createTestPod(client *fake.Clientset, name, ns string, labels map[string]string, annotations map[string]string, phase corev1.PodPhase, podIP string, containers []corev1.Container) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			Containers: containers,
		},
		Status: corev1.PodStatus{
			Phase: phase,
			PodIP: podIP,
		},
	}
	_, _ = client.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{})
}

func TestSyncAll_ReportsAllPods(t *testing.T) {
	client := fake.NewClientset()
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, client, "gasboat", slog.Default())

	createTestPod(client, "crew-gasboat-dev-test-1", "gasboat",
		map[string]string{
			podmanager.LabelApp:     podmanager.LabelAppValue,
			podmanager.LabelProject: "gasboat",
			podmanager.LabelRole:    "dev",
			podmanager.LabelAgent:   "test-1",
			podmanager.LabelMode:    "crew",
		},
		map[string]string{podmanager.AnnotationBeadID: "bead-123"},
		corev1.PodRunning,
		"10.0.0.1",
		[]corev1.Container{{Name: "agent", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}},
	)

	createTestPod(client, "crew-gasboat-dev-test-2", "gasboat",
		map[string]string{
			podmanager.LabelApp:     podmanager.LabelAppValue,
			podmanager.LabelProject: "gasboat",
			podmanager.LabelRole:    "dev",
			podmanager.LabelAgent:   "test-2",
			podmanager.LabelMode:    "crew",
		},
		nil,
		corev1.PodPending,
		"",
		[]corev1.Container{{Name: "agent"}},
	)

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("SyncAll: %v", err)
	}

	// First pod: Running → working.
	if updater.states["bead-123"] != "working" {
		t.Errorf("bead-123 state = %q, want working", updater.states["bead-123"])
	}

	// First pod: has coop port + IP → backend metadata written.
	notes := updater.notes["bead-123"]
	if !strings.Contains(notes, "coop_url: http://10.0.0.1:8080") {
		t.Errorf("bead-123 missing coop_url in notes: %q", notes)
	}

	// Second pod: Pending → spawning.
	secondID := "crew-gasboat-dev-test-2"
	if updater.states[secondID] != "spawning" {
		t.Errorf("%s state = %q, want spawning", secondID, updater.states[secondID])
	}

	// Second pod: no coop port → no backend metadata.
	if _, ok := updater.notes[secondID]; ok {
		t.Error("second pod should not have backend metadata (no coop port)")
	}
}

func TestSyncAll_SkipPodsWithoutLabels(t *testing.T) {
	client := fake.NewClientset()
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, client, "gasboat", slog.Default())

	// Pod missing agent label.
	createTestPod(client, "controller", "gasboat",
		map[string]string{
			podmanager.LabelApp:     podmanager.LabelAppValue,
			podmanager.LabelProject: "gasboat",
		},
		nil,
		corev1.PodRunning,
		"10.0.0.2",
		nil,
	)

	err := r.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("SyncAll: %v", err)
	}

	if len(updater.states) != 0 {
		t.Error("should not report status for pod without agent/role labels")
	}
}

func TestSyncAll_NoCoopMetadataWithoutIP(t *testing.T) {
	client := fake.NewClientset()
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, client, "gasboat", slog.Default())

	createTestPod(client, "crew-gasboat-dev-test-1", "gasboat",
		map[string]string{
			podmanager.LabelApp:     podmanager.LabelAppValue,
			podmanager.LabelProject: "gasboat",
			podmanager.LabelRole:    "dev",
			podmanager.LabelAgent:   "test-1",
			podmanager.LabelMode:    "crew",
		},
		nil,
		corev1.PodPending,
		"", // no IP yet
		[]corev1.Container{{Name: "agent", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}},
	)

	_ = r.SyncAll(context.Background())

	// State should be reported (spawning) but no backend metadata without IP.
	beadID := "crew-gasboat-dev-test-1"
	if updater.states[beadID] != "spawning" {
		t.Errorf("state = %q, want spawning", updater.states[beadID])
	}
	if _, ok := updater.notes[beadID]; ok {
		t.Error("should not write backend metadata without pod IP")
	}
}

func TestSyncAll_Metrics(t *testing.T) {
	client := fake.NewClientset()
	updater := newMockUpdater()
	r := NewHTTPReporter(updater, client, "gasboat", slog.Default())

	_ = r.SyncAll(context.Background())
	_ = r.SyncAll(context.Background())

	m := r.Metrics()
	if m.SyncAllRuns != 2 {
		t.Errorf("sync runs = %d, want 2", m.SyncAllRuns)
	}
}
