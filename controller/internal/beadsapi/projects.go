package beadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SecretEntry maps a K8s Secret key to a pod environment variable.
// Used in per-project secret overrides on project beads.
type SecretEntry struct {
	Env    string `json:"env"`    // env var name in the pod
	Secret string `json:"secret"` // K8s Secret name
	Key    string `json:"key"`    // key within the Secret
}

// EnvEntry maps a plain environment variable name to a value.
// Used for non-secret configuration on project beads.
type EnvEntry struct {
	Name  string `json:"name"`  // env var name in the pod
	Value string `json:"value"` // plain text value
}

// RepoEntry declares a repository to clone into the agent workspace.
type RepoEntry struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	Role   string `json:"role,omitempty"` // "primary" or "reference"
	Name   string `json:"name,omitempty"`
}

// ProjectInfo represents a registered project from daemon project beads.
type ProjectInfo struct {
	Name           string // Project name (from bead title)
	Prefix         string // Beads prefix (e.g., "kd", "bot")
	GitURL         string // Repository URL
	DefaultBranch  string // Default branch (e.g., "main")
	Image          string // Per-project agent image override
	StorageClass   string // Per-project PVC storage class override
	ServiceAccount string // Per-project K8s ServiceAccount override
	RTKEnabled     bool   // Enable RTK token optimization for this project

	// Tier 1 enhancements: per-project pod resource overrides.
	CPURequest    string // Kubernetes quantity string, e.g. "500m"
	CPULimit      string // Kubernetes quantity string, e.g. "2000m"
	MemoryRequest string // Kubernetes quantity string, e.g. "512Mi"
	MemoryLimit   string // Kubernetes quantity string, e.g. "2Gi"

	// SlackChannels is a list of Slack channel IDs associated with this project.
	// Used to infer the default project when /spawn is invoked from a channel.
	// Populated from the comma-separated "slack_channel" field on project beads.
	SlackChannels []string

	// ChannelRoles maps Slack channel IDs to agent roles.
	// When an agent is spawned from a thread in a mapped channel, it receives
	// the specified role instead of the default "thread" role.
	// Populated from the "channel_roles" JSON field on project beads.
	ChannelRoles map[string]string

	// EnvOverrides holds extra env vars parsed from the env_json bead field.
	// Keys absent or empty in the JSON are silently skipped.
	EnvOverrides map[string]string

	Secrets        []SecretEntry       // Per-project secret overrides
	EnvVars        []EnvEntry          // Per-project plain env vars
	Repos          []RepoEntry         // Multi-repo definitions
	PrewarmedPool  *PrewarmedPoolConfig // Per-project prewarmed agent pool config (nil = disabled)
}

// PrewarmedPoolConfig holds per-project prewarmed agent pool settings.
// Stored as the "prewarmed_pool" JSON field on project beads.
type PrewarmedPoolConfig struct {
	Enabled bool   `json:"enabled"`
	MinSize int    `json:"min_size"`
	MaxSize int    `json:"max_size"`
	Role    string `json:"role"`
	Mode    string `json:"mode"`
}

// ListProjectBeads queries the daemon for project beads (type=project) and extracts
// project metadata from fields. Returns a map of project name -> ProjectInfo.
func (c *Client) ListProjectBeads(ctx context.Context) (map[string]ProjectInfo, error) {
	resp, err := c.listBeads(ctx, []string{"project"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing project beads: %w", err)
	}

	projects := make(map[string]ProjectInfo)
	for _, b := range resp.Beads {
		// Strip "Project: " prefix from title -- legacy project beads may have titles
		// like "Project: beads" instead of just "beads".
		name := strings.TrimPrefix(b.Title, "Project: ")
		fields := b.fieldsMap()
		info := ProjectInfo{
			Name:           name,
			Prefix:         fields["prefix"],
			GitURL:         fields["git_url"],
			DefaultBranch:  fields["default_branch"],
			Image:          fields["image"],
			StorageClass:   fields["storage_class"],
			ServiceAccount: fields["service_account"],
			RTKEnabled:     fields["rtk_enabled"] == "true",
			CPURequest:     fields["cpu_request"],
			CPULimit:       fields["cpu_limit"],
			MemoryRequest:  fields["memory_request"],
			MemoryLimit:    fields["memory_limit"],
			SlackChannels:  parseSlackChannels(fields["slack_channel"]),
		}
		// Parse channel-to-role mapping from JSON field.
		if raw := fields["channel_roles"]; raw != "" {
			var channelRoles map[string]string
			if json.Unmarshal([]byte(raw), &channelRoles) == nil {
				info.ChannelRoles = channelRoles
			}
		}
		// Parse per-project secrets from JSON field.
		if raw := fields["secrets"]; raw != "" {
			var secrets []SecretEntry
			if json.Unmarshal([]byte(raw), &secrets) == nil {
				info.Secrets = secrets
			}
		}
		// Parse per-project plain env vars from JSON field.
		if raw := fields["env"]; raw != "" {
			var envVars []EnvEntry
			if json.Unmarshal([]byte(raw), &envVars) == nil {
				info.EnvVars = envVars
			}
		}
		// Parse multi-repo definitions from JSON field.
		if raw := fields["repos"]; raw != "" {
			var repos []RepoEntry
			if json.Unmarshal([]byte(raw), &repos) == nil {
				info.Repos = repos
			}
		}
		// Parse prewarmed pool config from JSON field.
		if raw := fields["prewarmed_pool"]; raw != "" {
			var poolCfg PrewarmedPoolConfig
			if json.Unmarshal([]byte(raw), &poolCfg) == nil && poolCfg.Enabled {
				if poolCfg.MinSize <= 0 {
					poolCfg.MinSize = 2
				}
				if poolCfg.MaxSize <= 0 {
					poolCfg.MaxSize = 5
				}
				if poolCfg.Role == "" {
					poolCfg.Role = "thread"
				}
				if poolCfg.Mode == "" {
					poolCfg.Mode = "crew"
				}
				info.PrewarmedPool = &poolCfg
			}
		}
		// Parse env_json field.
		if raw := fields["env_json"]; raw != "" {
			var envMap map[string]string
			if err := json.Unmarshal([]byte(raw), &envMap); err != nil {
				// Log and skip malformed env_json rather than failing the whole refresh.
				_ = fmt.Errorf("project %q: malformed env_json (skipped): %w", name, err)
			} else {
				info.EnvOverrides = envMap
			}
		}
		if name != "" {
			projects[name] = info
		}
	}

	return projects, nil
}

// parseSlackChannels splits a comma-separated channel ID string into a slice.
// Handles single values ("C123") and multi-values ("C123,C456, C789").
// Returns nil for empty input.
func parseSlackChannels(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	channels := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			channels = append(channels, p)
		}
	}
	if len(channels) == 0 {
		return nil
	}
	return channels
}

// RoleForChannel returns the role mapped to a specific Slack channel, or empty string
// if no mapping exists. Used during thread-spawn to assign channel-specific roles.
func (p ProjectInfo) RoleForChannel(channelID string) string {
	if role, ok := p.ChannelRoles[channelID]; ok {
		return role
	}
	return ""
}

// HasChannel reports whether the project is associated with the given Slack channel.
func (p ProjectInfo) HasChannel(channelID string) bool {
	for _, ch := range p.SlackChannels {
		if ch == channelID {
			return true
		}
	}
	return false
}
