package main

// gb agent start-mock --scenario=<name>
//
// Creates an agent bead with mock_scenario metadata so the reconciler
// schedules a pod running claudeless instead of Claude Code.

import (
	"encoding/json"
	"fmt"
	"os"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var agentStartMockCmd = &cobra.Command{
	Use:   "start-mock",
	Short: "Create a mock agent bead (runs claudeless instead of Claude)",
	Long: `Create an agent bead with mock_scenario metadata. The reconciler
picks up the bead and schedules a pod that runs claudeless with the
given scenario file instead of Claude Code.

  --scenario       Scenario name (required). Maps to /scenarios/<name>.toml
                   inside the agent container.
  --scenario-configmap  K8s ConfigMap name to mount at /etc/agent-pod.
                   Use this to supply scenario TOML files to the pod.

Example:

  gb agent start-mock --scenario=hello --name=mock-bot --project=gasboat`,
	RunE: runAgentStartMock,
}

func init() {
	agentCmd.AddCommand(agentStartMockCmd)

	agentStartMockCmd.Flags().String("scenario", "", "Scenario name (required, maps to /scenarios/<name>.toml)")
	agentStartMockCmd.Flags().String("scenario-configmap", "", "ConfigMap name to mount scenario files")
	agentStartMockCmd.Flags().String("name", "", "Agent name (default: mock-<scenario>)")
	agentStartMockCmd.Flags().String("role", "crew", "Agent role (e.g. crew, captain, job)")
	agentStartMockCmd.Flags().String("project", "", "Project name (default: $BOAT_PROJECT)")

	_ = agentStartMockCmd.MarkFlagRequired("scenario")
}

func runAgentStartMock(cmd *cobra.Command, args []string) error {
	scenario, _ := cmd.Flags().GetString("scenario")
	configmap, _ := cmd.Flags().GetString("scenario-configmap")
	agentName, _ := cmd.Flags().GetString("name")
	role, _ := cmd.Flags().GetString("role")
	project, _ := cmd.Flags().GetString("project")

	if project == "" {
		project = os.Getenv("BOAT_PROJECT")
	}

	if agentName == "" {
		agentName = "mock-" + scenario
	}

	// Build agent fields with mock_scenario.
	fields := map[string]string{
		"agent":         agentName,
		"mode":          "crew",
		"role":          role,
		"project":       project,
		"mock_scenario": scenario,
	}
	if configmap != "" {
		fields["configmap"] = configmap
	}

	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshalling fields: %w", err)
	}

	ctx := cmd.Context()

	id, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:     agentName,
		Type:      "agent",
		CreatedBy: actor,
		Fields:    fieldsJSON,
	})
	if err != nil {
		return fmt.Errorf("creating mock agent bead: %w", err)
	}

	// Best-effort labels for project and role filtering.
	if project != "" {
		_ = daemon.AddLabel(ctx, id, "project:"+project)
	}
	_ = daemon.AddLabel(ctx, id, "role:"+role)
	_ = daemon.AddLabel(ctx, id, "mock:"+scenario)

	if jsonOutput {
		printJSON(map[string]string{
			"id":       id,
			"name":     agentName,
			"scenario": scenario,
		})
		return nil
	}

	fmt.Printf("Mock agent bead created: %s\n", id)
	fmt.Printf("  Name:     %s\n", agentName)
	fmt.Printf("  Scenario: %s\n", scenario)
	if configmap != "" {
		fmt.Printf("  ConfigMap: %s\n", configmap)
	}
	fmt.Printf("\nThe reconciler will schedule a pod running claudeless --scenario /scenarios/%s.toml\n", scenario)

	return nil
}
