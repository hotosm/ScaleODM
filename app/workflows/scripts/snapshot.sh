# shellcheck shell=busybox
# Cleanup (onExit) step: log workflow status and workspace state before the
# PVC is removed. Diagnostic only, so it never fails on missing values.
set -u

# Inputs: WORKSPACE (set in scripts.go), WORKFLOW_* env vars (see cleanupEnv).
JOB_ID="${WORKFLOW_NAME:-unknown}"
WORKSPACE_DIR="${WORKSPACE:-/workspace}/$JOB_ID"

echo "=== Workflow Status ==="
echo "Workflow Name: $JOB_ID"
echo "Workflow UID: ${WORKFLOW_UID:-unknown}"
echo "Started: ${WORKFLOW_CREATION_TIMESTAMP:-unknown}"
echo "Finished: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
echo "Duration (seconds): ${WORKFLOW_DURATION:-unknown}"
echo "Status: ${WORKFLOW_STATUS:-unknown}"

echo ""
echo "=== Failed Nodes (JSON) ==="
# JSON array from Argo; empty/unset when nothing failed.
if [ -n "${WORKFLOW_FAILURES:-}" ] && [ "${WORKFLOW_FAILURES}" != "<nil>" ]; then
  echo "${WORKFLOW_FAILURES}"
else
  echo "[]"
fi

echo ""
echo "=== Workspace Disk Usage (df) ==="
df -h "$WORKSPACE_DIR" 2>/dev/null || echo "df failed (workspace may be unmounted)"

echo ""
echo "=== Workspace Disk Usage (du, top level) ==="
(du -sh "$WORKSPACE_DIR"/* 2>/dev/null || true) | sort -h || echo "du failed"

echo ""
echo "=== Workspace Tree (depth 3, excluding images/ and .rclone/) ==="
if [ -d "$WORKSPACE_DIR" ]; then
  (find "$WORKSPACE_DIR" -maxdepth 3 \
    \( -path "$WORKSPACE_DIR/images" -prune \) \
    -o \( -path "$WORKSPACE_DIR/.rclone" -prune \) \
    -o -print 2>/dev/null || true) | sort
else
  echo "Workspace directory missing"
fi

echo ""
echo "Workspace snapshot complete."
