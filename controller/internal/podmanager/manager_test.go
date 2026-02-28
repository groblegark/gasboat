package podmanager

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newTestManager() (*K8sManager, *fake.Clientset) {
	client := fake.NewSimpleClientset()
	logger := slog.Default()
	return New(client, logger), client
}

func testSpec() AgentPodSpec {
	return AgentPodSpec{
		Project:   "gasboat",
		Mode:      "crew",
		Role:      "dev",
		AgentName: "test-agent",
		BeadID:    "kd-test123",
		Image:     "ghcr.io/test/agent:latest",
		Namespace: "default",
	}
}

func TestCreateAgentPod(t *testing.T) {
	m, client := newTestManager()
	ctx := context.Background()
	spec := testSpec()

	if err := m.CreateAgentPod(ctx, spec); err != nil {
		t.Fatalf("CreateAgentPod failed: %v", err)
	}

	pods, err := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods.Items))
	}
	if pods.Items[0].Name != spec.PodName() {
		t.Errorf("pod name = %q, want %q", pods.Items[0].Name, spec.PodName())
	}
}

func TestCreateAgentPod_Idempotent(t *testing.T) {
	m, _ := newTestManager()
	ctx := context.Background()
	spec := testSpec()

	if err := m.CreateAgentPod(ctx, spec); err != nil {
		t.Fatalf("first CreateAgentPod: %v", err)
	}

	// Second create should succeed (idempotent).
	if err := m.CreateAgentPod(ctx, spec); err != nil {
		t.Fatalf("second CreateAgentPod should be idempotent: %v", err)
	}
}

func TestDeleteAgentPod(t *testing.T) {
	m, client := newTestManager()
	ctx := context.Background()

	// Create a pod first.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}
	if _, err := client.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test pod: %v", err)
	}

	if err := m.DeleteAgentPod(ctx, "test-pod", "default"); err != nil {
		t.Fatalf("DeleteAgentPod failed: %v", err)
	}

	pods, _ := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Errorf("expected 0 pods after delete, got %d", len(pods.Items))
	}
}

func TestDeleteAgentPod_NotFound(t *testing.T) {
	m, _ := newTestManager()
	ctx := context.Background()

	// Deleting a non-existent pod should succeed (idempotent).
	if err := m.DeleteAgentPod(ctx, "nonexistent", "default"); err != nil {
		t.Fatalf("DeleteAgentPod for missing pod should be idempotent: %v", err)
	}
}

func TestListAgentPods(t *testing.T) {
	m, client := newTestManager()
	ctx := context.Background()

	// Create two pods with different labels.
	for _, name := range []string{"pod-a", "pod-b"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels: map[string]string{
					LabelApp:     LabelAppValue,
					LabelProject: "gasboat",
				},
			},
		}
		if _, err := client.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating pod %s: %v", name, err)
		}
	}

	pods, err := m.ListAgentPods(ctx, "default", map[string]string{LabelProject: "gasboat"})
	if err != nil {
		t.Fatalf("ListAgentPods: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("expected 2 pods, got %d", len(pods))
	}
}

func TestGetAgentPod(t *testing.T) {
	m, client := newTestManager()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "get-test",
			Namespace: "default",
		},
	}
	if _, err := client.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating pod: %v", err)
	}

	got, err := m.GetAgentPod(ctx, "get-test", "default")
	if err != nil {
		t.Fatalf("GetAgentPod: %v", err)
	}
	if got.Name != "get-test" {
		t.Errorf("got pod name %q, want %q", got.Name, "get-test")
	}
}

func TestPodName(t *testing.T) {
	spec := AgentPodSpec{
		Mode:      "crew",
		Project:   "gasboat",
		Role:      "dev",
		AgentName: "alpha",
	}
	if got := spec.PodName(); got != "crew-gasboat-dev-alpha" {
		t.Errorf("PodName() = %q, want %q", got, "crew-gasboat-dev-alpha")
	}
}

func TestLabels(t *testing.T) {
	spec := AgentPodSpec{
		Mode:      "crew",
		Project:   "gasboat",
		Role:      "dev",
		AgentName: "alpha",
	}
	labels := spec.Labels()
	if labels[LabelApp] != LabelAppValue {
		t.Errorf("missing app label")
	}
	if labels[LabelProject] != "gasboat" {
		t.Errorf("missing project label")
	}
	if labels[LabelMode] != "crew" {
		t.Errorf("missing mode label")
	}
	if labels[LabelRole] != "dev" {
		t.Errorf("missing role label")
	}
	if labels[LabelAgent] != "alpha" {
		t.Errorf("missing agent label")
	}
}

func TestCreateAgentPod_WithPVC(t *testing.T) {
	m, client := newTestManager()
	ctx := context.Background()
	spec := testSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{
		Size:             "5Gi",
		StorageClassName: "gp3",
	}

	if err := m.CreateAgentPod(ctx, spec); err != nil {
		t.Fatalf("CreateAgentPod with PVC: %v", err)
	}

	// PVC should exist.
	pvcs, err := client.CoreV1().PersistentVolumeClaims("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing PVCs: %v", err)
	}
	if len(pvcs.Items) != 1 {
		t.Fatalf("expected 1 PVC, got %d", len(pvcs.Items))
	}
	pvc := pvcs.Items[0]
	if pvc.Name != spec.PodName()+"-workspace" {
		t.Errorf("PVC name = %q, want %q", pvc.Name, spec.PodName()+"-workspace")
	}
}

func TestEnsurePVC_Idempotent(t *testing.T) {
	m, _ := newTestManager()
	ctx := context.Background()
	spec := testSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "10Gi"}

	if err := m.ensurePVC(ctx, spec); err != nil {
		t.Fatalf("first ensurePVC: %v", err)
	}
	// Second call should succeed.
	if err := m.ensurePVC(ctx, spec); err != nil {
		t.Fatalf("second ensurePVC should be idempotent: %v", err)
	}
}
