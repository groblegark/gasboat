package reconciler

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// UpgradeStrategy defines how a role's pods should be updated during drift.
type UpgradeStrategy int

const (
	// UpgradeSkip means don't kill running pods for image drift.
	// New pods spawned after the image change will use the new image.
	// Used for jobs: let running ones finish, new ones get new spec.
	UpgradeSkip UpgradeStrategy = iota

	// UpgradeRolling means update one pod at a time, waiting for the
	// replacement to become Ready before upgrading the next.
	UpgradeRolling

	// UpgradeLast means defer this role until all other non-Last roles
	// have been upgraded. Then apply UpgradeRolling.
	UpgradeLast
)

// modeUpgradeStrategy returns the upgrade strategy for a given mode.
func modeUpgradeStrategy(mode string) UpgradeStrategy {
	switch mode {
	case "job":
		return UpgradeSkip
	default:
		// crew and other persistent agents use rolling updates.
		return UpgradeRolling
	}
}

// UpgradeTracker tracks the state of an ongoing rolling upgrade across pods.
// It ensures only one pod per role is being upgraded at a time.
type UpgradeTracker struct {
	mu     sync.Mutex
	logger *slog.Logger

	// upgrading tracks pods currently being upgraded (deleted for recreation).
	// Key: pod name, Value: time the upgrade started.
	upgrading map[string]time.Time

	// pendingByMode tracks pods that need upgrading, grouped by mode.
	// Key: mode, Value: list of pod names needing upgrade.
	pendingByMode map[string][]string
}

// NewUpgradeTracker creates a new upgrade tracker.
func NewUpgradeTracker(logger *slog.Logger) *UpgradeTracker {
	return &UpgradeTracker{
		logger:        logger,
		upgrading:     make(map[string]time.Time),
		pendingByMode: make(map[string][]string),
	}
}

// Reset clears all tracking state. Called at the start of each reconcile pass.
func (t *UpgradeTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingByMode = make(map[string][]string)
}

// RegisterDrift records that a pod needs upgrading due to spec drift.
func (t *UpgradeTracker) RegisterDrift(podName, mode string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingByMode[mode] = append(t.pendingByMode[mode], podName)
}

// MarkUpgrading records that a pod upgrade has started (pod deleted for recreation).
func (t *UpgradeTracker) MarkUpgrading(podName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.upgrading[podName] = time.Now()
}

// ClearUpgrading removes a pod from the upgrading set (pod recreated and healthy).
func (t *UpgradeTracker) ClearUpgrading(podName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.upgrading, podName)
}

// IsUpgrading returns true if any pod of the given mode is currently being upgraded.
func (t *UpgradeTracker) IsUpgrading(mode string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name := range t.upgrading {
		// Pod names follow the pattern {mode}-{project}-{role}-{agentName}
		// We check if the mode segment matches.
		if podMode := extractModeFromPodName(name); podMode == mode {
			return true
		}
	}
	return false
}

// AllNonLastUpgraded returns true if all roles with non-Last strategies
// have no pending upgrades and nothing is currently upgrading.
func (t *UpgradeTracker) AllNonLastUpgraded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if any non-Last mode has pending upgrades.
	for mode, pods := range t.pendingByMode {
		if modeUpgradeStrategy(mode) == UpgradeLast {
			continue
		}
		if len(pods) > 0 {
			return false
		}
	}

	// Check if any non-Last mode is currently upgrading.
	for name := range t.upgrading {
		mode := extractModeFromPodName(name)
		if modeUpgradeStrategy(mode) != UpgradeLast {
			return false
		}
	}

	return true
}

// CanUpgrade determines whether a specific pod can be upgraded right now,
// based on its role's strategy and the current upgrade state.
func (t *UpgradeTracker) CanUpgrade(podName, mode string) bool {
	strategy := modeUpgradeStrategy(mode)

	switch strategy {
	case UpgradeSkip:
		// Never upgrade running jobs for drift.
		return false

	case UpgradeLast:
		// Defer until all other non-Last modes are done.
		if !t.AllNonLastUpgraded() {
			t.logger.Info("deferring upgrade until all other modes are upgraded",
				"pod", podName, "mode", mode)
			return false
		}
		return !t.IsUpgrading(mode)

	case UpgradeRolling:
		// Only one pod per mode at a time.
		return !t.IsUpgrading(mode)

	default:
		return false
	}
}

// CleanStaleUpgrades removes entries from the upgrading set that are older
// than the timeout. This handles the case where a pod was deleted but the
// replacement never became healthy.
func (t *UpgradeTracker) CleanStaleUpgrades(timeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for name, started := range t.upgrading {
		if now.Sub(started) > timeout {
			t.logger.Warn("upgrade timed out, clearing stale entry",
				"pod", name, "started", started)
			delete(t.upgrading, name)
		}
	}
}

// extractModeFromPodName extracts the mode segment from a pod name.
// Pod names follow the pattern: {mode}-{project}-{role}-{agentName}
// e.g., "job-gasboat-devops-furiosa" -> "job"
//       "crew-gasboat-devops-toolbox" -> "crew"
func extractModeFromPodName(podName string) string {
	parts := strings.SplitN(podName, "-", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// IsPodReady returns true if a pod is in Running phase and all containers
// have passing readiness probes.
func IsPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
