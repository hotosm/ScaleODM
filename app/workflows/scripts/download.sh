# shellcheck shell=busybox
# Download step: fetch imagery, extract archives, flatten images into
# $WORKSPACE/$JOB_ID/images, then fetch any boundary/cameras file.
set -e
set -o pipefail

# Inputs (set in scripts.go). BOUNDARY_* and CAMERAS_* are optional.
: "${ATTEMPT:?}" "${WORKSPACE:?}" "${JOB_ID:?}" "${SRC_PATH:?}" "${FILTER_RULES:?}" "${MAX_DEPTH:?}"

echo "=== download attempt $ATTEMPT @ $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
echo "Downloading imagery from S3..."
DEST_DIR="$WORKSPACE/$JOB_ID/images"

echo "Job ID: $JOB_ID"
echo "Source: $SRC_PATH"
echo "Destination: $DEST_DIR"
mkdir -p "$DEST_DIR"

RCLONE_DIR="$WORKSPACE/$JOB_ID/.rclone"
setup_rclone "$RCLONE_DIR"
S3_REMOTE=$(to_rclone_remote "$SRC_PATH")

FILTER_FILE="$RCLONE_DIR/filters.txt"
printf '%s' "$FILTER_RULES" > "$FILTER_FILE"

echo "Downloading files with filters..."
echo "Filter file contents:"
cat "$FILTER_FILE"
# MAX_DEPTH 0 means no limit.
if [ "$MAX_DEPTH" -gt 0 ]; then
  set -- --max-depth "$MAX_DEPTH"
else
  set --
fi
rclone copy "$S3_REMOTE" "$DEST_DIR" --filter-from "$FILTER_FILE" "$@"

cd "$DEST_DIR"

# Safety limits for archive extraction
MAX_EXTRACT_SIZE_MB=50000 # 50GB max total extracted size
MAX_FILES=500000          # Max number of extracted files

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
find "$DEST_DIR" -type f \( -iname "*.jpg" -o -iname "*.jpeg" -o -iname "*.tiff" -o -iname "*.tif" \) | awk 'NR<=10'

fetch_input_file boundary "${BOUNDARY_DEST:-}" "${BOUNDARY_S3_PATH:-}" "${BOUNDARY_INLINE:-}"
fetch_input_file cameras "${CAMERAS_DEST:-}" "${CAMERAS_S3_PATH:-}" "${CAMERAS_INLINE:-}"
