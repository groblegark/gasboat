package main

// gb session — first-class session management.
//
// Sessions are tracked as beads (type=session) that record each coop session's
// lifecycle: start time, end time, exit code, session log path, and the linked
// agent bead. This replaces the previous approach where sessions were just JSONL
// files on disk with no metadata or tracking.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:     "session",
	Short:   "Manage agent sessions",
	GroupID: "session",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sessions for the current agent or all agents",
	RunE:  runSessionList,
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <session-id>",
	Short: "Show details of a session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionShow,
}

var sessionRegisterCmd = &cobra.Command{
	Use:    "register",
	Short:  "Register a new session (called internally by the restart loop)",
	Hidden: true,
	RunE:   runSessionRegister,
}

var sessionCloseCmd = &cobra.Command{
	Use:    "close <session-id>",
	Short:  "Close a session bead (called internally)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runSessionClose,
}

var (
	sessionAgent    string
	sessionLimit    int
	sessionAll      bool
	sessionExitCode int
	sessionLog      string
)

func init() {
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionShowCmd)
	sessionCmd.AddCommand(sessionRegisterCmd)
	sessionCmd.AddCommand(sessionCloseCmd)

	sessionListCmd.Flags().StringVar(&sessionAgent, "agent", "", "filter by agent name (default: current agent)")
	sessionListCmd.Flags().IntVar(&sessionLimit, "limit", 20, "max number of sessions to show")
	sessionListCmd.Flags().BoolVar(&sessionAll, "all", false, "show sessions for all agents")

	sessionRegisterCmd.Flags().StringVar(&sessionLog, "log", "", "path to session JSONL log")

	sessionCloseCmd.Flags().IntVar(&sessionExitCode, "exit-code", 0, "exit code of the session")
	sessionCloseCmd.Flags().StringVar(&sessionLog, "log", "", "path to session JSONL log")
}

func runSessionList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	agentFilter := sessionAgent
	if !sessionAll && agentFilter == "" {
		agentFilter = os.Getenv("BOAT_AGENT")
	}

	sessions, err := daemon.ListSessionBeads(ctx, agentFilter, sessionLimit)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sessions)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tAGENT\tSTATUS\tSTARTED\tDURATION\tEXIT\n")
	for _, s := range sessions {
		agent := s.Fields["agent"]
		status := s.Fields["status"]
		startedAt := s.Fields["started_at"]
		endedAt := s.Fields["ended_at"]
		exitCode := s.Fields["exit_code"]

		duration := "-"
		if startedAt != "" && endedAt != "" {
			if st, err := time.Parse(time.RFC3339, startedAt); err == nil {
				if et, err := time.Parse(time.RFC3339, endedAt); err == nil {
					duration = et.Sub(st).Round(time.Second).String()
				}
			}
		} else if startedAt != "" && status == "active" {
			if st, err := time.Parse(time.RFC3339, startedAt); err == nil {
				duration = time.Since(st).Round(time.Second).String() + " (running)"
			}
		}

		startShort := ""
		if startedAt != "" {
			if t, err := time.Parse(time.RFC3339, startedAt); err == nil {
				startShort = t.Format("01-02 15:04")
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, agent, status, startShort, duration, exitCode)
	}
	return w.Flush()
}

func runSessionShow(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	sessionID := args[0]

	bead, err := daemon.GetBead(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("getting session %s: %w", sessionID, err)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(bead)
	}

	fmt.Printf("ID:           %s\n", bead.ID)
	fmt.Printf("Title:        %s\n", bead.Title)
	fmt.Printf("Status:       %s\n", bead.Fields["status"])
	fmt.Printf("Agent:        %s\n", bead.Fields["agent"])
	fmt.Printf("Agent Bead:   %s\n", bead.Fields["agent_bead_id"])
	fmt.Printf("Project:      %s\n", bead.Fields["project"])
	fmt.Printf("Role:         %s\n", bead.Fields["role"])
	fmt.Printf("Hostname:     %s\n", bead.Fields["hostname"])
	fmt.Printf("Started At:   %s\n", bead.Fields["started_at"])
	fmt.Printf("Ended At:     %s\n", bead.Fields["ended_at"])
	fmt.Printf("Exit Code:    %s\n", bead.Fields["exit_code"])
	fmt.Printf("Resumed:      %s\n", bead.Fields["resumed"])
	fmt.Printf("Session Log:  %s\n", bead.Fields["session_log"])

	if bead.Fields["started_at"] != "" && bead.Fields["ended_at"] != "" {
		if st, err := time.Parse(time.RFC3339, bead.Fields["started_at"]); err == nil {
			if et, err := time.Parse(time.RFC3339, bead.Fields["ended_at"]); err == nil {
				fmt.Printf("Duration:     %s\n", et.Sub(st).Round(time.Second))
			}
		}
	}

	return nil
}

func runSessionRegister(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	agentName := os.Getenv("BOAT_AGENT")
	agentBeadID := envOr("KD_AGENT_ID", os.Getenv("BOAT_AGENT_BEAD_ID"))
	project := defaultProject()
	role := envOr("BOAT_ROLE", "unknown")
	hostname, _ := os.Hostname()

	if agentName == "" {
		return fmt.Errorf("BOAT_AGENT not set")
	}

	fields := beadsapi.SessionFields{
		Agent:       agentName,
		AgentBeadID: agentBeadID,
		Project:     project,
		Role:        role,
		Hostname:    hostname,
		SessionLog:  sessionLog,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		Resumed:     sessionLog != "",
		Status:      "active",
	}

	id, err := daemon.RegisterSession(ctx, fields)
	if err != nil {
		return fmt.Errorf("registering session: %w", err)
	}

	fmt.Println(id)
	return nil
}

func runSessionClose(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	sessionID := args[0]

	if err := daemon.CloseSession(ctx, sessionID, sessionExitCode, sessionLog); err != nil {
		return fmt.Errorf("closing session %s: %w", sessionID, err)
	}

	fmt.Printf("Session %s closed (exit code: %d).\n", sessionID, sessionExitCode)
	return nil
}

// registerSessionBead creates a session bead for the current coop session.
// Called from the restart loop in agent_start_k8s.go.
func registerSessionBead(ctx context.Context, cfg k8sConfig, resumeLog string) string {
	if daemon == nil {
		return ""
	}

	agentBeadID := envOr("KD_AGENT_ID", os.Getenv("BOAT_AGENT_BEAD_ID"))
	hostname, _ := os.Hostname()

	fields := beadsapi.SessionFields{
		Agent:       cfg.agent,
		AgentBeadID: agentBeadID,
		Project:     cfg.project,
		Role:        cfg.role,
		Hostname:    hostname,
		SessionLog:  resumeLog,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		Resumed:     resumeLog != "",
		Status:      "active",
	}

	regCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	id, err := daemon.RegisterSession(regCtx, fields)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gb agent start] warning: could not register session bead: %v\n", err)
		return ""
	}
	fmt.Printf("[gb agent start] session bead registered: %s\n", id)
	return id
}

// closeSessionBead closes the session bead after coop exits.
func closeSessionBead(ctx context.Context, sessionBeadID string, exitCode int, resumeLog string) {
	if daemon == nil || sessionBeadID == "" {
		return
	}

	// Try to find the actual session log path if we started fresh.
	logPath := resumeLog
	if logPath == "" {
		logPath = findLatestSessionLog()
	}

	closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := daemon.CloseSession(closeCtx, sessionBeadID, exitCode, logPath); err != nil {
		fmt.Fprintf(os.Stderr, "[gb agent start] warning: could not close session bead %s: %v\n", sessionBeadID, err)
	} else {
		fmt.Printf("[gb agent start] session bead closed: %s (exit code: %d)\n", sessionBeadID, exitCode)
	}
}

// findLatestSessionLog returns the path of the most recently modified .jsonl
// file under ~/.claude/projects/ (the session log coop just wrote).
func findLatestSessionLog() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	projectsDir := homeDir + "/.claude/projects"

	var bestPath string
	var bestTime time.Time

	_ = walkSessionLogs(projectsDir, func(path string, modTime time.Time) {
		if modTime.After(bestTime) {
			bestPath = path
			bestTime = modTime
		}
	})

	return bestPath
}

// walkSessionLogs walks the projects directory for .jsonl files,
// excluding subagent sessions.
func walkSessionLogs(projectsDir string, fn func(path string, modTime time.Time)) error {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subDir := projectsDir + "/" + entry.Name()
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if se.IsDir() {
				continue
			}
			name := se.Name()
			if !strings.HasSuffix(name, ".jsonl") || strings.Contains(name, "subagent") {
				return nil
			}
			info, err := se.Info()
			if err != nil {
				continue
			}
			fn(subDir+"/"+name, info.ModTime())
		}
	}
	return nil
}
