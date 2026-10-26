# shellcheck shell=bash
# Process step: run ODM on $WORKSPACE/$JOB_ID. Script arguments are ODM flags.
set -e
set -o pipefail

# Inputs (set in scripts.go).
: "${ATTEMPT:?}" "${WORKSPACE:?}" "${JOB_ID:?}" "${ODM_PROJECT_ID:?}"

echo "=== process attempt $ATTEMPT @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
mkdir -p "$HOME" "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$MPLCONFIGDIR"
echo "Running ODM processing..."
echo "Processing job: $JOB_ID"
echo "ODM Project ID: $ODM_PROJECT_ID"
echo "Executing: python3 -u run.py $* --project-path $WORKSPACE $JOB_ID"
# -u: unbuffered, so logs survive OOM kills.
python3 -u run.py "$@" --project-path "$WORKSPACE" "$JOB_ID"
echo "ODM processing complete"
