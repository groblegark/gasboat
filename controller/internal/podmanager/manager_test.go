package podmanager

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPodName(t *testing.T) {
	spec := AgentPodSpec{Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "alpha"}
	if got := spec.PodName(); got != "crew-gasboat-dev-alpha" {
		t.Errorf("PodName() = %s, want crew-gasboat-dev-alpha", got)
	}
}

func TestLabels(t *testing.T) {
	spec := AgentPodSpec{Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "alpha"}
	labels := spec.Labels()
	expected := map[string]string{LabelApp: LabelAppValue, LabelProject: "gasboat", LabelMode: "crew", LabelRole: "dev", LabelAgent: "alpha"}
	for k, v := range expected {
		if labels[k] != v {
			t.Errorf("Labels()[%s] = %s, want %s", k, labels[k], v)
		}
	}
}

func TestRestartPolicyForMode_Crew(t *testing.T) {
	if got := restartPolicyForMode("crew"); got != corev1.RestartPolicyAlways {
		t.Errorf("restartPolicyForMode(crew) = %s, want Always", got)
	}
}

func TestRestartPolicyForMode_Job(t *testing.T) {
	if got := restartPolicyForMode("job"); got != corev1.RestartPolicyNever {
		t.Errorf("restartPolicyForMode(job) = %s, want Never", got)
	}
}

func TestRestartPolicyForMode_Unknown(t *testing.T) {
	if got := restartPolicyForMode("other"); got != corev1.RestartPolicyAlways {
		t.Errorf("restartPolicyForMode(other) = %s, want Always", got)
	}
}

func TestCreateAgentPod_Basic(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "ghcr.io/org/agent:v1", Namespace: "ns", BeadID: "kd-abc"}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pod, err := client.CoreV1().Pods("ns").Get(context.Background(), "crew-proj-dev-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod not found: %v", err)
	}
	if pod.Labels[LabelProject] != "proj" {
		t.Errorf("expected project label proj, got %s", pod.Labels[LabelProject])
	}
	if pod.Annotations[AnnotationBeadID] != "kd-abc" {
		t.Errorf("expected bead-id annotation kd-abc, got %s", pod.Annotations[AnnotationBeadID])
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != "ghcr.io/org/agent:v1" {
		t.Error("unexpected container setup")
	}
}

func TestCreateAgentPod_Idempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "ghcr.io/org/agent:v1", Namespace: "ns"}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("first CreateAgentPod: %v", err)
	}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("second CreateAgentPod should be idempotent: %v", err)
	}
}

func TestCreateAgentPod_WithPVC(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", WorkspaceStorage: &WorkspaceStorageSpec{Size: "20Gi", StorageClassName: "gp3"}}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("ns").Get(context.Background(), "crew-proj-dev-alpha-workspace", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PVC not found: %v", err)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "gp3" {
		t.Errorf("expected storage class gp3")
	}
}

func TestCreateAgentPod_PVCIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", WorkspaceStorage: &WorkspaceStorageSpec{ClaimName: "my-pvc", Size: "10Gi"}}
	client.CoreV1().PersistentVolumeClaims("ns").Create(context.Background(), &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "ns"}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}}}, metav1.CreateOptions{})
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsurePVC_Idempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", WorkspaceStorage: &WorkspaceStorageSpec{Size: "10Gi"}}
	if err := mgr.ensurePVC(context.Background(), spec); err != nil {
		t.Fatalf("first ensurePVC: %v", err)
	}
	if err := mgr.ensurePVC(context.Background(), spec); err != nil {
		t.Fatalf("second ensurePVC should be idempotent: %v", err)
	}
}

func TestDeleteAgentPod(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "ns"}})
	mgr := New(client, testLogger())
	if err := mgr.DeleteAgentPod(context.Background(), "test-pod", "ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := client.CoreV1().Pods("ns").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestDeleteAgentPod_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	if err := mgr.DeleteAgentPod(context.Background(), "nonexistent", "ns"); err != nil {
		t.Fatalf("DeleteAgentPod for missing pod should be idempotent: %v", err)
	}
}

func TestListAgentPods(t *testing.T) {
	pod1 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "crew-proj-dev-alpha", Namespace: "ns", Labels: map[string]string{LabelApp: LabelAppValue, LabelProject: "proj"}}}
	pod2 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "crew-other-dev-beta", Namespace: "ns", Labels: map[string]string{LabelApp: LabelAppValue, LabelProject: "other"}}}
	client := fake.NewSimpleClientset(pod1, pod2)
	mgr := New(client, testLogger())
	pods, err := mgr.ListAgentPods(context.Background(), "ns", map[string]string{LabelProject: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "crew-proj-dev-alpha" {
		t.Errorf("expected 1 pod crew-proj-dev-alpha, got %d pods", len(pods))
	}
}

func TestGetAgentPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "ns"}}
	client := fake.NewSimpleClientset(pod)
	mgr := New(client, testLogger())
	got, err := mgr.GetAgentPod(context.Background(), "my-pod", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "my-pod" {
		t.Errorf("expected my-pod, got %s", got.Name)
	}
}

func TestGetAgentPod_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	_, err := mgr.GetAgentPod(context.Background(), "nonexistent", "ns")
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestBuildPod_SecurityContext(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns"}
	pod := mgr.buildPod(spec)
	sc := pod.Spec.SecurityContext
	if sc == nil || *sc.RunAsUser != AgentUID || !*sc.RunAsNonRoot {
		t.Error("expected proper pod security context")
	}
}

func TestBuildPod_InitContainerWithGitURL(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", GitURL: "https://github.com/org/repo.git"}
	pod := mgr.buildPod(spec)
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != InitCloneName {
		t.Error("expected 1 init container named init-clone")
	}
}

func TestBuildContainer_Ports(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	c := mgr.buildContainer(AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1"})
	if len(c.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(c.Ports))
	}
}

func TestBuildEnvVars_CoreVars(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	envVars := mgr.buildEnvVars(AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", BeadID: "kd-abc"})
	envMap := map[string]string{}
	for _, e := range envVars {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}
	if envMap["BOAT_PROJECT"] != "proj" || envMap["BOAT_AGENT"] != "alpha" || envMap["KD_AGENT_ID"] != "kd-abc" {
		t.Error("missing expected core env vars")
	}
}

func TestBuildEnvVars_PodIP(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
	}

	envVars := mgr.buildEnvVars(spec)

	var foundPodIP bool
	for _, e := range envVars {
		if e.Name == "POD_IP" {
			foundPodIP = true
			if e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "status.podIP" {
				t.Error("POD_IP should come from downward API status.podIP")
			}
		}
	}
	if !foundPodIP {
		t.Error("expected POD_IP env var from downward API")
	}
}

func TestBuildEnvVars_SessionResume(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())

	// Without workspace storage — no session resume.
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha"}
	envVars := mgr.buildEnvVars(spec)
	for _, e := range envVars {
		if e.Name == "BOAT_SESSION_RESUME" {
			t.Error("BOAT_SESSION_RESUME should not be set without workspace storage")
		}
	}

	// With workspace storage — session resume enabled.
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "10Gi"}
	envVars = mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "BOAT_SESSION_RESUME" {
			found = true
			if e.Value != "1" {
				t.Errorf("BOAT_SESSION_RESUME = %s, want 1", e.Value)
			}
		}
	}
	if !found {
		t.Error("expected BOAT_SESSION_RESUME with workspace storage")
	}
}

func TestBuildEnvVars_CustomEnv(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Env: map[string]string{"CUSTOM_VAR": "custom_value"},
	}

	envVars := mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "CUSTOM_VAR" && e.Value == "custom_value" {
			found = true
		}
	}
	if !found {
		t.Error("expected CUSTOM_VAR=custom_value")
	}
}

func TestBuildEnvVars_SecretEnv(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		SecretEnv: []SecretEnvSource{
			{EnvName: "API_KEY", SecretName: "api-secret", SecretKey: "key"},
		},
	}

	envVars := mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "API_KEY" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			found = true
			if e.ValueFrom.SecretKeyRef.Name != "api-secret" {
				t.Errorf("secret name = %s, want api-secret", e.ValueFrom.SecretKeyRef.Name)
			}
			if e.ValueFrom.SecretKeyRef.Key != "key" {
				t.Errorf("secret key = %s, want key", e.ValueFrom.SecretKeyRef.Key)
			}
		}
	}
	if !found {
		t.Error("expected API_KEY secret env var")
	}
}

func TestBuildEnvVars_DaemonToken(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		DaemonTokenSecret: "beads-token",
	}

	envVars := mgr.buildEnvVars(spec)
	var foundDaemon, foundAuth bool
	for _, e := range envVars {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			switch e.Name {
			case "BEADS_DAEMON_TOKEN":
				foundDaemon = true
				if e.ValueFrom.SecretKeyRef.Name != "beads-token" {
					t.Errorf("daemon token secret name = %s, want beads-token", e.ValueFrom.SecretKeyRef.Name)
				}
			case "BEADS_AUTH_TOKEN":
				foundAuth = true
				if e.ValueFrom.SecretKeyRef.Name != "beads-token" {
					t.Errorf("auth token secret name = %s, want beads-token", e.ValueFrom.SecretKeyRef.Name)
				}
			}
		}
	}
	if !foundDaemon {
		t.Error("expected BEADS_DAEMON_TOKEN from secret")
	}
	if !foundAuth {
		t.Error("expected BEADS_AUTH_TOKEN from secret")
	}
}

// --- buildResources tests ---

func TestBuildResources_Default(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	res := mgr.buildResources(AgentPodSpec{})
	if res.Requests[corev1.ResourceCPU].String() != DefaultCPURequest {
		t.Errorf("default CPU request = %s, want %s", res.Requests[corev1.ResourceCPU].String(), DefaultCPURequest)
	}
}

func TestBuildResources_Custom(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	custom := &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}}
	res := mgr.buildResources(AgentPodSpec{Resources: custom})
	if res.Requests[corev1.ResourceCPU].String() != "500m" {
		t.Errorf("custom CPU request = %s, want 500m", res.Requests[corev1.ResourceCPU].String())
	}
}

func TestBuildInitCloneContainer_WithGitURL(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", GitURL: "https://github.com/org/repo.git", GitDefaultBranch: "develop"}
	ic := mgr.buildInitCloneContainer(spec)
	if ic == nil {
		t.Fatal("expected init container with GitURL")
	}
	script := ic.Command[2]
	if !strings.Contains(script, "git clone -b develop") || !strings.Contains(script, "https://github.com/org/repo.git") {
		t.Error("script should contain git clone with specified branch and URL")
	}
}

func TestBuildInitCloneContainer_DefaultBranch(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	ic := mgr.buildInitCloneContainer(AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", GitURL: "https://github.com/org/repo.git"})
	if !strings.Contains(ic.Command[2], "git clone -b main") {
		t.Error("script should default to main branch")
	}
}

func TestBuildInitCloneContainer_ChownsWorkspace(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	ic := mgr.buildInitCloneContainer(AgentPodSpec{Mode: "crew", Project: "myproj", Role: "dev", AgentName: "alpha", GitURL: "https://github.com/org/repo.git"})
	if !strings.Contains(ic.Command[2], "chown -R 1000:1000") {
		t.Errorf("script should chown workspace to %d:%d", AgentUID, AgentGID)
	}
}
