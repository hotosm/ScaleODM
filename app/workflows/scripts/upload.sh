# shellcheck shell=busybox
# Upload step: copy ODM outputs from $WORKSPACE/$JOB_ID to DEST_PATH.
set -e
set -o pipefail

# Inputs (set in scripts.go).
: "${ATTEMPT:?}" "${WORKSPACE:?}" "${JOB_ID:?}" "${DEST_PATH:?}"

echo "=== upload attempt $ATTEMPT @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
echo "Running final upload..."
SRC_DIR="$WORKSPACE/$JOB_ID"

setup_rclone "$SRC_DIR/.rclone"
S3_REMOTE=$(to_rclone_remote "$DEST_PATH")

echo "Validating S3 credentials with a test write..."
echo "S3 Remote: $S3_REMOTE"
echo "Destination Path: $DEST_PATH"

# Log whether credentials are set, without their values.
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
# Skip undistorted images: a full-size copy of every input photo.
if ! rclone copy "$SRC_DIR" "$S3_REMOTE" \
  --exclude ".rclone/**" \
  --exclude "opensfm/undistorted/**" \
  --exclude "**/opensfm/undistorted/**" \
  --progress; then
  echo "Upload failed."
  exit 1
fi

echo "Upload complete."
