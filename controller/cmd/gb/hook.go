package main

// gb hook — Claude Code agent hook subcommands.
//
// Replaces the shell scripts that implement Claude Code hook behaviour:
//   - check-mail.sh + drain-queue.sh  →  gb hook check-mail
//   - prime.sh                         →  gb hook prime
//   - stop-gate.sh                     →  gb hook stop-gate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:     "hook",
	Short:   "Agent hook subcommands (replaces shell hook scripts)",
	GroupID: "orchestration",
}

// ── gb hook check-mail ────────────────────────────────────────────────────

var hookCheckMailCmd = &cobra.Command{
	Use:   "check-mail",
	Short: "Inject unread mail as a system-reminder (replaces check-mail.sh)",
	RunE: func(cmd *cobra.Command, args []string) error {
		me := resolveMailActor()
		if me == "" || me == "unknown" {
			return nil
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
			Types:    []string{"mail", "message"},
			Statuses: []string{"open"},
			Assignee: me,
			Sort:     "-created_at",
			Limit:    20,
		})
		if err != nil || len(result.Beads) == 0 {
			return nil
		}

		var sb strings.Builder
		sb.WriteString("## Inbox\n\n")
		for _, b := range result.Beads {
			sender := senderFromLabels(b.Labels)
			sb.WriteString(fmt.Sprintf("- %s | %s | %s\n", b.ID, b.Title, sender))
		}
		fmt.Printf("<system-reminder>\n%s</system-reminder>\n", sb.String())
		return nil
	},
}

// ── gb hook prime ─────────────────────────────────────────────────────────

var hookPrimeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Output workflow context as system-reminder (replaces prime.sh)",
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID := resolvePrimeAgentFromEnv(actor)
		outputPrimeForHook(os.Stdout, agentID)
		// Show this agent's assignment bead (BOAT_AGENT_BEAD_ID set by controller).
		beadID := os.Getenv("BOAT_AGENT_BEAD_ID")
		if beadID == "" {
			beadID, _ = resolveAgentID("")
		}
		if beadID != "" {
			out, err := exec.CommandContext(cmd.Context(), "kd", "show", beadID).Output()
			if err == nil && len(out) > 0 {
				fmt.Printf("<system-reminder>\n## Assignment\n\n%s</system-reminder>\n", out)
			}
		}
		return nil
	},
}

// ── gb hook stop-gate ─────────────────────────────────────────────────────

var hookStopGateCmd = &cobra.Command{
	Use:   "stop-gate",
	Short: "Emit Stop hook event and handle gate block (replaces stop-gate.sh)",
	RunE: func(cmd *cobra.Command, args []string) error {
		var stdinEvent map[string]any
		if err := json.NewDecoder(os.Stdin).Decode(&stdinEvent); err != nil {
			stdinEvent = map[string]any{}
		}

		claudeSessionID, _ := stdinEvent["session_id"].(string)
		cwd, _ := stdinEvent["cwd"].(string)
		if cwd == "" {
			cwd, _ = os.Getwd()
		}

		agentBeadID, _ := resolveAgentID("")
		if agentBeadID == "" {
			agentBeadID = resolveAgentByActor(cmd.Context(), actor)
		}

		resp, err := daemon.EmitHook(cmd.Context(), beadsapi.EmitHookRequest{
			AgentBeadID:     agentBeadID,
			HookType:        "Stop",
			ClaudeSessionID: claudeSessionID,
			CWD:             cwd,
			Actor:           actor,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "gb hook stop-gate: server error (failing open): %v\n", err)
			return nil
		}

		for _, w := range resp.Warnings {
			fmt.Printf("<system-reminder>%s</system-reminder>\n", w)
		}
		if resp.Inject != "" {
			fmt.Print(resp.Inject)
		}

		if resp.Block {
			blockJSON, _ := json.Marshal(map[string]string{
				"decision": "block",
				"reason":   resp.Reason,
			})
			fmt.Fprintf(os.Stderr, "%s\n", blockJSON)
			os.Exit(2)
		}

		return nil
	},
}

func init() {
	hookCmd.AddCommand(hookCheckMailCmd)
	hookCmd.AddCommand(hookPrimeCmd)
	hookCmd.AddCommand(hookStopGateCmd)
}
