package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
)

func TestApplyProjectDefaults_ResourceOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				CPURequest:    "500m",
				CPULimit:      "2000m",
				MemoryRequest: "512Mi",
				MemoryLimit:   "2Gi",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}

	applyProjectDefaults(cfg, spec)

	if got := spec.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("500m")) != 0 {
		t.Errorf("cpu_request: got %s, want 500m", got.String())
	}
	if got := spec.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse("2000m")) != 0 {
		t.Errorf("cpu_limit: got %s, want 2000m", got.String())
	}
	if got := spec.Resources.Requests[corev1.ResourceMemory]; got.Cmp(resource.MustParse("512Mi")) != 0 {
		t.Errorf("memory_request: got %s, want 512Mi", got.String())
	}
	if got := spec.Resources.Limits[corev1.ResourceMemory]; got.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Errorf("memory_limit: got %s, want 2Gi", got.String())
	}
}

func TestApplyProjectDefaults_ServiceAccount(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {ServiceAccount: "project-sa"},
		},
	}
	spec := &podmanager.AgentPodSpec{Project: "myproject"}

	applyProjectDefaults(cfg, spec)

	if spec.ServiceAccountName != "project-sa" {
		t.Errorf("service_account: got %q, want %q", spec.ServiceAccountName, "project-sa")
	}
}


func TestApplyProjectDefaults_EnvOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				EnvOverrides: map[string]string{
					"FOO": "from-project",
					"BAR": "from-project",
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env: map[string]string{
			"FOO": "from-spec", // spec value should NOT be overridden
		},
	}

	applyProjectDefaults(cfg, spec)

	if spec.Env["FOO"] != "from-spec" {
		t.Errorf("FOO should not be overridden; got %q, want %q", spec.Env["FOO"], "from-spec")
	}
	if spec.Env["BAR"] != "from-project" {
		t.Errorf("BAR: got %q, want %q", spec.Env["BAR"], "from-project")
	}
}

func TestApplyProjectDefaults_NoProject(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"other": {ServiceAccount: "other-sa"},
		},
	}
	spec := &podmanager.AgentPodSpec{Project: "unknown"}

	// Should not panic and spec should remain unchanged.
	applyProjectDefaults(cfg, spec)

	if spec.ServiceAccountName != "" {
		t.Errorf("expected empty SA for unknown project, got %q", spec.ServiceAccountName)
	}
}

func TestApplyProjectDefaults_InvalidQuantitySkipped(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				CPURequest: "not-a-quantity",
				CPULimit:   "1000m",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100m"),
			},
			Limits: corev1.ResourceList{},
		},
	}

	// Should not panic; invalid quantity is silently skipped.
	applyProjectDefaults(cfg, spec)

	// CPU limit should be set (valid quantity).
	if got := spec.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse("1000m")) != 0 {
		t.Errorf("cpu_limit: got %s, want 1000m", got.String())
	}
	// CPU request should remain unchanged (invalid quantity skipped).
	if got := spec.Resources.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse("100m")) != 0 {
		t.Errorf("cpu_request should be unchanged; got %s, want 100m", got.String())
	}
}
