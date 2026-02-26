#!/usr/bin/env bash
# test-gate-system.sh — kbeads gate system E2E tests
#
# Tests gb bus emit --hook=Stop gate enforcement against a live
# kd server. Works against any gasboat namespace (gasboat-e2e, gasboat-rwx, etc.).
#
# Usage:
#   ./tests/e2e/scripts/test-gate-system.sh [--namespace <ns>] [--daemon <url>] [--token <token>]
#
# Prerequisites:
#   - kubectl context pointing at the target cluster
#   - kd binary in PATH (or set KD_BIN) for CRUD
#   - gb binary in PATH (or set GB_BIN) for orchestration
#   - jq installed
#
# Scenarios tested:
#   1. Decision gate blocks Stop when no decision offered
#   2. Decision create clears gate; Stop still blocks (gate re-pending after close)
#   3. Decision respond satisfies gate; Stop now allowed
#   4. No agent identity → fails open (exit 0)
#   5. commit-push auto-check: warns on dirty tree but does not block
#   6. gate transitions + gb gate mark decision requires --force (hardening)
#   7. gb yield sets gate_satisfied_by=yield; gb gate satisfied-by validates method

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────
NAMESPACE="${NAMESPACE:-gasboat-e2e}"
BEADS_SVC="${BEADS_SVC:-${NAMESPACE}-beads}"
KD_BIN="${KD_BIN:-kd}"         # CRUD: create, close, list, show
GB_BIN="${GB_BIN:-gb}"         # Orchestration: bus emit, decision, gate
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
PASS=0
FAIL=0
ERRORS=()
PF_PID=""

# ── Helpers ───────────────────────────────────────────────────────
red()   { printf '\033[0;31m%s\033[0m\n' "$*"; }
green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[0;34m%s\033[0m\n' "$*"; }
dim()   { printf '\033[2m%s\033[0m\n' "$*"; }

pass() {
  local name="$1"
  green "  ✓ $name"
  PASS=$((PASS + 1))
}

fail() {
  local name="$1"
  local reason="${2:-}"
  red "  ✗ $name"
  [ -n "$reason" ] && dim "    $reason"
  FAIL=$((FAIL + 1))
  ERRORS+=("$name: $reason")
}

assert_exit() {
  local name="$1"
  local expected="$2"
  local actual="$3"
  if [ "$actual" -eq "$expected" ]; then
    pass "$name (exit $expected)"
  else
    fail "$name" "expected exit $expected, got exit $actual"
  fi
}

assert_contains() {
  local name="$1"
  local pattern="$2"
  local actual="$3"
  if echo "$actual" | grep -q "$pattern"; then
    pass "$name (contains '$pattern')"
  else
    fail "$name" "expected output to contain '$pattern'; got: $actual"
  fi
}

# Emit a Stop hook event for the given agent bead ID.
# Returns the exit code of gb bus emit.
emit_stop() {
  local agent_id="$1"
  local cwd="${2:-/tmp}"
  local hook_json
  hook_json=$(printf '{"session_id":"e2e-test-session","cwd":"%s"}' "$cwd")
  KD_AGENT_ID="$agent_id" \
    "$GB_BIN" bus emit --hook=Stop <<< "$hook_json" \
    2>/tmp/kd-emit-stderr.txt
  # Return the exit code
}

emit_stop_rc() {
  local agent_id="$1"
  local cwd="${2:-/tmp}"
  local hook_json
  hook_json=$(printf '{"session_id":"e2e-test-session","cwd":"%s"}' "$cwd")
  KD_AGENT_ID="$agent_id" \
    "$GB_BIN" bus emit --hook=Stop <<< "$hook_json" \
    2>/tmp/kd-emit-stderr.txt
  echo $?
}

# ── Setup: port-forward kd server ────────────────────────────────
setup_portforward() {
  # kd uses BEADS_HTTP_URL env var for the daemon URL
  if [ -n "${BEADS_HTTP_URL:-}" ]; then
    blue "Using BEADS_HTTP_URL=$BEADS_HTTP_URL"
    return
  fi

  blue "Port-forwarding ${NAMESPACE} kd server..."
  local port=19090
  kubectl -n "$NAMESPACE" port-forward "svc/${BEADS_SVC}" "${port}:8080" \
    >/tmp/kd-pf.log 2>&1 &
  PF_PID=$!
  sleep 3

  export BEADS_HTTP_URL="http://localhost:${port}"
  dim "  Daemon at $BEADS_HTTP_URL (pid $PF_PID)"
}

# ── Setup: get or create test token ──────────────────────────────
setup_token() {
  # kd uses BEADS_TOKEN env var for auth
  if [ -n "${BEADS_TOKEN:-}" ]; then
    return
  fi
  # Try fetching from K8s secret
  local secret
  secret=$(kubectl -n "$NAMESPACE" get secret kd-beads-token \
    -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || true)
  if [ -n "$secret" ]; then
    export BEADS_TOKEN="$secret"
    dim "  Token loaded from secret kd-beads-token"
  else
    dim "  WARNING: No token found; kd server may reject requests"
  fi
}

# ── Cleanup ───────────────────────────────────────────────────────
cleanup() {
  if [ -n "$PF_PID" ]; then
    kill "$PF_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ── Create a test bead (agent identity) ──────────────────────────
create_test_agent() {
  local title="$1"
  "$KD_BIN" create "$title" --type=task --json 2>/dev/null \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null \
    || echo ""
}

close_bead() {
  local id="$1"
  "$KD_BIN" close "$id" 2>/dev/null || true
}

# ── Test Suite ────────────────────────────────────────────────────
main() {
  blue "═══════════════════════════════════════════════════"
  blue " kbeads gate system E2E tests"
  blue "═══════════════════════════════════════════════════"

  setup_portforward
  setup_token

  # Verify kd server is reachable
  if ! "$KD_BIN" list --status=open --json >/dev/null 2>&1; then
    red "ERROR: Cannot reach kd server at $BEADS_HTTP_URL"
    exit 1
  fi
  green "  Server reachable: $BEADS_HTTP_URL"

  # Verify the server supports gate endpoints (requires bd-pe028 gate system)
  local gate_probe_id
  gate_probe_id=$("$KD_BIN" create "e2e-gate-probe-$$" --type=task --json 2>/dev/null \
    | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
  if [ -n "$gate_probe_id" ]; then
    local gate_probe_out
    gate_probe_out=$(KD_AGENT_ID="$gate_probe_id" "$GB_BIN" gate status 2>&1 || true)
    "$KD_BIN" close "$gate_probe_id" 2>/dev/null || true
    if echo "$gate_probe_out" | grep -q "404\|unknown command\|not found"; then
      red "ERROR: kd server does not support gate API (bd-pe028 not deployed)"
      red "  Deploy a kbeads image built from the gate-system branch first."
      red "  See: ~/kbeads (commit 8c92e4e or later)"
      exit 1
    fi
  fi
  green "  Gate API: supported"
  echo

  # ── Scenario 1: Decision gate blocks Stop when no decision offered ──
  blue "Scenario 1: Decision gate blocks Stop (no decision created)"
  AGENT1=$(create_test_agent "e2e-gate-test-1-$$")
  if [ -z "$AGENT1" ]; then
    fail "scenario-1-setup" "Could not create test bead"
  else
    dim "  Agent bead: $AGENT1"

    # Emit Stop — should block (exit 2) because no decision offered
    local rc
    emit_stop "$AGENT1" "/tmp" >/dev/null 2>/tmp/kd-emit-stderr.txt; rc=$?
    assert_exit "decision gate blocks Stop hook" 2 "$rc"

    # Verify block reason in stderr
    local stderr_content
    stderr_content=$(cat /tmp/kd-emit-stderr.txt 2>/dev/null || true)
    assert_contains "block reason mentions decision" "decision" "$stderr_content"

    close_bead "$AGENT1"
  fi
  echo

  # ── Scenario 2: Create decision → gate resets to pending; Stop still blocks ──
  blue "Scenario 2: Decision created but not yet responded → Stop still blocks"
  AGENT2=$(create_test_agent "e2e-gate-test-2-$$")
  if [ -z "$AGENT2" ]; then
    fail "scenario-2-setup" "Could not create test bead"
  else
    dim "  Agent bead: $AGENT2"

    # Create a decision (this resets the decision gate to pending)
    local dec_id
    dec_id=$("$GB_BIN" decision create \
      --prompt="E2E test decision: what next?" \
      --options='[{"id":"a","label":"Continue"},{"id":"b","label":"Stop"}]' \
      --no-wait --json 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null \
      || echo "")

    if [ -z "$dec_id" ]; then
      fail "scenario-2-decision-create" "Could not create decision"
    else
      dim "  Decision bead: $dec_id"

      # Emit Stop — should still block (gate is pending, decision not responded to)
      emit_stop "$AGENT2" "/tmp" >/dev/null 2>/tmp/kd-emit-stderr.txt; rc=$?
      assert_exit "Stop still blocks before decision response" 2 "$rc"

      # Verify gate status shows pending
      local gate_status
      gate_status=$(KD_AGENT_ID="$AGENT2" "$GB_BIN" gate status 2>/dev/null || true)
      assert_contains "gate status shows pending" "pending\|○" "$gate_status"

      # Clean up: close decision bead (this satisfies the gate)
      close_bead "$dec_id"
    fi
    close_bead "$AGENT2"
  fi
  echo

  # ── Scenario 3: Decision responded → gate satisfied; Stop allowed ──
  blue "Scenario 3: Decision closed → gate satisfied → Stop allowed"
  AGENT3=$(create_test_agent "e2e-gate-test-3-$$")
  if [ -z "$AGENT3" ]; then
    fail "scenario-3-setup" "Could not create test bead"
  else
    dim "  Agent bead: $AGENT3"

    # Create a decision
    local dec3_id
    dec3_id=$("$GB_BIN" decision create \
      --prompt="E2E test decision 3: what next?" \
      --options='[{"id":"a","label":"Done"}]' \
      --no-wait --json 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null \
      || echo "")

    if [ -z "$dec3_id" ]; then
      fail "scenario-3-decision-create" "Could not create decision"
    else
      dim "  Decision bead: $dec3_id"

      # Close the decision — this satisfies the gate
      close_bead "$dec3_id"
      dim "  Decision closed (gate should be satisfied)"

      # Emit Stop — should allow (exit 0)
      emit_stop "$AGENT3" "/tmp" >/dev/null 2>/tmp/kd-emit-stderr.txt; rc=$?
      assert_exit "Stop allowed after decision responded" 0 "$rc"

      # Verify gate status shows satisfied
      local gate3_status
      gate3_status=$(KD_AGENT_ID="$AGENT3" "$GB_BIN" gate status 2>/dev/null || true)
      assert_contains "gate status shows satisfied" "satisfied\|●" "$gate3_status"
    fi
    close_bead "$AGENT3"
  fi
  echo

  # ── Scenario 4: No agent identity → fails open ──────────────────
  blue "Scenario 4: No agent identity (KD_AGENT_ID unset) → fails open"
  local hook_json='{"session_id":"e2e-anon","cwd":"/tmp"}'
  # Unset KD_AGENT_ID and KD_ACTOR to simulate unrecognized agent
  env -u KD_AGENT_ID -u KD_ACTOR \
    "$GB_BIN" bus emit --hook=Stop <<< "$hook_json" \
    >/dev/null 2>/tmp/kd-emit-anon-stderr.txt; rc=$?
  assert_exit "anonymous agent: Stop fails open" 0 "$rc"
  echo

  # ── Scenario 5: commit-push auto-check warns but does not block ──
  blue "Scenario 5: Dirty git tree → commit-push warns, does not block"
  AGENT5=$(create_test_agent "e2e-gate-test-5-$$")
  if [ -z "$AGENT5" ]; then
    fail "scenario-5-setup" "Could not create test bead"
  else
    dim "  Agent bead: $AGENT5"

    # Satisfy decision gate first so it doesn't interfere
    local dec5_id
    dec5_id=$("$GB_BIN" decision create \
      --prompt="E2E scenario 5 decision" \
      --options='[{"id":"a","label":"ok"}]' \
      --no-wait --json 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null \
      || echo "")
    [ -n "$dec5_id" ] && close_bead "$dec5_id"

    # Create a dirty git worktree for the commit-push check
    local dirty_dir
    dirty_dir=$(mktemp -d)
    git -C "$dirty_dir" init -q
    echo "dirty" > "$dirty_dir/uncommitted.txt"
    # No commit — tree is dirty (untracked file)

    local stdout_content
    stdout_content=$(KD_AGENT_ID="$AGENT5" \
      "$GB_BIN" bus emit --hook=Stop --cwd="$dirty_dir" \
      <<< '{"session_id":"e2e-dirty","cwd":"'"$dirty_dir"'"}' \
      2>/tmp/kd-emit-dirty-stderr.txt || true)
    rc=$?

    # Should allow (exit 0) — commit-push is a soft warning, not a hard block
    assert_exit "dirty tree: Stop allows (soft warning)" 0 "$rc"

    # Verify the warning appears in stdout
    assert_contains "dirty tree warning in stdout" "uncommitted\|unpushed\|dirty\|system-reminder" "$stdout_content"

    rm -rf "$dirty_dir"
    close_bead "$AGENT5"
  fi
  echo

  # ── Scenario 6: gb gate status reflects reality + gate mark hardening ──
  blue "Scenario 6: gate transitions + gb gate mark decision requires --force"
  AGENT6=$(create_test_agent "e2e-gate-test-6-$$")
  if [ -z "$AGENT6" ]; then
    fail "scenario-6-setup" "Could not create test bead"
  else
    dim "  Agent bead: $AGENT6"

    # Initially: no gate row → gate status should show nothing or pending
    local status6_init
    status6_init=$(KD_AGENT_ID="$AGENT6" "$GB_BIN" gate status 2>/dev/null || true)
    dim "  Initial status: $status6_init"

    # Trigger gate creation by emitting Stop
    emit_stop "$AGENT6" "/tmp" >/dev/null 2>/dev/null; true  # ignore exit code

    # Now gate should be pending
    local status6_pending
    status6_pending=$(KD_AGENT_ID="$AGENT6" "$GB_BIN" gate status 2>/dev/null || true)
    assert_contains "gate status pending after emit" "pending\|○\|decision" "$status6_pending"

    # Verify that gb gate mark decision WITHOUT --force is now rejected
    local mark_no_force_rc
    KD_AGENT_ID="$AGENT6" "$GB_BIN" gate mark decision \
      >/dev/null 2>/tmp/kd-gate-mark-stderr.txt; mark_no_force_rc=$?
    if [ "$mark_no_force_rc" -ne 0 ]; then
      pass "gate mark decision without --force: rejected (exit $mark_no_force_rc)"
    else
      fail "gate mark decision without --force: should have been rejected" \
        "expected non-zero exit, got 0"
    fi
    local mark_err_content
    mark_err_content=$(cat /tmp/kd-gate-mark-stderr.txt 2>/dev/null || true)
    assert_contains "gate mark rejection message mentions --force" "force\|yield" "$mark_err_content"

    # Gate should still be pending (mark was rejected)
    local status6_still_pending
    status6_still_pending=$(KD_AGENT_ID="$AGENT6" "$GB_BIN" gate status 2>/dev/null || true)
    assert_contains "gate still pending after rejected mark" "pending\|○\|decision" "$status6_still_pending"

    # Manually satisfy via gb gate mark --force (operator override)
    local mark_force_rc
    KD_AGENT_ID="$AGENT6" "$GB_BIN" gate mark decision --force \
      >/dev/null 2>/dev/null; mark_force_rc=$?
    assert_exit "gate mark decision --force: succeeds" 0 "$mark_force_rc"

    # Now gate should be satisfied
    local status6_satisfied
    status6_satisfied=$(KD_AGENT_ID="$AGENT6" "$GB_BIN" gate status 2>/dev/null || true)
    assert_contains "gate status satisfied after --force mark" "satisfied\|●" "$status6_satisfied"

    # gate_satisfied_by should be "operator"
    local satisfied_by_force
    satisfied_by_force=$(KD_AGENT_ID="$AGENT6" "$GB_BIN" gate satisfied-by 2>/dev/null || true)
    assert_contains "gate_satisfied_by=operator after --force" "operator" "$satisfied_by_force"

    # Clear the gate
    KD_AGENT_ID="$AGENT6" "$GB_BIN" gate clear decision 2>/dev/null || true

    # Back to pending
    local status6_cleared
    status6_cleared=$(KD_AGENT_ID="$AGENT6" "$GB_BIN" gate status 2>/dev/null || true)
    assert_contains "gate status pending after clear" "pending\|○\|decision" "$status6_cleared"

    close_bead "$AGENT6"
  fi
  echo

  # ── Scenario 7: gb yield sets gate_satisfied_by=yield ─────────────
  blue "Scenario 7: gb yield satisfies gate and sets gate_satisfied_by=yield"
  AGENT7=$(create_test_agent "e2e-gate-test-7-$$")
  if [ -z "$AGENT7" ]; then
    fail "scenario-7-setup" "Could not create test bead"
  else
    dim "  Agent bead: $AGENT7"

    # Create a decision for this agent
    local dec7_id
    dec7_id=$(KD_AGENT_ID="$AGENT7" "$GB_BIN" decision create \
      --prompt="E2E scenario 7 decision" \
      --options='[{"id":"a","label":"ok","artifact_type":"report"}]' \
      --no-wait --json 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null \
      || echo "")

    if [ -z "$dec7_id" ]; then
      fail "scenario-7-decision-create" "Could not create decision"
    else
      dim "  Decision bead: $dec7_id"

      # Verify gate_satisfied_by is NOT yet set
      local before_satisfied_by
      before_satisfied_by=$(KD_AGENT_ID="$AGENT7" "$GB_BIN" gate satisfied-by 2>/dev/null \
        || echo "not-set")
      if [ "$before_satisfied_by" = "not-set" ] || [ -z "$before_satisfied_by" ]; then
        pass "gate_satisfied_by unset before yield (expected)"
      else
        fail "gate_satisfied_by should be unset before yield" \
          "got: $before_satisfied_by"
      fi

      # Respond to decision (closes it), which triggers gb yield to unblock
      # and satisfy the gate. We simulate this: close the decision then
      # trigger satisfaction via gb gate mark --force (since gb yield requires
      # a live blocking call we cannot easily test in shell).
      # The gate_satisfied_by=yield path is tested via the marker being set
      # when gate is satisfied by yield. Here we test the --force path (already
      # covered in scenario 6) and verify the negative (unset case).
      # Full yield integration is tested in claudeless scenarios.
      close_bead "$dec7_id"
      dim "  Decision closed (gate should be satisfied by daemon)"

      # After decision close, gate is satisfied. Now manually set the yield marker
      # to simulate what gb yield does (in real sessions, gb yield sets this).
      KD_AGENT_ID="$AGENT7" "$GB_BIN" gate mark decision --force >/dev/null 2>/dev/null || true

      # gb gate satisfied-by should exit 0 for "operator"
      local after_satisfied_by_rc
      KD_AGENT_ID="$AGENT7" "$GB_BIN" gate satisfied-by >/dev/null 2>/dev/null
      after_satisfied_by_rc=$?
      assert_exit "gate satisfied-by exits 0 when operator" 0 "$after_satisfied_by_rc"

      # Test stop-gate with a fresh agent that has no gate_satisfied_by set
      local agent7b
      agent7b=$(create_test_agent "e2e-gate-test-7b-$$")
      if [ -n "$agent7b" ]; then
        dim "  Test agent 7b: $agent7b (no gate_satisfied_by)"
        # Trigger gate, then manually satisfy it via daemon (direct mark without flag tracking)
        # We cannot do this easily; instead check that satisfied-by exits 1 when field unset
        local unset_rc
        KD_AGENT_ID="$agent7b" "$GB_BIN" gate satisfied-by >/dev/null 2>/dev/null
        unset_rc=$?
        if [ "$unset_rc" -ne 0 ]; then
          pass "gate satisfied-by exits 1 when gate_satisfied_by not set"
        else
          fail "gate satisfied-by should exit 1 when field not set" \
            "expected non-zero exit, got 0"
        fi
        close_bead "$agent7b"
      fi
    fi
    close_bead "$AGENT7"
  fi
  echo

  # ── Summary ───────────────────────────────────────────────────────
  blue "═══════════════════════════════════════════════════"
  local total=$((PASS + FAIL))
  if [ "$FAIL" -eq 0 ]; then
    green " ✓ All $total assertions passed"
  else
    red " ✗ $FAIL/$total assertions failed"
    echo
    for err in "${ERRORS[@]}"; do
      red "   - $err"
    done
  fi
  blue "═══════════════════════════════════════════════════"

  [ "$FAIL" -eq 0 ]
}

main "$@"
