#!/usr/bin/env bash
# test-decisions-yield.sh — decisions + yield E2E tests
#
# Tests gb decision create/list/show/respond and gb yield against a live
# kd server in the gasboat-e2e namespace.
#
# Usage:
#   ./tests/e2e/scripts/test-decisions-yield.sh [--namespace <ns>] [--daemon <url>] [--token <token>]
#
# Prerequisites:
#   - kubectl context pointing at america-e2e-eks
#   - kd binary in PATH (or set KD_BIN) for CRUD
#   - gb binary in PATH (or set GB_BIN) for orchestration
#   - jq and python3 installed
#
# Scenarios tested:
#   1. Decision create — returns ID, exit 0
#   2. Decision list — created decision appears
#   3. Decision show — shows prompt, options, status=open
#   4. Decision respond — closes it; chosen=a
#   5. Yield unblocks on decision close
#   6. Yield unblocks on mail
#   7. Yield timeout

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────
NAMESPACE="${NAMESPACE:-gasboat-e2e}"
KD_BIN="${KD_BIN:-kd}"         # CRUD: create, close, list, show
GB_BIN="${GB_BIN:-gb}"         # Orchestration: decision, yield, mail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
PASS=0
FAIL=0
ERRORS=()
PF_PID=""

export KD_ACTOR="${KD_ACTOR:-e2e-test}"

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

# ── Setup: port-forward kd server ────────────────────────────────
setup_portforward() {
  if [ -n "${BEADS_HTTP_URL:-}" ]; then
    blue "Using BEADS_HTTP_URL=$BEADS_HTTP_URL"
    return
  fi

  blue "Port-forwarding gasboat-e2e kd server..."
  local port=19091
  kubectl -n "$NAMESPACE" port-forward svc/gasboat-e2e-beads "${port}:8080" \
    >/tmp/kd-pf-decisions.log 2>&1 &
  PF_PID=$!
  sleep 3

  export BEADS_HTTP_URL="http://localhost:${port}"
  dim "  Daemon at $BEADS_HTTP_URL (pid $PF_PID)"
}

# ── Setup: get or create test token ──────────────────────────────
setup_token() {
  if [ -n "${BEADS_TOKEN:-}" ]; then
    return
  fi
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
  # Clean up any background yield processes
  for pid in "${YIELD_PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

YIELD_PIDS=()

# ── Bead helpers ──────────────────────────────────────────────────
close_bead() {
  local id="$1"
  "$KD_BIN" close "$id" 2>/dev/null || true
}

# ── Test Suite ────────────────────────────────────────────────────
main() {
  blue "═══════════════════════════════════════════════════"
  blue " decisions + yield E2E tests"
  blue "═══════════════════════════════════════════════════"

  setup_portforward
  setup_token

  # Verify kd server is reachable
  if ! "$KD_BIN" list --status=open --json >/dev/null 2>&1; then
    red "ERROR: Cannot reach kd server at $BEADS_HTTP_URL"
    exit 1
  fi
  green "  Server reachable: $BEADS_HTTP_URL"
  echo

  # ── Scenario 1: Decision create ───────────────────────────────
  blue "Scenario 1: Decision create returns ID, exit 0"
  local dec1_out dec1_rc dec1_id
  dec1_out=$("$GB_BIN" decision create \
    --prompt="E2E test decision s1-$$: choose one" \
    --options='[{"id":"a","label":"Alpha"},{"id":"b","label":"Beta"}]' \
    --no-wait --json 2>/dev/null) ; dec1_rc=$?

  assert_exit "decision create exits 0" 0 "$dec1_rc"

  dec1_id=$(echo "$dec1_out" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
  if [ -n "$dec1_id" ]; then
    pass "decision create returns ID ($dec1_id)"
  else
    fail "decision create returns ID" "no id in output: $dec1_out"
  fi
  echo

  # ── Scenario 2: Decision list ─────────────────────────────────
  blue "Scenario 2: Created decision appears in decision list"
  if [ -n "$dec1_id" ]; then
    local list_out
    list_out=$("$GB_BIN" decision list --json 2>/dev/null || echo "[]")
    assert_contains "decision list contains created ID" "$dec1_id" "$list_out"
  else
    fail "scenario 2 skipped" "no decision ID from scenario 1"
  fi
  echo

  # ── Scenario 3: Decision show ─────────────────────────────────
  blue "Scenario 3: Decision show displays prompt, options, status"
  if [ -n "$dec1_id" ]; then
    local show_out show_rc
    show_out=$("$GB_BIN" decision show "$dec1_id" --json 2>/dev/null) ; show_rc=$?
    assert_exit "decision show exits 0" 0 "$show_rc"
    assert_contains "show contains prompt" "E2E test decision s1-$$" "$show_out"
    assert_contains "show contains status open" "open" "$show_out"
  else
    fail "scenario 3 skipped" "no decision ID from scenario 1"
  fi
  echo

  # ── Scenario 4: Decision respond ──────────────────────────────
  blue "Scenario 4: Decision respond closes it; chosen=a"
  if [ -n "$dec1_id" ]; then
    local respond_out respond_rc
    respond_out=$("$GB_BIN" decision respond "$dec1_id" --select=a --json 2>/dev/null) ; respond_rc=$?
    assert_exit "decision respond exits 0" 0 "$respond_rc"
    assert_contains "respond returns status closed" "closed" "$respond_out"

    # Verify via show
    local show_after
    show_after=$("$GB_BIN" decision show "$dec1_id" --json 2>/dev/null || echo "{}")
    assert_contains "decision now closed" "closed" "$show_after"
    assert_contains "chosen is a" '"a"' "$show_after"
  else
    fail "scenario 4 skipped" "no decision ID from scenario 1"
  fi
  echo

  # ── Scenario 5: Yield unblocks on decision close ──────────────
  blue "Scenario 5: Yield unblocks when pending decision is closed"

  # Create a fresh decision
  local dec5_out dec5_id
  dec5_out=$("$GB_BIN" decision create \
    --prompt="E2E yield test s5-$$: pick" \
    --options='[{"id":"a","label":"Go"},{"id":"b","label":"Wait"}]' \
    --no-wait --json 2>/dev/null)
  dec5_id=$(echo "$dec5_out" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

  if [ -n "$dec5_id" ]; then
    dim "  Decision: $dec5_id"

    # Start yield in background
    local yield5_out="/tmp/yield5-$$.out"
    "$GB_BIN" yield --timeout=15s >"$yield5_out" 2>&1 &
    local yield5_pid=$!
    YIELD_PIDS+=("$yield5_pid")
    sleep 2

    # Respond to the decision (closes it)
    "$GB_BIN" decision respond "$dec5_id" --select=a 2>/dev/null || true
    dim "  Responded to decision"

    # Wait for yield to exit
    local yield5_rc=0
    wait "$yield5_pid" || yield5_rc=$?
    local yield5_content
    yield5_content=$(cat "$yield5_out" 2>/dev/null || echo "")

    assert_exit "yield exits 0 on decision close" 0 "$yield5_rc"
    assert_contains "yield output mentions resolved" "resolved\|closed" "$yield5_content"
    rm -f "$yield5_out"
  else
    fail "scenario 5 skipped" "could not create decision"
  fi
  echo

  # ── Scenario 6: Yield unblocks on mail ────────────────────────
  blue "Scenario 6: Yield unblocks when mail arrives"

  # Ensure no open decisions so yield waits for mail
  local yield6_out="/tmp/yield6-$$.out"
  "$GB_BIN" yield --timeout=15s >"$yield6_out" 2>&1 &
  local yield6_pid=$!
  YIELD_PIDS+=("$yield6_pid")
  sleep 2

  # Send mail
  local mail6_out
  mail6_out=$("$GB_BIN" mail send "$KD_ACTOR" \
    --subject="E2E mail test s6-$$" \
    --body="Hello from E2E" --json 2>/dev/null || echo "{}")
  local mail6_id
  mail6_id=$(echo "$mail6_out" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
  dim "  Mail sent: $mail6_id"

  # Wait for yield to exit
  local yield6_rc=0
  wait "$yield6_pid" || yield6_rc=$?
  local yield6_content
  yield6_content=$(cat "$yield6_out" 2>/dev/null || echo "")

  assert_exit "yield exits 0 on mail" 0 "$yield6_rc"
  assert_contains "yield output mentions mail" "Mail received" "$yield6_content"

  # Clean up mail bead
  [ -n "$mail6_id" ] && close_bead "$mail6_id"
  rm -f "$yield6_out"
  echo

  # ── Scenario 7: Yield timeout ─────────────────────────────────
  blue "Scenario 7: Yield times out with no events"
  local yield7_out yield7_rc
  yield7_out=$("$GB_BIN" yield --timeout=3s 2>/dev/null) ; yield7_rc=$?

  assert_exit "yield exits 0 on timeout" 0 "$yield7_rc"
  assert_contains "yield output mentions timeout" "timed out" "$yield7_out"
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
