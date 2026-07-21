package s3

import (
	"fmt"
	"strings"
)

// File handling utilities for S3-based workflows
// These functions generate shell scripts for use in Argo workflow containers

// alwaysExcludePatterns are unconditionally prepended to both download and
// image-count filters, regardless of user settings or useDefaultExcludes.
// They protect ScaleODM's own output directories from being re-ingested on a
// rerun, and match the paths the upload script writes into the write S3 path.
var alwaysExcludePatterns = []string{
	"output/**", "**/output/**",
	"odm/**", "**/odm/**",
}

// imageIncludePatterns is the canonical rclone filter list for input imagery
// and supported archive types. Mirrored as `+ <pattern>` lines in the
// generated --filter-from file.
var imageIncludePatterns = []string{
	"*.jpg",
	"*.jpeg",
	"*.JPG",
	"*.JPEG",
	"*.tiff",
	"*.tif",
	"*.TIFF",
	"*.TIF",
	"*.zip",
	"*.ZIP",
	"*.tar.gz",
	"*.tar",
	"*.TAR.GZ",
	"*.TAR",
}

var uploadExcludePatterns = []string{
	".rclone/**",
	"opensfm/undistorted/**",
	"**/opensfm/undistorted/**",
}

func renderRcloneExcludeFlags(excludePatterns []string) string {
	if len(excludePatterns) == 0 {
		return ""
	}

	var b strings.Builder
	for _, pattern := range excludePatterns {
		b.WriteString(` --exclude "`)
		b.WriteString(pattern)
		b.WriteString(`"`)
	}
	return b.String()
}

// renderRcloneFilterFile builds the contents of a rclone --filter-from file.
// Order matters: excludes go first (so they win over the catch-all includes
// below), then includes for image/archive extensions, then a final catch-all
// that drops everything else.
func renderRcloneFilterFile(excludePatterns []string) string {
	var b strings.Builder
	for _, p := range excludePatterns {
		b.WriteString("- ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	for _, p := range imageIncludePatterns {
		b.WriteString("+ ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	b.WriteString("- *\n")
	return b.String()
}

func rcloneS3ConfigSnippet() string {
	return `PROVIDER="AWS"
if [ -n "${AWS_S3_ENDPOINT:-}" ]; then
  PROVIDER="Minio"
fi

cat > "$RCLONE_CONFIG" <<EOF
[s3]
type = s3
provider = ${PROVIDER}
env_auth = true
region = ${AWS_DEFAULT_REGION:-us-east-1}
no_check_bucket = true
EOF

if [ -n "${AWS_S3_ENDPOINT:-}" ]; then
  cat >> "$RCLONE_CONFIG" <<EOF
endpoint = ${AWS_S3_ENDPOINT}
force_path_style = true
use_path_style = true
EOF
fi`
}

// GenerateDownloadScript generates a shell script for downloading and processing imagery from S3
// Credentials are injected via Kubernetes Secret references in the workflow spec
// Note: We create rclone config on-the-fly to avoid ContainerSet env var filtering of RCLONE_CONFIG_*
//
// excludePatterns are rclone-style filter patterns (relative paths, no leading
// "/", validated by the API layer). They are written verbatim to a
// --filter-from file in the workspace, so user input never touches the shell.
// The default set always includes "output/**" and "**/output/**" so prior
// ScaleODM auto-output dirs are skipped even when the caller supplies no
// excludes; the API layer is responsible for prepending the broader
// DefaultProjectExcludes set when useDefaultExcludes is true.
//
// maxDepth caps how deep rclone walks beneath srcPath. A value > 0 maps to
// `--max-depth N`; values <= 0 mean "no limit" so callers wanting an
// unbounded scan can opt in explicitly.
func GenerateDownloadScript(jobID, srcPath string, excludePatterns []string, maxDepth int) string {
	patterns := make([]string, 0, len(alwaysExcludePatterns)+len(excludePatterns))
	patterns = append(patterns, alwaysExcludePatterns...)
	patterns = append(patterns, excludePatterns...)
	filterFileContents := renderRcloneFilterFile(patterns)

	maxDepthFlag := ""
	if maxDepth > 0 {
		maxDepthFlag = fmt.Sprintf(" --max-depth %d", maxDepth)
	}

	return `set -e
set -o pipefail
echo "Downloading imagery from S3..."
JOB_ID="` + jobID + `"
SRC_PATH="` + srcPath + `"
DEST_DIR="/workspace/$JOB_ID/images"

echo "Job ID: $JOB_ID"
echo "Source: $SRC_PATH"
echo "Destination: $DEST_DIR"
mkdir -p "$DEST_DIR"

# Create rclone config on-the-fly using AWS env vars (not filtered by ContainerSet)
# This avoids the RCLONE_CONFIG_* env var filtering issue
RCLONE_DIR="/workspace/$JOB_ID/.rclone"
mkdir -p "$RCLONE_DIR"
export RCLONE_CONFIG="$RCLONE_DIR/rclone.conf"

` + rcloneS3ConfigSnippet() + `

# Convert s3://bucket/path to s3:bucket/path format for rclone remote
if echo "$SRC_PATH" | grep -q "^s3://"; then
  S3_REMOTE=$(echo "$SRC_PATH" | sed 's|^s3://|s3:|')
else
  S3_REMOTE="$SRC_PATH"
fi

# Write rclone filter file. Excludes win over the include patterns below
# because rclone applies filter rules top-to-bottom.
FILTER_FILE="$RCLONE_DIR/filters.txt"
cat > "$FILTER_FILE" <<'RCLONE_FILTER_EOF'
` + filterFileContents + `RCLONE_FILTER_EOF

echo "Downloading files with filters..."
echo "Filter file contents:"
cat "$FILTER_FILE"
rclone copy "$S3_REMOTE" "$DEST_DIR" --filter-from "$FILTER_FILE"` + maxDepthFlag + `

cd "$DEST_DIR"

# Safety limits for archive extraction
MAX_EXTRACT_SIZE_MB=50000  # 50GB max total extracted size
MAX_FILES=500000           # Max number of extracted files

check_extract_limits() {
  local dir="$1"
  local total_size_kb
  total_size_kb=$(du -sk "$dir" 2>/dev/null | cut -f1)
  local total_size_mb=$((total_size_kb / 1024))
  if [ "$total_size_mb" -gt "$MAX_EXTRACT_SIZE_MB" ]; then
    echo "ERROR: Extracted data exceeds ${MAX_EXTRACT_SIZE_MB}MB limit (${total_size_mb}MB). Aborting."
    exit 1
  fi
  local file_count
  file_count=$(find "$dir" -type f | wc -l)
  if [ "$file_count" -gt "$MAX_FILES" ]; then
    echo "ERROR: Extracted file count exceeds ${MAX_FILES} limit (${file_count}). Aborting."
    exit 1
  fi
}

extract_and_clean() {
  local dir="$1"
  local found_archive=false

  while IFS= read -r zipfile; do
    [ -z "$zipfile" ] && continue
    found_archive=true
    echo "Extracting $zipfile..."
    # Use -j to junk (flatten) paths within the zip, preventing path traversal
    if ! unzip -q -o -j "$zipfile" -d "$(dirname "$zipfile")"; then
      echo "ERROR: Failed to extract zip archive: $zipfile"
      exit 1
    fi
    rm -f "$zipfile"
    check_extract_limits "$dir"
  done <<EOF
$(find "$dir" -type f \( -name "*.zip" -o -name "*.ZIP" \))
EOF

  while IFS= read -r tarfile; do
    [ -z "$tarfile" ] && continue
    found_archive=true
    echo "Extracting $tarfile..."
    # --no-same-owner: don't try to preserve ownership
    # --no-same-permissions: don't try to preserve permissions
    # --no-absolute-filenames: strip leading / to prevent writing outside target
    # --transform: strip leading directory component (like -j for zip)
    if ! tar --no-same-owner --no-same-permissions --no-absolute-filenames --transform='s|.*/||' -xf "$tarfile" -C "$(dirname "$tarfile")" 2>/dev/null; then
      if ! tar --no-same-owner --no-same-permissions --no-absolute-filenames --transform='s|.*/||' -xzf "$tarfile" -C "$(dirname "$tarfile")" 2>/dev/null; then
        echo "ERROR: Failed to extract tar archive: $tarfile"
        exit 1
      fi
    fi
    rm -f "$tarfile"
    check_extract_limits "$dir"
  done <<EOF
$(find "$dir" -type f \( -name "*.tar.gz" -o -name "*.tar" -o -name "*.TAR.GZ" -o -name "*.TAR" \))
EOF

  if [ "$found_archive" = true ]; then
    extract_and_clean "$dir"
  fi
}

echo "Extracting archives..."
extract_and_clean "$DEST_DIR"

echo "Cleaning up non-image files..."
# Delete non-image files, but skip anything in output/odm directories
find "$DEST_DIR" -type f ! \( \
  -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \
\) ! -path "*/output/*" ! -path "*/odm/*" -delete

# Remove empty directories, but skip output/odm directories entirely
find "$DEST_DIR" -type d ! -path "*/output/*" ! -path "*/odm/*" -empty -delete

echo "Flattening directory structure..."
FLAT_DIR="$DEST_DIR"
TEMP_LIST="$DEST_DIR/.flatten-list.txt"

# Find image files, excluding any in output/odm directories
find "$DEST_DIR" -type f \( -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \) \
  ! -path "*/output/*" ! -path "*/odm/*" > "$TEMP_LIST"

while IFS= read -r imgfile; do
  # Skip files in output/odm directories (defensive check)
  if echo "$imgfile" | grep -qE "/(output|odm)/"; then
    continue
  fi

  filename=$(basename "$imgfile")
  if [ "$(dirname "$imgfile")" != "$FLAT_DIR" ]; then
    counter=1
    destfile="$FLAT_DIR/$filename"
    while [ -f "$destfile" ]; do
      namepart="${filename%.*}"
      extpart="${filename##*.}"
      destfile="$FLAT_DIR/${namepart}_${counter}.${extpart}"
      counter=$((counter + 1))
    done
    mv "$imgfile" "$destfile"
    echo "Moved: $imgfile -> $destfile"
  fi
done < "$TEMP_LIST"
rm -f "$TEMP_LIST"

find "$DEST_DIR" -type d -empty -delete

echo "Download and extraction complete. Image files in $DEST_DIR:"
find "$DEST_DIR" -type f | wc -l | xargs echo "Total image files:"
find "$DEST_DIR" -type f \( -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \) | awk 'NR<=10'`
}

// GenerateUploadScript generates a shell script for uploading ODM results to S3
// Credentials are injected via Kubernetes Secret references in the workflow spec
// Note: We create rclone config on-the-fly to avoid ContainerSet env var filtering of RCLONE_CONFIG_*
func GenerateUploadScript(destPath string) string {
	uploadExcludeFlags := renderRcloneExcludeFlags(uploadExcludePatterns)

	return `set -e
set -o pipefail
echo "Running final upload..."

DEST_PATH="` + destPath + `"
JOB_ID="{{workflow.name}}"
SRC_DIR="/workspace/$JOB_ID"

# Create rclone config on-the-fly using AWS env vars (not filtered by ContainerSet)
# This avoids the RCLONE_CONFIG_* env var filtering issue
RCLONE_DIR="$SRC_DIR/.rclone"
mkdir -p "$RCLONE_DIR"
export RCLONE_CONFIG="$RCLONE_DIR/rclone.conf"

` + rcloneS3ConfigSnippet() + `

# Convert s3://bucket/path to s3:bucket/path format for rclone remote
if echo "$DEST_PATH" | grep -q "^s3://"; then
  S3_REMOTE=$(echo "$DEST_PATH" | sed 's|^s3://|s3:|')
else
  S3_REMOTE="$DEST_PATH"
fi

echo "Validating S3 credentials with a test write..."
echo "S3 Remote: $S3_REMOTE"
echo "Destination Path: $DEST_PATH"

# Debug: Check if AWS credentials are available (without exposing values)
if [ -n "$AWS_ACCESS_KEY_ID" ]; then
  echo "AWS_ACCESS_KEY_ID is set (length: ${#AWS_ACCESS_KEY_ID})"
else
  echo "Warning: AWS_ACCESS_KEY_ID is not set"
fi
if [ -n "$AWS_SECRET_ACCESS_KEY" ]; then
  echo "AWS_SECRET_ACCESS_KEY is set (length: ${#AWS_SECRET_ACCESS_KEY})"
else
  echo "Warning: AWS_SECRET_ACCESS_KEY is not set"
fi
echo "AWS_DEFAULT_REGION: ${AWS_DEFAULT_REGION:-not set}"

TEST_FILE="$SRC_DIR/.s3-write-test-local-$(date +%s).txt"
echo "s3 write test $(date)" > "$TEST_FILE"
TEST_OBJECT="$S3_REMOTE/.s3-write-test-$(date +%s)"

echo "Testing write to: $TEST_OBJECT"
if TEST_OUTPUT=$(rclone copyto "$TEST_FILE" "$TEST_OBJECT" 2>&1); then
  echo "Test write successful, cleaning up test object..."
  rclone deletefile "$TEST_OBJECT" 2>&1 || echo "Warning: Failed to delete test object (non-fatal)"
  echo "S3 write access confirmed."
else
  TEST_EXIT_CODE=$?
  echo ""
  echo "Warning: Test write failed (exit code: $TEST_EXIT_CODE)"
  echo "Test output: $TEST_OUTPUT"
  echo ""
  echo "This might be a false positive. Continuing with actual upload..."
  echo "The upload will fail if there are real permission issues."
  echo ""
fi

rm -f "$TEST_FILE"

echo "Job ID: $JOB_ID"
echo "Source: $SRC_DIR"
echo "Destination: $DEST_PATH"

rm -rf "$SRC_DIR/images"

echo "Listing ODM imagery products..."
ls -lh "$SRC_DIR"

echo "Uploading to S3..."
if ! rclone copy "$SRC_DIR" "$S3_REMOTE"` + uploadExcludeFlags + ` --progress; then
  echo "Upload failed."
  exit 1
fi

echo "Upload complete."`
}

// GenerateWorkspaceSnapshotScript prints the final workspace state to stdout.
// Argo archives the cleanup pod logs when archiveLogs is enabled.
func GenerateWorkspaceSnapshotScript() string {
	return `set -u
JOB_ID="${WORKFLOW_NAME:-{{workflow.name}}}"
WORKSPACE_DIR="/workspace/$JOB_ID"

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
echo "Workspace snapshot complete."`
}
