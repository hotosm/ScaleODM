# shellcheck shell=bash
# Convert DJI thermal R-JPEGs to °C TIFFs for ODM. Args are converter settings.
# Retry-safe: the original photos are kept in source/.
set -e
set -o pipefail

# Inputs (set in scripts.go).
: "${ATTEMPT:?}" "${WORKSPACE:?}" "${JOB_ID:?}" "${JOBS:?}"

echo "=== thermal attempt $ATTEMPT @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
JOB_DIR="$WORKSPACE/$JOB_ID"
if [ ! -d "$JOB_DIR/source" ]; then
  mv "$JOB_DIR/images" "$JOB_DIR/source"
fi
rm -rf "$JOB_DIR/images"

# Bad photos are skipped and listed in the uploaded log. Fails only if none convert.
dji-thermal-convert --jobs "$JOBS" "$@" "$JOB_DIR/source" "$JOB_DIR/images" 2>&1 |
  tee "$JOB_DIR/thermal_conversion.log"
