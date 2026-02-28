package podmanager

import (
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "main", "'main'"},
		{"empty", "", "''"},
		{"with spaces", "my project", "'my project'"},
		{"with single quote", "it's", `'it'\''s'`},
		{"with double quote", `say "hello"`, `'say "hello"'`},
		{"with backtick", "foo`bar`", "'foo`bar`'"},
		{"with dollar", "foo$bar", "'foo$bar'"},
		{"with semicolon", "foo; rm -rf /", "'foo; rm -rf /'"},
		{"with newline", "foo\nbar", "'foo\nbar'"},
		{"with backslash", `foo\bar`, `'foo\bar'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.in)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildInitCloneContainer_NilWhenNoSources(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:   "test",
		AgentName: "agent",
	}
	if c := m.buildInitCloneContainer(spec); c != nil {
		t.Error("expected nil container when no GitURL or ReferenceRepos")
	}
}

func TestBuildInitCloneContainer_QuotesProjectName(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:   "my project",
		Mode:      "crew",
		Role:      "dev",
		AgentName: "agent-1",
		GitURL:    "https://github.com/example/repo.git",
	}
	c := m.buildInitCloneContainer(spec)
	if c == nil {
		t.Fatal("expected non-nil container")
	}

	script := c.Command[2] // /bin/sh -c <script>
	// The project name should be single-quoted to prevent shell word splitting.
	if !strings.Contains(script, "'my project'") {
		t.Errorf("script does not contain quoted project name:\n%s", script)
	}
}

func TestBuildInitCloneContainer_QuotesMaliciousURL(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:   "test",
		Mode:      "crew",
		Role:      "dev",
		AgentName: "agent",
		GitURL:    "https://evil.com/repo.git'; echo pwned; '",
	}
	c := m.buildInitCloneContainer(spec)
	if c == nil {
		t.Fatal("expected non-nil container")
	}

	script := c.Command[2]
	// The URL with embedded shell commands should be safely quoted.
	// It should NOT contain unquoted "echo pwned".
	if strings.Contains(script, "echo pwned") && !strings.Contains(script, `'\''echo pwned`) {
		// Check that "echo pwned" only appears inside quotes
		if !strings.Contains(script, `'; echo pwned; '`) {
			t.Errorf("script may be vulnerable to injection:\n%s", script)
		}
	}
}

func TestBuildInitCloneContainer_DefaultBranch(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:   "test",
		Mode:      "crew",
		Role:      "dev",
		AgentName: "agent",
		GitURL:    "https://github.com/example/repo.git",
	}
	c := m.buildInitCloneContainer(spec)
	script := c.Command[2]
	// Default branch should be "main", quoted.
	if !strings.Contains(script, "git checkout 'main'") {
		t.Errorf("script does not checkout default branch 'main':\n%s", script)
	}
}

func TestBuildInitCloneContainer_CustomBranch(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:          "test",
		Mode:             "crew",
		Role:             "dev",
		AgentName:        "agent",
		GitURL:           "https://github.com/example/repo.git",
		GitDefaultBranch: "develop",
	}
	c := m.buildInitCloneContainer(spec)
	script := c.Command[2]
	if !strings.Contains(script, "git checkout 'develop'") {
		t.Errorf("script does not use custom branch:\n%s", script)
	}
}

func TestBuildInitCloneContainer_ReferenceRepos(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:   "test",
		Mode:      "crew",
		Role:      "dev",
		AgentName: "agent",
		ReferenceRepos: []RepoRef{
			{URL: "https://github.com/ref/repo.git", Name: "ref-repo", Branch: "v2"},
		},
	}
	c := m.buildInitCloneContainer(spec)
	if c == nil {
		t.Fatal("expected non-nil container for reference repos")
	}
	script := c.Command[2]
	if !strings.Contains(script, "'ref-repo'") {
		t.Errorf("script does not contain quoted ref name:\n%s", script)
	}
	if !strings.Contains(script, "'v2'") {
		t.Errorf("script does not contain quoted ref branch:\n%s", script)
	}
}

func TestBuildInitCloneContainer_CredentialSetup(t *testing.T) {
	m := &K8sManager{}
	spec := AgentPodSpec{
		Project:              "test",
		Mode:                 "crew",
		Role:                 "dev",
		AgentName:            "agent",
		GitURL:               "https://github.com/example/repo.git",
		GitCredentialsSecret: "git-creds",
	}
	c := m.buildInitCloneContainer(spec)
	script := c.Command[2]
	if !strings.Contains(script, "credential.helper") {
		t.Errorf("script missing credential helper setup:\n%s", script)
	}

	// Verify env vars include GIT_USERNAME and GIT_TOKEN.
	envNames := map[string]bool{}
	for _, e := range c.Env {
		envNames[e.Name] = true
	}
	if !envNames["GIT_USERNAME"] || !envNames["GIT_TOKEN"] {
		t.Errorf("missing git credential env vars, got: %v", envNames)
	}
}
