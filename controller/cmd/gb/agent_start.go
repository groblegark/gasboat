package main

// gb agent start --local / --docker
//
// Spawns a local coop+claude agent session that is fully registered with the
// kbeads server and coopmux — indistinguishable from a K8s agent.
//
// --local  Run claude directly on the host inside a coop session. Attached.
// --docker Build the gasboat agent image locally and run in a container.
//          Mounts $PWD into /home/agent/workspace. Detachable.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var agentStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a local agent session (--local or --docker)",
	Long: `Start a coop-based agent session registered with the kbeads server.

  --local   Run claude on the host via coop. Attached (ctrl-C to stop).
  --docker  Build the gasboat agent image and run in a container.
            Mounts $PWD into /home/agent/workspace. Detachable by default.

The agent is registered with the kbeads server on startup and appears in
'gb agent roster'. It is deregistered and its bead closed on exit.`,
	RunE: runAgentStart,
}

func init() {
	agentCmd.AddCommand(agentStartCmd)

	agentStartCmd.Flags().Bool("k8s", false, "Run as K8s pod entrypoint (replaces entrypoint.sh)")
	agentStartCmd.Flags().Bool("local", false, "Run agent on the host via coop (attached)")
	agentStartCmd.Flags().Bool("docker", false, "Run agent in a Docker container")
	agentStartCmd.Flags().String("name", "", "Agent name (default: derived from hostname)")
	agentStartCmd.Flags().String("role", "crew", "Agent role (e.g. crew, captain)")
	agentStartCmd.Flags().String("dir", "", "Working directory (default: $PWD)")
	agentStartCmd.Flags().String("image-dir", "", "Path to gasboat agent Dockerfile dir (--docker only)")
	agentStartCmd.Flags().String("image", "", "Pre-built image name to skip rebuild (--docker only)")
	agentStartCmd.Flags().Bool("attach", false, "Follow container output (--docker only; --local is always attached)")
	agentStartCmd.Flags().String("command", "", "Command to run (default: claude --dangerously-skip-permissions)")

	// K8s-only flags.
	agentStartCmd.Flags().String("workspace", "/home/agent/workspace", "Workspace path (--k8s only)")
	agentStartCmd.Flags().Int("coop-port", 8080, "Coop HTTP port (--k8s only)")
	agentStartCmd.Flags().Int("coop-health-port", 9090, "Coop health port (--k8s only)")
	agentStartCmd.Flags().Int("max-restarts", 0, "Max consecutive restarts — 0 uses COOP_MAX_RESTARTS env or 10 (--k8s only)")
}

func runAgentStart(cmd *cobra.Command, args []string) error {
	isK8s, _ := cmd.Flags().GetBool("k8s")
	if isK8s {
		return runAgentStartK8s(cmd, args)
	}

	isLocal, _ := cmd.Flags().GetBool("local")
	isDocker, _ := cmd.Flags().GetBool("docker")

	if isLocal == isDocker {
		return fmt.Errorf("specify exactly one of --local, --docker, or --k8s")
	}

	agentName, _ := cmd.Flags().GetString("name")
	if agentName == "" {
		h, _ := os.Hostname()
		agentName = "local-" + h
	}
	role, _ := cmd.Flags().GetString("role")
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
	}
	dir, _ = filepath.Abs(dir)

	agentCommand, _ := cmd.Flags().GetString("command")
	if agentCommand == "" {
		agentCommand = "claude --dangerously-skip-permissions"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 1. Create agent bead.
	fmt.Printf("[gb agent start] Registering agent '%s' with server...\n", agentName)
	agentBeadID, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:     agentName,
		Type:      "task",
		Assignee:  agentName,
		CreatedBy: actor,
		Labels:    []string{"kd:agent", "execution_target:local"},
	})
	if err != nil {
		return fmt.Errorf("create agent bead: %w", err)
	}
	fmt.Printf("[gb agent start] Agent bead: %s\n", agentBeadID)

	// Ensure bead is closed on exit.
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		if err := daemon.CloseBead(dctx, agentBeadID, nil); err != nil {
			fmt.Fprintf(os.Stderr, "[gb agent start] warning: failed to close agent bead: %v\n", err)
		} else {
			fmt.Printf("[gb agent start] Agent bead %s closed.\n", agentBeadID)
		}
	}()

	if isLocal {
		return runLocal(ctx, agentName, agentBeadID, role, dir, agentCommand)
	}

	imageDir, _ := cmd.Flags().GetString("image-dir")
	imageName, _ := cmd.Flags().GetString("image")
	attach, _ := cmd.Flags().GetBool("attach")
	return runDocker(ctx, agentName, agentBeadID, role, dir, imageDir, imageName, agentCommand, attach)
}

// ── --local ───────────────────────────────────────────────────────────────

func runLocal(ctx context.Context, agentName, agentBeadID, role, dir, agentCommand string) error {
	// Find a free port for coop (health probe gets port+1).
	coopPort, err := freePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}
	coopURL := fmt.Sprintf("http://127.0.0.1:%d", coopPort)
	fmt.Printf("[gb agent start] Starting coop on port %d...\n", coopPort)

	// Materialize workspace .claude/settings.json via gb setup claude.
	fmt.Printf("[gb agent start] Materializing hooks (gb setup claude)...\n")
	if err := runSetupClaude(ctx, dir, role); err != nil {
		fmt.Fprintf(os.Stderr, "[gb agent start] config beads not found, installing defaults...\n")
		if err2 := runSetupClaudeDefaults(dir); err2 != nil {
			fmt.Fprintf(os.Stderr, "[gb agent start] warning: could not write .claude/settings.json: %v\n", err2)
		}
	}

	// Build env for the coop+claude subprocess.
	env := localAgentEnv(agentName, agentBeadID, role, dir, coopURL)

	// Start coop as a background daemon.
	coopProc := exec.CommandContext(ctx, "coop",
		"--agent=claude",
		fmt.Sprintf("--port=%d", coopPort),
		fmt.Sprintf("--port-health=%d", coopPort+1),
		"--cols=220", "--rows=50",
		"--", "sh", "-c", agentCommand,
	)
	coopProc.Dir = dir
	coopProc.Env = env
	coopProc.Stdout = os.Stderr
	coopProc.Stderr = os.Stderr

	if err := coopProc.Start(); err != nil {
		return fmt.Errorf("start coop: %w", err)
	}
	fmt.Printf("[gb agent start] Coop started (pid %d)\n", coopProc.Process.Pid)

	// Ensure coop is killed if we exit early.
	defer func() {
		if coopProc.ProcessState == nil {
			_ = coopProc.Process.Kill()
			_ = coopProc.Wait()
		}
	}()

	// Wait for coop health.
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", coopPort+1)
	if err := waitForHealth(ctx, healthURL, 30*time.Second); err != nil {
		return fmt.Errorf("coop health check: %w", err)
	}

	// Emit SessionStart hook.
	if err := emitAgentHook(ctx, agentBeadID, agentName, "SessionStart", dir); err != nil {
		fmt.Fprintf(os.Stderr, "[gb agent start] warning: SessionStart emit failed: %v\n", err)
	}

	// Start heartbeat in background.
	go agentHeartbeatLoop(ctx, agentBeadID, agentName, dir)

	// Attach an interactive terminal.
	fmt.Printf("[gb agent start] Attaching to session (detach: ctrl-\\ )...\n")
	attachProc := exec.CommandContext(ctx, "coop", "attach", coopURL)
	attachProc.Stdin = os.Stdin
	attachProc.Stdout = os.Stdout
	attachProc.Stderr = os.Stderr
	_ = attachProc.Run()

	// After detach: wait for coop session to finish naturally.
	fmt.Printf("[gb agent start] Detached. Coop session continues in background.\n")
	fmt.Printf("[gb agent start] Re-attach: coop attach %s\n", coopURL)
	fmt.Printf("[gb agent start] Agent bead: %s\n", agentBeadID)

	waitErr := coopProc.Wait()

	// Emit Stop hook (best-effort).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = emitAgentHook(stopCtx, agentBeadID, agentName, "Stop", dir)

	if waitErr != nil && ctx.Err() == nil {
		return fmt.Errorf("coop exited: %w", waitErr)
	}
	return nil
}

// ── --docker ──────────────────────────────────────────────────────────────

func runDocker(ctx context.Context, agentName, agentBeadID, role, dir, imageDir, imageName string, agentCommand string, attach bool) error {
	if imageName == "" {
		if imageDir == "" {
			candidates := []string{
				filepath.Join(os.Getenv("HOME"), "gasboat", "images", "agent"),
				"./images/agent",
			}
			for _, c := range candidates {
				if _, err := os.Stat(filepath.Join(c, "Dockerfile")); err == nil {
					imageDir = c
					break
				}
			}
			if imageDir == "" {
				return fmt.Errorf("could not find gasboat agent Dockerfile; use --image-dir or --image")
			}
		}

		imageName = "gb-agent-local:latest"
		fmt.Printf("[gb agent start] Building agent image from %s...\n", imageDir)
		buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", imageName, imageDir)
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("docker build: %w", err)
		}
		fmt.Printf("[gb agent start] Image built: %s\n", imageName)
	}

	beadsURL := os.Getenv("BEADS_HTTP_URL")
	if beadsURL == "" {
		beadsURL = httpURL
	}

	runArgs := []string{
		"run", "--rm",
		"--name", containerName(agentName),
		"-v", dir + ":/home/agent/workspace",
		"-e", "ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"),
		"-e", "BEADS_HTTP_URL=" + beadsURL,
		"-e", "BEADS_HTTP_ADDR=" + beadsURL,
		"-e", "KD_AGENT_ID=" + agentBeadID,
		"-e", "KD_ACTOR=" + agentName,
		"-e", "BOAT_ROLE=" + role,
		"-e", "BOAT_AGENT=" + agentName,
		"-e", "BOAT_AGENT_BEAD_ID=" + agentBeadID,
		"-e", "BOAT_COMMAND=" + agentCommand,
	}

	for _, k := range []string{
		"COOP_MUX_URL", "COOP_MUX_AUTH_TOKEN",
		"GIT_AUTHOR_NAME", "GIT_USERNAME", "GIT_TOKEN",
		"BEADS_DAEMON_TOKEN",
	} {
		if v := os.Getenv(k); v != "" {
			runArgs = append(runArgs, "-e", k+"="+v)
		}
	}

	if attach {
		runArgs = append(runArgs, imageName)
		fmt.Printf("[gb agent start] Running container '%s' (attached)...\n", containerName(agentName))
		dockerCmd := exec.CommandContext(ctx, "docker", runArgs...)
		dockerCmd.Stdout = os.Stdout
		dockerCmd.Stderr = os.Stderr
		dockerCmd.Stdin = os.Stdin

		go func() {
			time.Sleep(5 * time.Second)
			_ = emitAgentHook(ctx, agentBeadID, agentName, "SessionStart", dir)
			agentHeartbeatLoop(ctx, agentBeadID, agentName, dir)
		}()

		waitErr := dockerCmd.Run()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = emitAgentHook(stopCtx, agentBeadID, agentName, "Stop", dir)
		return waitErr
	}

	// Detached mode.
	runArgs = append([]string{runArgs[0], "-d"}, runArgs[1:]...)
	runArgs = append(runArgs, imageName)

	fmt.Printf("[gb agent start] Starting container '%s' (detached)...\n", containerName(agentName))
	out, err := exec.CommandContext(ctx, "docker", runArgs...).Output()
	if err != nil {
		return fmt.Errorf("docker run: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	fmt.Printf("[gb agent start] Container started: %s\n", containerID[:12])
	fmt.Printf("[gb agent start] Agent bead:       %s\n", agentBeadID)
	fmt.Printf("[gb agent start] Logs:              docker logs -f %s\n", containerName(agentName))
	fmt.Printf("[gb agent start] Stop:              docker stop %s\n", containerName(agentName))

	go func() {
		time.Sleep(5 * time.Second)
		_ = emitAgentHook(context.Background(), agentBeadID, agentName, "SessionStart", dir)
	}()

	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────

// localAgentEnv builds the environment slice for a --local coop subprocess.
func localAgentEnv(agentName, agentBeadID, role, dir, coopAddr string) []string {
	beadsURL := os.Getenv("BEADS_HTTP_URL")
	if beadsURL == "" {
		beadsURL = httpURL
	}

	natsWsURL := strings.Replace(beadsURL, "https://", "wss://", 1)
	natsWsURL = strings.Replace(natsWsURL, "http://", "ws://", 1)
	natsWsURL += "/nats"

	env := os.Environ()
	overrides := map[string]string{
		"KD_AGENT_ID":           agentBeadID,
		"KD_ACTOR":              agentName,
		"BOAT_ROLE":             role,
		"BOAT_AGENT":            agentName,
		"BOAT_AGENT_BEAD_ID":    agentBeadID,
		"BEADS_HTTP_URL":        beadsURL,
		"BEADS_HTTP_ADDR":       beadsURL,
		"BEADS_DAEMON_HTTP_URL": beadsURL,
		"XDG_STATE_HOME":        filepath.Join(dir, ".state"),
		"COOP_NATS_URL":         natsWsURL,
		"COOP_NATS_PREFIX":      "coop.mux",
		"COOP_NATS_RELAY":       "1",
		"COOP_URL":              coopAddr,
	}
	if tok := os.Getenv("BEADS_DAEMON_TOKEN"); tok != "" {
		overrides["COOP_NATS_TOKEN"] = tok
	}

	filtered := env[:0]
	for _, e := range env {
		key := strings.SplitN(e, "=", 2)[0]
		if _, skip := overrides[key]; !skip {
			filtered = append(filtered, e)
		}
	}
	for k, v := range overrides {
		filtered = append(filtered, k+"="+v)
	}
	return filtered
}

// emitAgentHook sends a hook event to the kbeads server.
func emitAgentHook(ctx context.Context, agentBeadID, agentName, hookType, cwd string) error {
	_, err := daemon.EmitHook(ctx, beadsapi.EmitHookRequest{
		AgentBeadID: agentBeadID,
		HookType:    hookType,
		Actor:       agentName,
		CWD:         cwd,
	})
	return err
}

// agentHeartbeatLoop emits periodic hook events to keep the agent alive in the
// presence tracker (reaper threshold: 15 min).
func agentHeartbeatLoop(ctx context.Context, agentBeadID, agentName, cwd string) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = emitAgentHook(ctx, agentBeadID, agentName, "PreToolUse", cwd)
		}
	}
}

// waitForHealth polls an HTTP health endpoint until it returns 200 or times out.
func waitForHealth(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if reqErr != nil {
			return reqErr
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", url)
}

// freePort finds a free TCP port on localhost.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// containerName returns a stable Docker container name for an agent.
func containerName(agentName string) string {
	safe := strings.NewReplacer(" ", "-", "/", "-", ":", "-").Replace(agentName)
	return "gb-agent-" + safe
}
