# shellcheck shell=busybox

# setup_rclone DIR
# Writes the "s3" remote config to DIR from AWS_* env vars
# (ContainerSet filters out RCLONE_CONFIG_* env vars).
setup_rclone() {
  mkdir -p "$1"
  export RCLONE_CONFIG="$1/rclone.conf"

  # Provider by host, so an explicit s3.amazonaws.com stays AWS. Mirrors bucketLookupForEndpoint.
  S3_HOST=$(echo "${AWS_S3_ENDPOINT:-}" | sed -e 's|^https\?://||' -e 's|/.*$||' -e 's|:.*$||')
  case "$S3_HOST" in
    ""|amazonaws.com|*.amazonaws.com) PROVIDER="AWS" ;;
    *) PROVIDER="Minio" ;;
  esac

  cat > "$RCLONE_CONFIG" <<EOF
[s3]
type = s3
provider = ${PROVIDER}
env_auth = true
region = ${AWS_DEFAULT_REGION:-us-east-1}
no_check_bucket = true
EOF

  if [ "$PROVIDER" = "Minio" ]; then
    cat >> "$RCLONE_CONFIG" <<EOF
endpoint = ${AWS_S3_ENDPOINT}
force_path_style = true
use_path_style = true
EOF
  fi
}

# to_rclone_remote PATH: s3://bucket/path -> s3:bucket/path
to_rclone_remote() {
  echo "$1" | sed 's|^s3://|s3:|'
}
