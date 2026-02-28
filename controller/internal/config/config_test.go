package config

import (
	"testing"
	"time"
)

func TestEnvOr(t *testing.T) {
	t.Run("returns env value when set", func(t *testing.T) {
		t.Setenv("TEST_ENV_OR", "custom")
		got := envOr("TEST_ENV_OR", "default")
		if got != "custom" {
			t.Errorf("envOr() = %q, want %q", got, "custom")
		}
	})

	t.Run("returns fallback when unset", func(t *testing.T) {
		got := envOr("TEST_ENV_OR_UNSET", "default")
		if got != "default" {
			t.Errorf("envOr() = %q, want %q", got, "default")
		}
	})

	t.Run("returns fallback when empty", func(t *testing.T) {
		t.Setenv("TEST_ENV_OR_EMPTY", "")
		got := envOr("TEST_ENV_OR_EMPTY", "default")
		if got != "default" {
			t.Errorf("envOr() = %q, want %q", got, "default")
		}
	})
}

func TestEnvIntOr(t *testing.T) {
	t.Run("returns env value when valid int", func(t *testing.T) {
		t.Setenv("TEST_INT", "42")
		got := envIntOr("TEST_INT", 0)
		if got != 42 {
			t.Errorf("envIntOr() = %d, want %d", got, 42)
		}
	})

	t.Run("returns fallback when unset", func(t *testing.T) {
		got := envIntOr("TEST_INT_UNSET", 10)
		if got != 10 {
			t.Errorf("envIntOr() = %d, want %d", got, 10)
		}
	})

	t.Run("returns fallback when invalid", func(t *testing.T) {
		t.Setenv("TEST_INT_INVALID", "not-a-number")
		got := envIntOr("TEST_INT_INVALID", 5)
		if got != 5 {
			t.Errorf("envIntOr() = %d, want %d", got, 5)
		}
	})

	t.Run("handles zero value", func(t *testing.T) {
		t.Setenv("TEST_INT_ZERO", "0")
		got := envIntOr("TEST_INT_ZERO", 99)
		if got != 0 {
			t.Errorf("envIntOr() = %d, want %d", got, 0)
		}
	})

	t.Run("handles negative value", func(t *testing.T) {
		t.Setenv("TEST_INT_NEG", "-3")
		got := envIntOr("TEST_INT_NEG", 1)
		if got != -3 {
			t.Errorf("envIntOr() = %d, want %d", got, -3)
		}
	})
}

func TestEnvBoolOr(t *testing.T) {
	t.Run("returns true for truthy values", func(t *testing.T) {
		for _, v := range []string{"true", "1", "TRUE", "True", "t", "T"} {
			t.Setenv("TEST_BOOL", v)
			got := envBoolOr("TEST_BOOL", false)
			if !got {
				t.Errorf("envBoolOr(%q) = false, want true", v)
			}
		}
	})

	t.Run("returns false for falsy values", func(t *testing.T) {
		for _, v := range []string{"false", "0", "FALSE", "False", "f", "F"} {
			t.Setenv("TEST_BOOL", v)
			got := envBoolOr("TEST_BOOL", true)
			if got {
				t.Errorf("envBoolOr(%q) = true, want false", v)
			}
		}
	})

	t.Run("returns fallback when unset", func(t *testing.T) {
		got := envBoolOr("TEST_BOOL_UNSET", true)
		if !got {
			t.Error("envBoolOr() = false, want true")
		}
	})

	t.Run("returns fallback when invalid", func(t *testing.T) {
		t.Setenv("TEST_BOOL_INVALID", "maybe")
		got := envBoolOr("TEST_BOOL_INVALID", true)
		if !got {
			t.Error("envBoolOr() = false, want true (fallback)")
		}
	})
}

func TestEnvDurationOr(t *testing.T) {
	t.Run("parses valid duration", func(t *testing.T) {
		t.Setenv("TEST_DUR", "30s")
		got := envDurationOr("TEST_DUR", time.Minute)
		if got != 30*time.Second {
			t.Errorf("envDurationOr() = %v, want %v", got, 30*time.Second)
		}
	})

	t.Run("parses complex duration", func(t *testing.T) {
		t.Setenv("TEST_DUR", "2m30s")
		got := envDurationOr("TEST_DUR", time.Minute)
		if got != 2*time.Minute+30*time.Second {
			t.Errorf("envDurationOr() = %v, want %v", got, 2*time.Minute+30*time.Second)
		}
	})

	t.Run("returns fallback when unset", func(t *testing.T) {
		got := envDurationOr("TEST_DUR_UNSET", 5*time.Minute)
		if got != 5*time.Minute {
			t.Errorf("envDurationOr() = %v, want %v", got, 5*time.Minute)
		}
	})

	t.Run("returns fallback when invalid", func(t *testing.T) {
		t.Setenv("TEST_DUR_INVALID", "not-a-duration")
		got := envDurationOr("TEST_DUR_INVALID", time.Hour)
		if got != time.Hour {
			t.Errorf("envDurationOr() = %v, want %v", got, time.Hour)
		}
	})
}

func TestHostname(t *testing.T) {
	h := hostname()
	if h == "" {
		t.Error("hostname() returned empty string")
	}
}

func TestParse_Defaults(t *testing.T) {
	// Clear env vars that Parse reads so we test defaults.
	for _, key := range []string{
		"NAMESPACE", "KUBECONFIG", "BEADS_GRPC_ADDR", "BEADS_HTTP_ADDR",
		"BEADS_E2E_HTTP_ADDR", "BEADS_TOKEN_SECRET", "NATS_URL", "NATS_TOKEN_SECRET",
		"COOP_IMAGE", "COOP_SERVICE_ACCOUNT", "COOP_MAX_PODS", "COOP_BURST_LIMIT",
		"COOP_SYNC_INTERVAL", "AGENT_STORAGE_CLASS", "CLAUDE_MODEL",
		"CLAUDE_OAUTH_SECRET", "CLAUDE_OAUTH_TOKEN_SECRET", "ANTHROPIC_API_KEY_SECRET",
		"GIT_CREDENTIALS_SECRET", "GITHUB_TOKEN_SECRET", "GITLAB_TOKEN_SECRET",
		"RWX_ACCESS_TOKEN_SECRET", "COOPMUX_URL", "COOPMUX_TOKEN_SECRET",
		"ENABLE_LEADER_ELECTION", "LEADER_ELECTION_ID", "POD_NAME",
		"EXTERNAL_SECRET_STORE_NAME", "EXTERNAL_SECRET_STORE_KIND",
		"EXTERNAL_SECRET_REFRESH_INTERVAL", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}

	cfg := Parse()

	if cfg.Namespace != "gasboat" {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, "gasboat")
	}
	if cfg.BeadsGRPCAddr != "localhost:9090" {
		t.Errorf("BeadsGRPCAddr = %q, want %q", cfg.BeadsGRPCAddr, "localhost:9090")
	}
	if cfg.BeadsHTTPAddr != "localhost:8080" {
		t.Errorf("BeadsHTTPAddr = %q, want %q", cfg.BeadsHTTPAddr, "localhost:8080")
	}
	if cfg.CoopMaxPods != 0 {
		t.Errorf("CoopMaxPods = %d, want %d", cfg.CoopMaxPods, 0)
	}
	if cfg.CoopBurstLimit != 3 {
		t.Errorf("CoopBurstLimit = %d, want %d", cfg.CoopBurstLimit, 3)
	}
	if cfg.CoopSyncInterval != 60*time.Second {
		t.Errorf("CoopSyncInterval = %v, want %v", cfg.CoopSyncInterval, 60*time.Second)
	}
	if cfg.LeaderElection {
		t.Error("LeaderElection should default to false")
	}
	if cfg.LeaderElectionID != "agents-leader" {
		t.Errorf("LeaderElectionID = %q, want %q", cfg.LeaderElectionID, "agents-leader")
	}
	if cfg.ExternalSecretStoreName != "secretstore" {
		t.Errorf("ExternalSecretStoreName = %q, want %q", cfg.ExternalSecretStoreName, "secretstore")
	}
	if cfg.ExternalSecretStoreKind != "ClusterSecretStore" {
		t.Errorf("ExternalSecretStoreKind = %q, want %q", cfg.ExternalSecretStoreKind, "ClusterSecretStore")
	}
	if cfg.ExternalSecretRefreshInterval != "15m" {
		t.Errorf("ExternalSecretRefreshInterval = %q, want %q", cfg.ExternalSecretRefreshInterval, "15m")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestParse_EnvOverrides(t *testing.T) {
	t.Setenv("NAMESPACE", "custom-ns")
	t.Setenv("BEADS_GRPC_ADDR", "beads:9090")
	t.Setenv("BEADS_HTTP_ADDR", "beads:8080")
	t.Setenv("COOP_IMAGE", "ghcr.io/org/agent:latest")
	t.Setenv("COOP_MAX_PODS", "20")
	t.Setenv("COOP_BURST_LIMIT", "5")
	t.Setenv("COOP_SYNC_INTERVAL", "30s")
	t.Setenv("ENABLE_LEADER_ELECTION", "true")
	t.Setenv("CLAUDE_MODEL", "claude-opus-4-6")
	t.Setenv("LOG_LEVEL", "debug")

	cfg := Parse()

	if cfg.Namespace != "custom-ns" {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, "custom-ns")
	}
	if cfg.BeadsGRPCAddr != "beads:9090" {
		t.Errorf("BeadsGRPCAddr = %q, want %q", cfg.BeadsGRPCAddr, "beads:9090")
	}
	if cfg.BeadsHTTPAddr != "beads:8080" {
		t.Errorf("BeadsHTTPAddr = %q, want %q", cfg.BeadsHTTPAddr, "beads:8080")
	}
	if cfg.CoopImage != "ghcr.io/org/agent:latest" {
		t.Errorf("CoopImage = %q, want %q", cfg.CoopImage, "ghcr.io/org/agent:latest")
	}
	if cfg.CoopMaxPods != 20 {
		t.Errorf("CoopMaxPods = %d, want %d", cfg.CoopMaxPods, 20)
	}
	if cfg.CoopBurstLimit != 5 {
		t.Errorf("CoopBurstLimit = %d, want %d", cfg.CoopBurstLimit, 5)
	}
	if cfg.CoopSyncInterval != 30*time.Second {
		t.Errorf("CoopSyncInterval = %v, want %v", cfg.CoopSyncInterval, 30*time.Second)
	}
	if !cfg.LeaderElection {
		t.Error("LeaderElection should be true when ENABLE_LEADER_ELECTION=true")
	}
	if cfg.ClaudeModel != "claude-opus-4-6" {
		t.Errorf("ClaudeModel = %q, want %q", cfg.ClaudeModel, "claude-opus-4-6")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestParse_SecretEnvVars(t *testing.T) {
	t.Setenv("BEADS_TOKEN_SECRET", "beads-token")
	t.Setenv("CLAUDE_OAUTH_SECRET", "claude-oauth")
	t.Setenv("CLAUDE_OAUTH_TOKEN_SECRET", "claude-token")
	t.Setenv("ANTHROPIC_API_KEY_SECRET", "anthropic-key")
	t.Setenv("GIT_CREDENTIALS_SECRET", "git-creds")
	t.Setenv("GITHUB_TOKEN_SECRET", "gh-token")
	t.Setenv("GITLAB_TOKEN_SECRET", "gl-token")
	t.Setenv("RWX_ACCESS_TOKEN_SECRET", "rwx-token")
	t.Setenv("NATS_TOKEN_SECRET", "nats-token")
	t.Setenv("COOPMUX_TOKEN_SECRET", "coopmux-token")

	cfg := Parse()

	checks := map[string]string{
		"BeadsTokenSecret":       cfg.BeadsTokenSecret,
		"ClaudeOAuthSecret":      cfg.ClaudeOAuthSecret,
		"ClaudeOAuthTokenSecret": cfg.ClaudeOAuthTokenSecret,
		"AnthropicApiKeySecret":  cfg.AnthropicApiKeySecret,
		"GitCredentialsSecret":   cfg.GitCredentialsSecret,
		"GithubTokenSecret":      cfg.GithubTokenSecret,
		"GitlabTokenSecret":      cfg.GitlabTokenSecret,
		"RwxAccessTokenSecret":   cfg.RwxAccessTokenSecret,
		"NatsTokenSecret":        cfg.NatsTokenSecret,
		"CoopmuxTokenSecret":     cfg.CoopmuxTokenSecret,
	}
	expected := map[string]string{
		"BeadsTokenSecret":       "beads-token",
		"ClaudeOAuthSecret":      "claude-oauth",
		"ClaudeOAuthTokenSecret": "claude-token",
		"AnthropicApiKeySecret":  "anthropic-key",
		"GitCredentialsSecret":   "git-creds",
		"GithubTokenSecret":      "gh-token",
		"GitlabTokenSecret":      "gl-token",
		"RwxAccessTokenSecret":   "rwx-token",
		"NatsTokenSecret":        "nats-token",
		"CoopmuxTokenSecret":     "coopmux-token",
	}
	for name, got := range checks {
		if got != expected[name] {
			t.Errorf("%s = %q, want %q", name, got, expected[name])
		}
	}
}
