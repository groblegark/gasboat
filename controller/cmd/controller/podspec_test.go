package main

import (
	"testing"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"

	corev1 "k8s.io/api/core/v1"
)

func TestOverrideOrAppendSecretEnv_OverridesExisting(t *testing.T) {
	envs := []podmanager.SecretEnvSource{
		{EnvName: "GITHUB_TOKEN", SecretName: "global-gh", SecretKey: "token"},
		{EnvName: "OTHER_SECRET", SecretName: "other", SecretKey: "key"},
	}
	src := podmanager.SecretEnvSource{
		EnvName: "GITHUB_TOKEN", SecretName: "project-gh", SecretKey: "my-token",
	}
	overrideOrAppendSecretEnv(&envs, src)

	if len(envs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(envs))
	}
	if envs[0].SecretName != "project-gh" {
		t.Errorf("expected SecretName project-gh, got %s", envs[0].SecretName)
	}
	if envs[0].SecretKey != "my-token" {
		t.Errorf("expected SecretKey my-token, got %s", envs[0].SecretKey)
	}
}

func TestOverrideOrAppendSecretEnv_AppendsNew(t *testing.T) {
	envs := []podmanager.SecretEnvSource{
		{EnvName: "GITHUB_TOKEN", SecretName: "global-gh", SecretKey: "token"},
	}
	src := podmanager.SecretEnvSource{
		EnvName: "JIRA_API_TOKEN", SecretName: "proj-jira", SecretKey: "api-token",
	}
	overrideOrAppendSecretEnv(&envs, src)

	if len(envs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(envs))
	}
	if envs[1].EnvName != "JIRA_API_TOKEN" {
		t.Errorf("expected JIRA_API_TOKEN, got %s", envs[1].EnvName)
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/org/my-repo.git", "my-repo"},
		{"https://github.com/org/my-repo", "my-repo"},
		{"https://gitlab.com/PiHealth/CoreFICS/monorepo", "monorepo"},
		{"https://gitlab.com/PiHealth/CoreFICS/monorepo.git", "monorepo"},
		{"repo", "repo"},
	}
	for _, tc := range tests {
		got := repoNameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestApplyCommonConfig_PerProjectSecretOverride(t *testing.T) {
	cfg := &config.Config{
		GithubTokenSecret: "global-gh-token",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "GITHUB_TOKEN", Secret: "project-gh-token", Key: "my-token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	// GITHUB_TOKEN should be overridden to project-specific secret.
	found := false
	for _, se := range spec.SecretEnv {
		if se.EnvName == "GITHUB_TOKEN" {
			found = true
			if se.SecretName != "project-gh-token" {
				t.Errorf("expected SecretName project-gh-token, got %s", se.SecretName)
			}
			if se.SecretKey != "my-token" {
				t.Errorf("expected SecretKey my-token, got %s", se.SecretKey)
			}
		}
	}
	if !found {
		t.Error("GITHUB_TOKEN not found in SecretEnv")
	}
}

func TestApplyCommonConfig_PerProjectSecretAdditive(t *testing.T) {
	cfg := &config.Config{
		GithubTokenSecret: "global-gh-token",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "JIRA_API_TOKEN", Secret: "proj-jira", Key: "api-token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	// Both GITHUB_TOKEN (global) and JIRA_API_TOKEN (project) should be present.
	envNames := map[string]bool{}
	for _, se := range spec.SecretEnv {
		envNames[se.EnvName] = true
	}
	if !envNames["GITHUB_TOKEN"] {
		t.Error("expected GITHUB_TOKEN from global config")
	}
	if !envNames["JIRA_API_TOKEN"] {
		t.Error("expected JIRA_API_TOKEN from project config")
	}
}

func TestApplyCommonConfig_GitCredentialOverride(t *testing.T) {
	cfg := &config.Config{
		GitCredentialsSecret: "global-git-creds",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "GIT_TOKEN", Secret: "project-git-creds", Key: "token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitCredentialsSecret != "project-git-creds" {
		t.Errorf("expected GitCredentialsSecret project-git-creds, got %s", spec.GitCredentialsSecret)
	}
}

func TestApplyCommonConfig_NoProjectOverrides(t *testing.T) {
	cfg := &config.Config{
		GithubTokenSecret: "global-gh-token",
		ProjectCache:      map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	// Should still have the global GITHUB_TOKEN.
	found := false
	for _, se := range spec.SecretEnv {
		if se.EnvName == "GITHUB_TOKEN" {
			found = true
			if se.SecretName != "global-gh-token" {
				t.Errorf("expected global-gh-token, got %s", se.SecretName)
			}
		}
	}
	if !found {
		t.Error("expected GITHUB_TOKEN from global config")
	}
}

func TestApplyCommonConfig_MultiRepo(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Repos: []beadsapi.RepoEntry{
					{URL: "https://github.com/org/main-repo.git", Branch: "develop", Role: "primary"},
					{URL: "https://github.com/org/shared-lib.git", Role: "reference", Name: "shared-lib"},
					{URL: "https://github.com/org/other.git", Role: "reference"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitURL != "https://github.com/org/main-repo.git" {
		t.Errorf("expected primary GitURL, got %s", spec.GitURL)
	}
	if spec.GitDefaultBranch != "develop" {
		t.Errorf("expected develop branch, got %s", spec.GitDefaultBranch)
	}
	if len(spec.ReferenceRepos) != 2 {
		t.Fatalf("expected 2 reference repos, got %d", len(spec.ReferenceRepos))
	}
	if spec.ReferenceRepos[0].Name != "shared-lib" {
		t.Errorf("expected shared-lib, got %s", spec.ReferenceRepos[0].Name)
	}
	if spec.ReferenceRepos[1].Name != "other" {
		t.Errorf("expected other (derived from URL), got %s", spec.ReferenceRepos[1].Name)
	}

	// BOAT_REFERENCE_REPOS should be set.
	refRepos := spec.Env["BOAT_REFERENCE_REPOS"]
	if refRepos == "" {
		t.Fatal("expected BOAT_REFERENCE_REPOS to be set")
	}
}

func TestApplyCommonConfig_LegacySingleRepo(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				GitURL:        "https://github.com/org/legacy.git",
				DefaultBranch: "master",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitURL != "https://github.com/org/legacy.git" {
		t.Errorf("expected legacy GitURL, got %s", spec.GitURL)
	}
	if spec.GitDefaultBranch != "master" {
		t.Errorf("expected master branch, got %s", spec.GitDefaultBranch)
	}
	if len(spec.ReferenceRepos) != 0 {
		t.Errorf("expected no reference repos, got %d", len(spec.ReferenceRepos))
	}
}

func TestApplyCommonConfig_ReferenceOnlyRepos(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Repos: []beadsapi.RepoEntry{
					{URL: "https://github.com/org/ref1.git", Role: "reference", Name: "ref1"},
					{URL: "https://github.com/org/ref2.git", Role: "reference", Name: "ref2"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitURL != "" {
		t.Errorf("expected empty GitURL, got %s", spec.GitURL)
	}
	if len(spec.ReferenceRepos) != 2 {
		t.Fatalf("expected 2 reference repos, got %d", len(spec.ReferenceRepos))
	}
}

func TestApplyProjectDefaults_ResourceOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"heavy": {
				CPURequest:    "4",
				CPULimit:      "8",
				MemoryRequest: "4Gi",
				MemoryLimit:   "16Gi",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "heavy",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Resources == nil {
		t.Fatal("expected Resources to be set")
	}
	if got := spec.Resources.Requests[corev1.ResourceCPU]; got.String() != "4" {
		t.Errorf("expected cpu request 4, got %s", got.String())
	}
	if got := spec.Resources.Requests[corev1.ResourceMemory]; got.String() != "4Gi" {
		t.Errorf("expected memory request 4Gi, got %s", got.String())
	}
	if got := spec.Resources.Limits[corev1.ResourceCPU]; got.String() != "8" {
		t.Errorf("expected cpu limit 8, got %s", got.String())
	}
	if got := spec.Resources.Limits[corev1.ResourceMemory]; got.String() != "16Gi" {
		t.Errorf("expected memory limit 16Gi, got %s", got.String())
	}
}

func TestApplyProjectDefaults_PartialResourceOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"partial": {
				CPURequest:  "2",
				MemoryLimit: "8Gi",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "partial",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Resources == nil {
		t.Fatal("expected Resources to be set for partial overrides")
	}
	if got := spec.Resources.Requests[corev1.ResourceCPU]; got.String() != "2" {
		t.Errorf("expected cpu request 2, got %s", got.String())
	}
	// Memory request should not be set.
	if _, ok := spec.Resources.Requests[corev1.ResourceMemory]; ok {
		t.Error("expected memory request to be unset")
	}
	if got := spec.Resources.Limits[corev1.ResourceMemory]; got.String() != "8Gi" {
		t.Errorf("expected memory limit 8Gi, got %s", got.String())
	}
}

func TestApplyProjectDefaults_NoResourceOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"simple": {Image: "custom:latest"},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "simple",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Resources != nil {
		t.Error("expected Resources to be nil when no resource fields set")
	}
}

func TestApplyProjectDefaults_InvalidResourceQuantity(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"bad": {
				CPURequest:  "not-a-quantity",
				MemoryLimit: "8Gi", // valid
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "bad",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	// Resources should still be set (memory_limit is valid).
	if spec.Resources == nil {
		t.Fatal("expected Resources to be set")
	}
	// Invalid cpu_request should be silently ignored.
	if _, ok := spec.Resources.Requests[corev1.ResourceCPU]; ok {
		t.Error("expected invalid cpu_request to be skipped")
	}
	if got := spec.Resources.Limits[corev1.ResourceMemory]; got.String() != "8Gi" {
		t.Errorf("expected memory limit 8Gi, got %s", got.String())
	}
}

func TestApplyProjectDefaults_EnvOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				EnvOverrides: map[string]string{
					"CUSTOM_VAR": "custom_value",
					"API_URL":    "https://api.example.com",
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{"EXISTING": "keep"},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Env["CUSTOM_VAR"] != "custom_value" {
		t.Errorf("expected CUSTOM_VAR=custom_value, got %s", spec.Env["CUSTOM_VAR"])
	}
	if spec.Env["API_URL"] != "https://api.example.com" {
		t.Errorf("expected API_URL, got %s", spec.Env["API_URL"])
	}
	if spec.Env["EXISTING"] != "keep" {
		t.Errorf("expected EXISTING=keep to be preserved, got %s", spec.Env["EXISTING"])
	}
}

func TestApplyProjectDefaults_EnvOverrides_NilMap(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				EnvOverrides: map[string]string{"KEY": "val"},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		// Env is nil.
	}
	applyProjectDefaults(cfg, spec)

	if spec.Env == nil {
		t.Fatal("expected Env to be initialized")
	}
	if spec.Env["KEY"] != "val" {
		t.Errorf("expected KEY=val, got %s", spec.Env["KEY"])
	}
}
