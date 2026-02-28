package podmanager

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// --- ApplyDefaults tests ---

func TestApplyDefaults_NilDefaults(t *testing.T) {
	spec := &AgentPodSpec{Image: "img:v1"}
	ApplyDefaults(spec, nil)
	if spec.Image != "img:v1" {
		t.Errorf("image should remain img:v1, got %s", spec.Image)
	}
}

func TestApplyDefaults_FillsEmptyFields(t *testing.T) {
	spec := &AgentPodSpec{}
	defaults := &PodDefaults{
		Image:              "default-img:v1",
		ServiceAccountName: "default-sa",
		ConfigMapName:      "default-cfg",
		NodeSelector:       map[string]string{"arch": "amd64"},
		Tolerations:        []corev1.Toleration{{Key: "gpu"}},
		WorkspaceStorage:   &WorkspaceStorageSpec{Size: "10Gi"},
	}

	ApplyDefaults(spec, defaults)

	if spec.Image != "default-img:v1" {
		t.Errorf("Image = %s, want default-img:v1", spec.Image)
	}
	if spec.ServiceAccountName != "default-sa" {
		t.Errorf("ServiceAccountName = %s, want default-sa", spec.ServiceAccountName)
	}
	if spec.ConfigMapName != "default-cfg" {
		t.Errorf("ConfigMapName = %s, want default-cfg", spec.ConfigMapName)
	}
	if spec.NodeSelector["arch"] != "amd64" {
		t.Error("expected NodeSelector from defaults")
	}
	if len(spec.Tolerations) != 1 {
		t.Errorf("expected 1 toleration, got %d", len(spec.Tolerations))
	}
	if spec.WorkspaceStorage == nil || spec.WorkspaceStorage.Size != "10Gi" {
		t.Error("expected WorkspaceStorage from defaults")
	}
}

func TestApplyDefaults_DoesNotOverwriteExisting(t *testing.T) {
	spec := &AgentPodSpec{
		Image:              "spec-img:v2",
		ServiceAccountName: "spec-sa",
		ConfigMapName:      "spec-cfg",
		NodeSelector:       map[string]string{"zone": "us-east"},
		Tolerations:        []corev1.Toleration{{Key: "spot"}},
		WorkspaceStorage:   &WorkspaceStorageSpec{Size: "50Gi"},
	}
	defaults := &PodDefaults{
		Image:              "default-img:v1",
		ServiceAccountName: "default-sa",
		ConfigMapName:      "default-cfg",
		NodeSelector:       map[string]string{"arch": "amd64"},
		Tolerations:        []corev1.Toleration{{Key: "gpu"}},
		WorkspaceStorage:   &WorkspaceStorageSpec{Size: "10Gi"},
	}

	ApplyDefaults(spec, defaults)

	if spec.Image != "spec-img:v2" {
		t.Errorf("Image should not be overwritten, got %s", spec.Image)
	}
	if spec.ServiceAccountName != "spec-sa" {
		t.Errorf("ServiceAccountName should not be overwritten, got %s", spec.ServiceAccountName)
	}
	if spec.NodeSelector["zone"] != "us-east" {
		t.Error("NodeSelector should not be overwritten")
	}
	if spec.Tolerations[0].Key != "spot" {
		t.Error("Tolerations should not be overwritten")
	}
	if spec.WorkspaceStorage.Size != "50Gi" {
		t.Error("WorkspaceStorage should not be overwritten")
	}
}

func TestApplyDefaults_Resources(t *testing.T) {
	spec := &AgentPodSpec{}
	defaults := &PodDefaults{
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
		},
	}

	ApplyDefaults(spec, defaults)
	if spec.Resources == nil {
		t.Fatal("expected Resources from defaults")
	}
	cpu := spec.Resources.Requests[corev1.ResourceCPU]
	if cpu.String() != "1" {
		t.Errorf("CPU request = %s, want 1", cpu.String())
	}
}

func TestApplyDefaults_ResourcesNotOverwritten(t *testing.T) {
	specRes := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("4"),
		},
	}
	spec := &AgentPodSpec{Resources: specRes}
	defaults := &PodDefaults{
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
		},
	}

	ApplyDefaults(spec, defaults)
	cpu := spec.Resources.Requests[corev1.ResourceCPU]
	if cpu.String() != "4" {
		t.Errorf("CPU request should not be overwritten, got %s", cpu.String())
	}
}

func TestApplyDefaults_Affinity(t *testing.T) {
	spec := &AgentPodSpec{}
	defaults := &PodDefaults{
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{},
		},
	}

	ApplyDefaults(spec, defaults)
	if spec.Affinity == nil {
		t.Error("expected Affinity from defaults")
	}
}

func TestApplyDefaults_EnvMerge(t *testing.T) {
	spec := &AgentPodSpec{
		Env: map[string]string{"EXISTING": "keep"},
	}
	defaults := &PodDefaults{
		Env: map[string]string{
			"EXISTING": "ignored",
			"NEW_VAR":  "added",
		},
	}

	ApplyDefaults(spec, defaults)

	if spec.Env["EXISTING"] != "keep" {
		t.Errorf("existing env should not be overwritten, got %s", spec.Env["EXISTING"])
	}
	if spec.Env["NEW_VAR"] != "added" {
		t.Errorf("new env should be added, got %s", spec.Env["NEW_VAR"])
	}
}

func TestApplyDefaults_EnvMerge_NilSpecEnv(t *testing.T) {
	spec := &AgentPodSpec{} // Env is nil
	defaults := &PodDefaults{
		Env: map[string]string{"VAR": "val"},
	}

	ApplyDefaults(spec, defaults)
	if spec.Env["VAR"] != "val" {
		t.Errorf("expected VAR=val, got %s", spec.Env["VAR"])
	}
}

func TestApplyDefaults_SecretEnvDedup(t *testing.T) {
	spec := &AgentPodSpec{
		SecretEnv: []SecretEnvSource{
			{EnvName: "TOKEN", SecretName: "s1", SecretKey: "k1"},
		},
	}
	defaults := &PodDefaults{
		SecretEnv: []SecretEnvSource{
			{EnvName: "TOKEN", SecretName: "s2", SecretKey: "k2"},   // duplicate, should skip
			{EnvName: "API_KEY", SecretName: "s3", SecretKey: "k3"}, // new, should add
		},
	}

	ApplyDefaults(spec, defaults)

	if len(spec.SecretEnv) != 2 {
		t.Fatalf("expected 2 secret env entries, got %d", len(spec.SecretEnv))
	}

	// TOKEN should still point to s1 (not overwritten).
	if spec.SecretEnv[0].SecretName != "s1" {
		t.Errorf("TOKEN should keep original secret s1, got %s", spec.SecretEnv[0].SecretName)
	}
	// API_KEY should be added from defaults.
	if spec.SecretEnv[1].EnvName != "API_KEY" {
		t.Errorf("expected API_KEY appended, got %s", spec.SecretEnv[1].EnvName)
	}
}

// --- DefaultPodDefaults tests ---

func TestDefaultPodDefaults_Crew(t *testing.T) {
	d := DefaultPodDefaults("crew")

	if d.Resources == nil {
		t.Fatal("expected Resources")
	}
	if d.NodeSelector["kubernetes.io/arch"] != "amd64" {
		t.Error("expected amd64 NodeSelector")
	}
	if d.Affinity == nil {
		t.Error("expected Affinity")
	}
	if d.WorkspaceStorage == nil {
		t.Fatal("expected WorkspaceStorage for crew mode")
	}
	if d.WorkspaceStorage.Size != "10Gi" {
		t.Errorf("expected 10Gi workspace, got %s", d.WorkspaceStorage.Size)
	}
}

func TestDefaultPodDefaults_Job(t *testing.T) {
	d := DefaultPodDefaults("job")

	if d.Resources == nil {
		t.Fatal("expected Resources")
	}
	if d.WorkspaceStorage != nil {
		t.Error("expected no WorkspaceStorage for job mode")
	}
}

func TestDefaultPodDefaults_Affinity_ExcludesPerMRNodes(t *testing.T) {
	d := DefaultPodDefaults("crew")

	if d.Affinity.NodeAffinity == nil {
		t.Fatal("expected NodeAffinity")
	}

	required := d.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil {
		t.Fatal("expected required node selector")
	}

	// Should exclude per-mr nodes.
	var foundPerMR bool
	for _, term := range required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == "per-mr" && expr.Operator == corev1.NodeSelectorOpDoesNotExist {
				foundPerMR = true
			}
		}
	}
	if !foundPerMR {
		t.Error("expected per-mr DoesNotExist in required scheduling")
	}
}

func TestDefaultPodDefaults_Affinity_PrefersOnDemand(t *testing.T) {
	d := DefaultPodDefaults("crew")

	preferred := d.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(preferred) < 1 {
		t.Fatal("expected preferred scheduling terms")
	}

	var foundOnDemand bool
	for _, term := range preferred {
		for _, expr := range term.Preference.MatchExpressions {
			if expr.Key == "karpenter.sh/capacity-type" {
				for _, v := range expr.Values {
					if v == "on-demand" {
						foundOnDemand = true
					}
				}
			}
		}
	}
	if !foundOnDemand {
		t.Error("expected on-demand preference in scheduling")
	}
}
