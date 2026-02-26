package main

// workspace.go — gb workspace subcommand group.
//
// Provides per-bead git worktree isolation so agents work on separate branches
// without carrying over git state between beads.
//
//   gb workspace setup <bead-id>     Create worktree, branch, store metadata
//   gb workspace teardown <bead-id>  Remove worktree, clear metadata
//   gb workspace list                Show active worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// WorkspaceMeta is stored as a JSON string in Bead.Fields["workspace"].
// Keeping it as a single nested JSON blob avoids polluting the flat fields map.
type WorkspaceMeta struct {
	Branch       string `json:"branch"`
	WorktreePath string `json:"worktree_path"`
	BaseBranch   string `json:"base_branch"`
}

// jiraKeyRe matches Jira-style keys like PE-1234 or PROJ-5678.
var jiraKeyRe = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-\d+)\b`)

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Short:   "Manage per-bead git worktrees",
	GroupID: "session",
}

// ── gb workspace setup ────────────────────────────────────────────────────

var workspaceSetupCmd = &cobra.Command{
	Use:   "setup <bead-id>",
	Short: "Create a git worktree for a bead and store metadata in its fields",
	Long: `Creates a git worktree for the given bead so the agent can work on an
isolated branch. The branch name is derived from the bead title (e.g.,
"[PE-6762] Fix..." → fix/PE-6762). The base branch is auto-resolved from the
bead's dependency chain: if a dependency has workspace metadata, its branch is
used as the base.

Worktree metadata (branch, path, base) is written to the bead's fields so
downstream commands and dependent agents can find it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		beadID := args[0]
		repoPath, _ := cmd.Flags().GetString("repo")
		baseBranch, _ := cmd.Flags().GetString("base")
		branchOverride, _ := cmd.Flags().GetString("branch")
		worktreesDir, _ := cmd.Flags().GetString("dir")

		return runWorkspaceSetup(cmd.Context(), beadID, repoPath, baseBranch, branchOverride, worktreesDir)
	},
}

func runWorkspaceSetup(ctx context.Context, beadID, repoPath, baseBranch, branchOverride, worktreesDir string) error {
	// Resolve repo path.
	repoPath, err := resolveRepo(repoPath)
	if err != nil {
		return err
	}

	// Fetch bead to get title and dependencies.
	bead, err := daemon.GetBead(ctx, beadID)
	if err != nil {
		return fmt.Errorf("fetching bead %s: %w", beadID, err)
	}

	// Check if already set up.
	if meta := parseWorkspaceMeta(bead.Fields["workspace"]); meta != nil {
		fmt.Printf("Workspace already set up for %s\n", beadID)
		fmt.Printf("  branch:  %s\n", meta.Branch)
		fmt.Printf("  path:    %s\n", meta.WorktreePath)
		fmt.Printf("  base:    %s\n", meta.BaseBranch)
		return nil
	}

	// Resolve branch name.
	branch := branchOverride
	if branch == "" {
		branch = branchFromTitle(beadID, bead.Title)
	}

	// Resolve base branch.
	if baseBranch == "" {
		baseBranch = resolveBaseBranch(ctx, beadID)
	}

	// Determine worktrees directory.
	if worktreesDir == "" {
		worktreesDir = defaultWorktreesDir()
	}
	worktreePath := filepath.Join(worktreesDir, sanitizeDirName(beadID))

	// Create worktree.
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return fmt.Errorf("creating worktrees dir %s: %w", worktreesDir, err)
	}

	if err := gitWorktreeAdd(repoPath, worktreePath, branch, baseBranch); err != nil {
		return fmt.Errorf("git worktree add: %w", err)
	}

	// Store metadata in bead fields.
	meta := WorkspaceMeta{
		Branch:       branch,
		WorktreePath: worktreePath,
		BaseBranch:   baseBranch,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshalling workspace metadata: %w", err)
	}
	if err := daemon.UpdateBeadFields(ctx, beadID, map[string]string{
		"workspace": string(metaJSON),
	}); err != nil {
		// Non-fatal: worktree was created; warn but continue.
		fmt.Fprintf(os.Stderr, "Warning: failed to persist workspace metadata to bead %s: %v\n", beadID, err)
	}

	fmt.Printf("Worktree set up for %s\n", beadID)
	fmt.Printf("  branch:  %s\n", branch)
	fmt.Printf("  path:    %s\n", worktreePath)
	fmt.Printf("  base:    %s\n", baseBranch)
	fmt.Printf("\ncd %s\n", worktreePath)
	return nil
}

// ── gb workspace teardown ─────────────────────────────────────────────────

var workspaceTeardownCmd = &cobra.Command{
	Use:   "teardown <bead-id>",
	Short: "Remove the git worktree for a bead",
	Long: `Removes the git worktree associated with a bead and clears the workspace
metadata from the bead's fields. Run after the bead's MR is merged.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		beadID := args[0]
		repoPath, _ := cmd.Flags().GetString("repo")
		force, _ := cmd.Flags().GetBool("force")

		return runWorkspaceTeardown(cmd.Context(), beadID, repoPath, force)
	},
}

func runWorkspaceTeardown(ctx context.Context, beadID, repoPath string, force bool) error {
	// Fetch bead to get workspace metadata.
	bead, err := daemon.GetBead(ctx, beadID)
	if err != nil {
		return fmt.Errorf("fetching bead %s: %w", beadID, err)
	}

	meta := parseWorkspaceMeta(bead.Fields["workspace"])
	if meta == nil {
		fmt.Printf("No workspace found for %s\n", beadID)
		return nil
	}

	// Resolve repo path.
	repoPath, err = resolveRepo(repoPath)
	if err != nil {
		return err
	}

	// Remove worktree.
	if err := gitWorktreeRemove(repoPath, meta.WorktreePath, force); err != nil {
		if !force {
			return fmt.Errorf("git worktree remove (use --force to override): %w", err)
		}
		fmt.Fprintf(os.Stderr, "Warning: git worktree remove failed (continuing): %v\n", err)
	}

	// Clear workspace metadata from bead.
	if err := daemon.UpdateBeadFields(ctx, beadID, map[string]string{
		"workspace": "",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear workspace metadata from bead %s: %v\n", beadID, err)
	}

	fmt.Printf("Worktree torn down for %s\n", beadID)
	fmt.Printf("  path:    %s\n", meta.WorktreePath)
	fmt.Printf("  branch:  %s\n", meta.Branch)
	return nil
}

// ── gb workspace list ─────────────────────────────────────────────────────

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show active worktrees",
	Long:  `Lists all worktrees in the default worktrees directory.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		worktreesDir, _ := cmd.Flags().GetString("dir")
		repoPath, _ := cmd.Flags().GetString("repo")
		return runWorkspaceList(repoPath, worktreesDir)
	},
}

func runWorkspaceList(repoPath, worktreesDir string) error {
	if worktreesDir == "" {
		worktreesDir = defaultWorktreesDir()
	}

	// Use git worktree list for authoritative list.
	repoPath, err := resolveRepo(repoPath)
	if err != nil {
		// Fall back to directory scan if no repo.
		return listWorktreesByDir(worktreesDir)
	}

	out, err := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return listWorktreesByDir(worktreesDir)
	}

	// Parse porcelain output.
	type wtEntry struct {
		path   string
		branch string
		bare   bool
	}
	var entries []wtEntry
	var current wtEntry
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			if current.path != "" {
				entries = append(entries, current)
			}
			current = wtEntry{}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			current.path = rest
		} else if rest, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			current.branch = rest
		} else if line == "bare" {
			current.bare = true
		}
	}
	if current.path != "" {
		entries = append(entries, current)
	}

	if len(entries) == 0 {
		fmt.Println("No worktrees found.")
		return nil
	}

	// Filter to worktrees within our managed dir (skip the main worktree).
	var managed []wtEntry
	absDir, _ := filepath.Abs(worktreesDir)
	for _, e := range entries {
		abs, _ := filepath.Abs(e.path)
		if strings.HasPrefix(abs, absDir+string(os.PathSeparator)) {
			managed = append(managed, e)
		}
	}

	if len(managed) == 0 {
		fmt.Printf("No managed worktrees in %s\n", worktreesDir)
		fmt.Printf("(Total git worktrees: %d)\n", len(entries))
		return nil
	}

	fmt.Printf("Managed worktrees in %s:\n\n", worktreesDir)
	for _, e := range managed {
		fmt.Printf("  %-30s  %s\n", filepath.Base(e.path), e.branch)
	}
	return nil
}

func listWorktreesByDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No worktrees directory at %s\n", dir)
			return nil
		}
		return fmt.Errorf("reading worktrees dir: %w", err)
	}

	if len(entries) == 0 {
		fmt.Printf("No worktrees in %s\n", dir)
		return nil
	}

	fmt.Printf("Worktrees in %s:\n\n", dir)
	for _, e := range entries {
		if e.IsDir() {
			fmt.Printf("  %s\n", e.Name())
		}
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────

// defaultWorktreesDir returns the default directory for managed worktrees.
// Priority: $KD_WORKSPACE/.beads/worktrees > $HOME/.beads/worktrees
func defaultWorktreesDir() string {
	if ws := os.Getenv("KD_WORKSPACE"); ws != "" {
		return filepath.Join(ws, ".beads", "worktrees")
	}
	return filepath.Join(homeDir(), ".beads", "worktrees")
}

// resolveRepo finds a git repo root from the given path (or cwd if empty).
// Returns an error if the path is not inside a git repo.
func resolveRepo(path string) (string, error) {
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not in a git repository", path)
	}
	return strings.TrimSpace(string(out)), nil
}

// branchFromTitle derives a branch name from a bead title and ID.
// "[PE-6762] Fix server-side filtering" → "fix/PE-6762"
// "Some other title" (no Jira key) → "fix/<bead-id>"
func branchFromTitle(beadID, title string) string {
	if m := jiraKeyRe.FindStringSubmatch(title); m != nil {
		return "fix/" + m[1]
	}
	// Fall back to sanitized bead ID.
	return "fix/" + sanitizeDirName(beadID)
}

// sanitizeDirName converts a bead ID to a safe directory name.
func sanitizeDirName(s string) string {
	// bead IDs are already URL-safe (e.g., "kd-9A6R6dx6bV"), but normalise just in case.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// resolveBaseBranch walks the bead's dependency chain to find a suitable base.
// Returns the branch of the first dependency that has workspace metadata,
// or "origin/main" as fallback.
func resolveBaseBranch(ctx context.Context, beadID string) string {
	deps, err := daemon.GetDependencies(ctx, beadID)
	if err != nil || len(deps) == 0 {
		return "origin/main"
	}

	for _, dep := range deps {
		// We depend on dep.DependsOnID — fetch it and check for workspace metadata.
		parent, err := daemon.GetBead(ctx, dep.DependsOnID)
		if err != nil {
			continue
		}
		if meta := parseWorkspaceMeta(parent.Fields["workspace"]); meta != nil && meta.Branch != "" {
			return meta.Branch
		}
	}
	return "origin/main"
}

// parseWorkspaceMeta decodes the JSON workspace metadata from a bead field value.
// Returns nil if the value is empty or invalid.
func parseWorkspaceMeta(raw string) *WorkspaceMeta {
	if raw == "" {
		return nil
	}
	var meta WorkspaceMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil
	}
	if meta.Branch == "" && meta.WorktreePath == "" {
		return nil
	}
	return &meta
}

// gitWorktreeAdd runs git worktree add to create a new worktree at path with
// a new branch checked out at baseBranch.
func gitWorktreeAdd(repoPath, worktreePath, branch, baseBranch string) error {
	// Check if branch already exists locally.
	existsErr := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	if existsErr == nil {
		// Branch exists: check it out without -b.
		cmd := exec.Command("git", "-C", repoPath, "worktree", "add", worktreePath, branch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Branch does not exist: create it from baseBranch.
	// First fetch the base if it looks remote.
	if remote, ok := strings.CutPrefix(baseBranch, "origin/"); ok {
		fetchCmd := exec.Command("git", "-C", repoPath, "fetch", "origin", remote)
		fetchCmd.Stderr = os.Stderr
		_ = fetchCmd.Run() // best-effort
	}

	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", "-b", branch, worktreePath, baseBranch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// gitWorktreeRemove runs git worktree remove.
func gitWorktreeRemove(repoPath, worktreePath string, force bool) error {
	args := []string{"-C", repoPath, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── init ──────────────────────────────────────────────────────────────────

func init() {
	// setup flags
	workspaceSetupCmd.Flags().String("repo", "", "git repository path (default: cwd)")
	workspaceSetupCmd.Flags().String("base", "", "base branch for new worktree (default: auto-resolved from deps or origin/main)")
	workspaceSetupCmd.Flags().String("branch", "", "branch name override (default: derived from bead title)")
	workspaceSetupCmd.Flags().String("dir", "", "worktrees directory (default: $KD_WORKSPACE/.beads/worktrees)")

	// teardown flags
	workspaceTeardownCmd.Flags().String("repo", "", "git repository path (default: cwd)")
	workspaceTeardownCmd.Flags().Bool("force", false, "force removal even if worktree is dirty")
	workspaceTeardownCmd.Flags().String("dir", "", "worktrees directory (default: $KD_WORKSPACE/.beads/worktrees)")

	// list flags
	workspaceListCmd.Flags().String("repo", "", "git repository path (default: cwd)")
	workspaceListCmd.Flags().String("dir", "", "worktrees directory (default: $KD_WORKSPACE/.beads/worktrees)")

	workspaceCmd.AddCommand(workspaceSetupCmd)
	workspaceCmd.AddCommand(workspaceTeardownCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
}
