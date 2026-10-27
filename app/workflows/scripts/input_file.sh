# shellcheck shell=busybox

# fetch_input_file NAME DEST S3_PATH INLINE
# Downloads S3_PATH or writes INLINE to DEST; no-op if both are empty.
# Exits on failure, so ODM never runs without a requested file.
fetch_input_file() {
  local name="$1"
  local dest="$2"
  local s3_path="$3"
  local inline="$4"

  if [ -n "$s3_path" ]; then
    echo "Fetching ODM $name from $s3_path..."
    local tmp="$dest.part"

    # Remove retry leftovers, including directories from prefix fetches.
    rm -rf "$dest" "$tmp"

    # A fresh temp path makes "no transfer" a missing-source error.
    if ! rclone copyto --error-on-no-transfer "$(to_rclone_remote "$s3_path")" "$tmp"; then
      echo "ERROR: $name not found or unreadable at $s3_path" >&2
      rm -rf "$tmp"
      exit 1
    elif [ ! -f "$tmp" ]; then
      # Prefix downloads create directories, which also pass -s.
      echo "ERROR: $name path $s3_path is a prefix, not a single object" >&2
      rm -rf "$tmp"
      exit 1
    elif [ ! -s "$tmp" ]; then
      echo "ERROR: $name at $s3_path is empty" >&2
      rm -rf "$tmp"
      exit 1
    fi
    mv "$tmp" "$dest"
  elif [ -n "$inline" ]; then
    echo "Writing inline ODM $name..."
    printf '%s\n' "$inline" > "$dest"
  else
    return 0
  fi

  echo "$name written to $dest ($(wc -c < "$dest") bytes)"
}
