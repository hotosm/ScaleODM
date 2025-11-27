package s3

// File handling utilities for S3-based workflows
// These functions generate shell scripts for use in Argo workflow containers

// GenerateDownloadScript generates a shell script for downloading and processing imagery from S3
// Credentials are always required and configured via AWS environment variables
// Note: We create rclone config on-the-fly to avoid ContainerSet env var filtering of RCLONE_CONFIG_*
func GenerateDownloadScript(jobID, srcPath string) string {
	return `set -e
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
mkdir -p /config/rclone
RCLONE_CONFIG=/config/rclone/rclone.conf

cat > "$RCLONE_CONFIG" <<EOF
[s3]
type = s3
provider = AWS
env_auth = true
region = ${AWS_DEFAULT_REGION:-us-east-1}
EOF

# Convert s3://bucket/path to s3:bucket/path format for rclone remote
if echo "$SRC_PATH" | grep -q "^s3://"; then
  S3_REMOTE=$(echo "$SRC_PATH" | sed 's|^s3://|s3:|')
else
  S3_REMOTE="$SRC_PATH"
fi

echo "Downloading files with filters..."
# Use --filter instead of --include/--exclude for deterministic ordering
# Order matters: exclusions first, then inclusions, then exclude everything else
rclone copy "$S3_REMOTE" "$DEST_DIR" \
  --filter "- output/**" \
  --filter "- **/output/**" \
  --filter "+ *.jpg" \
  --filter "+ *.jpeg" \
  --filter "+ *.JPG" \
  --filter "+ *.JPEG" \
  --filter "+ *.tiff" \
  --filter "+ *.tif" \
  --filter "+ *.TIFF" \
  --filter "+ *.TIF" \
  --filter "+ *.zip" \
  --filter "+ *.ZIP" \
  --filter "+ *.tar.gz" \
  --filter "+ *.tar" \
  --filter "+ *.TAR.GZ" \
  --filter "+ *.TAR" \
  --filter "- *" \

cd "$DEST_DIR"

extract_and_clean() {
  local dir="$1"
  local found_archive=false
  
  find "$dir" -type f \( -name "*.zip" -o -name "*.ZIP" \) | while read zipfile; do
    found_archive=true
    echo "Extracting $zipfile..."
    unzip -q "$zipfile" -d "$(dirname "$zipfile")" || true
    rm -f "$zipfile"
  done
  
  find "$dir" -type f \( -name "*.tar.gz" -o -name "*.tar" -o -name "*.TAR.GZ" -o -name "*.TAR" \) | while read tarfile; do
    found_archive=true
    echo "Extracting $tarfile..."
    tar -xzf "$tarfile" -C "$(dirname "$tarfile")" 2>/dev/null || tar -xf "$tarfile" -C "$(dirname "$tarfile")" 2>/dev/null || true
    rm -f "$tarfile"
  done
  
  if [ "$found_archive" = true ]; then
    extract_and_clean "$dir"
  fi
}

echo "Extracting archives..."
extract_and_clean "$DEST_DIR"

echo "Cleaning up non-image files..."
# Delete non-image files, but skip anything in output directories
find "$DEST_DIR" -type f ! \( \
  -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \
\) ! -path "*/output/*" -delete

# Remove empty directories, but skip output directories entirely
find "$DEST_DIR" -type d ! -path "*/output/*" -empty -delete

echo "Flattening directory structure..."
FLAT_DIR="$DEST_DIR"
TEMP_LIST=$(mktemp)

# Find image files, excluding any in output directories
find "$DEST_DIR" -type f \( -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \) \
  ! -path "*/output/*" > "$TEMP_LIST"

while IFS= read -r imgfile; do
  # Skip files in output directories (defensive check)
  if echo "$imgfile" | grep -q "/output/"; then
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
find "$DEST_DIR" -type f \( -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \) | head -10`
}

// GenerateUploadScript generates a shell script for uploading ODM results to S3
// Credentials are always required and configured via AWS environment variables
// Note: We create rclone config on-the-fly to avoid ContainerSet env var filtering of RCLONE_CONFIG_*
func GenerateUploadScript(destPath string) string {
	return `set -e
echo "Running final upload..."

DEST_PATH="` + destPath + `"

# Create rclone config on-the-fly using AWS env vars (not filtered by ContainerSet)
# This avoids the RCLONE_CONFIG_* env var filtering issue
mkdir -p /config/rclone
RCLONE_CONFIG=/config/rclone/rclone.conf

cat > "$RCLONE_CONFIG" <<EOF
[s3]
type = s3
provider = AWS
env_auth = true
region = ${AWS_DEFAULT_REGION:-us-east-1}
EOF

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
  echo "⚠️  Warning: AWS_ACCESS_KEY_ID is not set"
fi
if [ -n "$AWS_SECRET_ACCESS_KEY" ]; then
  echo "AWS_SECRET_ACCESS_KEY is set (length: ${#AWS_SECRET_ACCESS_KEY})"
else
  echo "⚠️  Warning: AWS_SECRET_ACCESS_KEY is not set"
fi
if [ -n "$AWS_SESSION_TOKEN" ]; then
  echo "AWS_SESSION_TOKEN is set (using STS credentials)"
else
  echo "AWS_SESSION_TOKEN is not set (using static credentials)"
fi
echo "AWS_DEFAULT_REGION: ${AWS_DEFAULT_REGION:-not set}"

TEST_FILE="$(mktemp)"
echo "s3 write test $(date)" > "$TEST_FILE"
TEST_OBJECT="$S3_REMOTE/.s3-write-test-$(date +%s)"

echo "Testing write to: $TEST_OBJECT"
# Try to upload test file (without metadata flag which might cause issues)
# Capture both stdout and stderr for debugging
TEST_OUTPUT=$(rclone copyto "$TEST_FILE" "$TEST_OBJECT" 2>&1)
TEST_EXIT_CODE=$?

if [ $TEST_EXIT_CODE -ne 0 ]; then
  echo ""
  echo "⚠️  Warning: Test write failed (exit code: $TEST_EXIT_CODE)"
  echo "Test output: $TEST_OUTPUT"
  echo ""
  echo "This might be a false positive. Continuing with actual upload..."
  echo "The upload will fail if there are real permission issues."
  echo ""
  # Don't exit - let the actual upload try and fail if needed
else
  echo "Test write successful, cleaning up test object..."
  rclone deletefile "$TEST_OBJECT" 2>&1 || echo "Warning: Failed to delete test object (non-fatal)"
  echo "✅ S3 write access confirmed."
fi

rm -f "$TEST_FILE"

JOB_ID="{{workflow.name}}"
SRC_DIR="/workspace/$JOB_ID"

echo "Job ID: $JOB_ID"
echo "Source: $SRC_DIR"
echo "Destination: $DEST_PATH"

rm -rf "$SRC_DIR/images"

echo "Listing ODM imagery products..."
ls -lh "$SRC_DIR"

echo "Uploading to S3..."
if ! rclone copy "$SRC_DIR" "$S3_REMOTE" --progress; then
  echo "❌ Upload failed."
  exit 1
fi

echo "✅ Upload complete."`
}

// GenerateLogUploadScript generates a script to collect workflow logs and upload to S3
// This runs after the main workflow completes to preserve logs before cleanup
// Collects logs from download, process, and upload stages, plus any ODM-generated log files
func GenerateLogUploadScript(destPath string) string {
	return `set -e
echo "Collecting workflow logs..."

DEST_PATH="` + destPath + `"

# Create rclone config on-the-fly using AWS env vars (not filtered by ContainerSet)
mkdir -p /config/rclone
RCLONE_CONFIG=/config/rclone/rclone.conf

cat > "$RCLONE_CONFIG" <<EOF
[s3]
type = s3
provider = AWS
env_auth = true
region = ${AWS_DEFAULT_REGION:-us-east-1}
EOF

JOB_ID="{{workflow.name}}"
WORKSPACE_DIR="/workspace/$JOB_ID"
LOG_FILE="/tmp/workflow-logs.txt"

# Collect logs from all containers and combine into single log file
echo "=== Workflow Logs for $JOB_ID ===" > "$LOG_FILE"
echo "Workflow Name: $JOB_ID" >> "$LOG_FILE"
echo "Collected at: $(date -u +"%Y-%m-%d %H:%M:%S UTC")" >> "$LOG_FILE"
echo "" >> "$LOG_FILE"

# Collect download stage logs
if [ -f "$WORKSPACE_DIR/.download.log" ]; then
  echo "=== Download Stage Logs ===" >> "$LOG_FILE"
  cat "$WORKSPACE_DIR/.download.log" >> "$LOG_FILE"
  echo "" >> "$LOG_FILE"
else
  echo "=== Download Stage Logs ===" >> "$LOG_FILE"
  echo "Download log file not found" >> "$LOG_FILE"
  echo "" >> "$LOG_FILE"
fi

# Collect process (ODM) stage logs
if [ -f "$WORKSPACE_DIR/.process.log" ]; then
  echo "=== Process (ODM) Stage Logs ===" >> "$LOG_FILE"
  cat "$WORKSPACE_DIR/.process.log" >> "$LOG_FILE"
  echo "" >> "$LOG_FILE"
else
  echo "=== Process (ODM) Stage Logs ===" >> "$LOG_FILE"
  echo "Process log file not found" >> "$LOG_FILE"
  echo "" >> "$LOG_FILE"
fi

# Collect any ODM-generated log files from the project directory
if [ -d "$WORKSPACE_DIR/$JOB_ID" ]; then
  echo "=== ODM-Generated Log Files ===" >> "$LOG_FILE"
  # Find and include ODM log files
  find "$WORKSPACE_DIR/$JOB_ID" -name "*_log.txt" -o -name "*.log" | while read logfile; do
    echo "--- $(basename "$logfile") ---" >> "$LOG_FILE"
    cat "$logfile" >> "$LOG_FILE" 2>/dev/null || echo "Failed to read log file" >> "$LOG_FILE"
    echo "" >> "$LOG_FILE"
  done
fi

# Collect upload stage logs
if [ -f "$WORKSPACE_DIR/.upload.log" ]; then
  echo "=== Upload Stage Logs ===" >> "$LOG_FILE"
  cat "$WORKSPACE_DIR/.upload.log" >> "$LOG_FILE"
  echo "" >> "$LOG_FILE"
else
  echo "=== Upload Stage Logs ===" >> "$LOG_FILE"
  echo "Upload log file not found" >> "$LOG_FILE"
  echo "" >> "$LOG_FILE"
fi

# Convert s3://bucket/path to s3:bucket/path format for rclone remote
if echo "$DEST_PATH" | grep -q "^s3://"; then
  S3_REMOTE=$(echo "$DEST_PATH" | sed 's|^s3://|s3:|')
else
  S3_REMOTE="$DEST_PATH"
fi

LOG_OBJECT="$S3_REMOTE/.workflow-logs.txt"

echo "Uploading workflow logs to S3..."
if rclone copyto "$LOG_FILE" "$LOG_OBJECT" --progress; then
  echo "✅ Workflow logs uploaded to: s3://$(echo "$LOG_OBJECT" | sed 's|^s3:||')"
else
  echo "⚠️  Warning: Failed to upload workflow logs to S3"
  # Don't fail the workflow if log upload fails
fi

rm -f "$LOG_FILE"
echo "Log collection complete."`
}
