package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/podmanager"
)

// --- Drift detection tests ---

func TestReconcile_DriftDetection_RecreatesOnImageChange(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	// Pod is running with old image; desired spec has new image.
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
		},
	}

	// The specBuilder returns img:v2, but the pod has img:v1 (from makePod).
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v2"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should delete the drifted pod and create a replacement.
	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion for drift, got %d", len(mgr.deleted))
	}
	if mgr.deleted[0] != "crew-proj-dev-alpha" {
		t.Errorf("expected crew-proj-dev-alpha deleted for drift, got %s", mgr.deleted[0])
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation after drift delete, got %d", len(mgr.created))
	}
	if mgr.created[0].Image != "img:v2" {
		t.Errorf("expected new pod with img:v2, got %s", mgr.created[0].Image)
	}
}

func TestReconcile_DriftDetection_SkipsDriftForJobMode(t *testing.T) {
	// Jobs use UpgradeSkip — don't kill running jobs for drift.
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "job", Role: "ops", AgentName: "task1"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("job-proj-ops-task1", "ns", "job", "proj", "ops", "task1", corev1.PodRunning),
		},
	}

	// Desired image differs, but jobs should not be restarted.
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v2"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions for job drift (UpgradeSkip), got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 0 {
		t.Errorf("expected 0 creations for job drift, got %d", len(mgr.created))
	}
}

func TestReconcile_NoDrift_WhenImageMatches(t *testing.T) {
	image := "ghcr.io/org/agent:v1"
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder(image))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions when image matches, got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 0 {
		t.Errorf("expected 0 creations when image matches, got %d", len(mgr.created))
	}
}

// --- Digest drift / rolling upgrade tests ---

func TestReconcile_DigestDrift_TriggersRecreate(t *testing.T) {
	image := "ghcr.io/org/agent:latest"
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}

	pod := makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning)
	pod.Spec.Containers[0].Image = image // same tag — no tag drift

	mgr := &mockManager{pods: []corev1.Pod{pod}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder(image))

	// Simulate: tag was deployed at digest A, registry now has digest B.
	r.digestTracker.Seed(image, "sha256:aaa111")
	// Confirm digest B twice (meets threshold).
	r.digestTracker.RecordRegistryDigest(image, "sha256:bbb222")
	r.digestTracker.RecordRegistryDigest(image, "sha256:bbb222")

	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion for digest drift, got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation after digest drift, got %d", len(mgr.created))
	}
}

func TestReconcile_DigestDrift_NoFalsePositiveOnFirstSeen(t *testing.T) {
	image := "ghcr.io/org/agent:latest"
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}

	pod := makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning)
	pod.Spec.Containers[0].Image = image

	mgr := &mockManager{pods: []corev1.Pod{pod}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder(image))

	// Seed with a digest — deployed and current are the same. No drift.
	r.digestTracker.Seed(image, "sha256:aaa111")

	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions (no drift on first seen), got %d", len(mgr.deleted))
	}
}

func TestReconcile_DigestDrift_ClearedAfterUpgrade(t *testing.T) {
	image := "ghcr.io/org/agent:latest"
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}

	pod := makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning)
	pod.Spec.Containers[0].Image = image

	mgr := &mockManager{pods: []corev1.Pod{pod}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder(image))

	// Set up drift: deployed=A, current=B.
	r.digestTracker.Seed(image, "sha256:aaa111")
	r.digestTracker.RecordRegistryDigest(image, "sha256:bbb222")
	r.digestTracker.RecordRegistryDigest(image, "sha256:bbb222")

	// First reconcile should delete+create (handling drift).
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation on first reconcile, got %d", len(mgr.created))
	}

	// After upgrade, MarkDeployed should have been called.
	// Verify no more drift.
	if r.digestTracker.HasDrift(image) {
		t.Error("expected no drift after pod recreation (MarkDeployed should clear it)")
	}
}

func TestReconcile_RollingUpgrade_OnlyOnePerMode(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
			{ID: "bd-2", Project: "proj", Mode: "crew", Role: "dev", AgentName: "beta"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-beta", "ns", "crew", "proj", "dev", "beta", corev1.PodRunning),
		},
	}

	// Both pods have old image, both need drift update.
	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v2"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Rolling upgrade: only one crew pod should be upgraded per pass.
	if len(mgr.deleted) != 1 {
		t.Fatalf("expected 1 deletion for rolling upgrade, got %d", len(mgr.deleted))
	}
	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 creation for rolling upgrade, got %d", len(mgr.created))
	}
}

// --- Error handling tests ---

func TestReconcile_DaemonUnreachable_ReturnsError(t *testing.T) {
	lister := &mockLister{err: errors.New("connection refused")}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when daemon is unreachable")
	}
	if mgr.deleted != nil {
		t.Errorf("should not delete any pods when daemon is unreachable")
	}
	if mgr.created != nil {
		t.Errorf("should not create any pods when daemon is unreachable")
	}
}

func TestReconcile_PodListError_ReturnsError(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{listErr: errors.New("k8s API down")}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when pod listing fails")
	}
}

func TestReconcile_PodCreateError_ContinuesGracefully(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods:      nil,
		createErr: errors.New("quota exceeded"),
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("expected graceful continue on pod create error, got: %v", err)
	}
}

func TestReconcile_PodDeleteError_ReturnsError(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-alpha", "ns", "crew", "proj", "dev", "alpha", corev1.PodRunning),
			makePod("crew-proj-dev-orphan", "ns", "crew", "proj", "dev", "orphan", corev1.PodRunning),
		},
		deleteErr: errors.New("forbidden"),
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error when pod deletion fails")
	}
}

// --- Edge case tests ---

func TestReconcile_EmptyState_NoBeadsNoPods(t *testing.T) {
	lister := &mockLister{beads: []beadsapi.AgentBead{}}
	mgr := &mockManager{pods: []corev1.Pod{}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mgr.created) != 0 || len(mgr.deleted) != 0 {
		t.Errorf("expected no-op for empty state, got %d created, %d deleted", len(mgr.created), len(mgr.deleted))
	}
}

func TestReconcile_SingleBeadSinglePod(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "solo"},
		},
	}
	mgr := &mockManager{
		pods: []corev1.Pod{
			makePod("crew-proj-dev-solo", "ns", "crew", "proj", "dev", "solo", corev1.PodRunning),
		},
	}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("ghcr.io/org/agent:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mgr.created) != 0 || len(mgr.deleted) != 0 {
		t.Errorf("expected no-op for converged single-pod state")
	}
}

func TestReconcile_IgnoresPodsWithoutAgentLabel(t *testing.T) {
	// Pods without the gasboat.io/agent label should be ignored (e.g., controller pod).
	lister := &mockLister{beads: nil}
	infraPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gasboat-controller-xyz",
			Namespace: "ns",
			Labels: map[string]string{
				podmanager.LabelApp: podmanager.LabelAppValue,
				// No LabelAgent — this is an infrastructure pod.
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	mgr := &mockManager{pods: []corev1.Pod{infraPod}}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Infrastructure pod should NOT be deleted despite not being in desired set.
	if len(mgr.deleted) != 0 {
		t.Errorf("expected 0 deletions (infra pod should be ignored), got %d", len(mgr.deleted))
	}
}

func TestReconcile_BeadIDPassedToSpec(t *testing.T) {
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-custom-id", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))
	err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mgr.created) != 1 {
		t.Fatalf("expected 1 pod created, got %d", len(mgr.created))
	}
	if mgr.created[0].BeadID != "bd-custom-id" {
		t.Errorf("expected BeadID bd-custom-id, got %s", mgr.created[0].BeadID)
	}
}

func TestReconcile_ConcurrencySafe(t *testing.T) {
	// Verify that concurrent Reconcile calls don't panic (mutex protection).
	lister := &mockLister{
		beads: []beadsapi.AgentBead{
			{ID: "bd-1", Project: "proj", Mode: "crew", Role: "dev", AgentName: "alpha"},
		},
	}
	mgr := &mockManager{pods: nil}

	r := New(lister, mgr, testConfig("ns"), testLogger(), simpleSpecBuilder("img:v1"))

	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			done <- r.Reconcile(context.Background())
		}()
	}

	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("concurrent reconcile returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent reconciles")
		}
	}
}

// --- podDriftReason unit tests ---

func TestPodDriftReason_NoChange(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: "img:v1"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "img:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason != "" {
		t.Errorf("expected no drift, got: %s", reason)
	}
}

func TestPodDriftReason_ImageTagChanged(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: "img:v2"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "img:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason == "" {
		t.Error("expected drift reason for image tag change")
	}
}

func TestPodDriftReason_EmptyDesiredImage(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: ""}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "img:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason != "" {
		t.Errorf("expected no drift when desired image is empty, got: %s", reason)
	}
}

func TestPodDriftReason_NoAgentContainer(t *testing.T) {
	spec := podmanager.AgentPodSpec{Image: "img:v2"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sidecar", Image: "other:v1"}},
		},
	}
	reason := podDriftReason(spec, pod, nil)
	if reason != "" {
		t.Errorf("expected no drift when no agent container, got: %s", reason)
	}
}
