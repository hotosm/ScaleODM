set dotenv-load

mod test 'tasks/test'
mod docs 'tasks/docs'

# List available commands
[private]
default:
  just help

# List available commands
help:
  just --justfile {{justfile()}} --list

# Prep module from https://github.com/hotosm/justfiles
prep *args:
    @curl -sS https://raw.githubusercontent.com/hotosm/justfiles/main/prep.just \
      -o {{justfile_directory()}}/tasks/prep.just;
    @just --justfile {{justfile_directory()}}/tasks/prep.just {{args}}

# Chart module from https://github.com/hotosm/justfiles
chart *args:
    @curl -sS https://raw.githubusercontent.com/hotosm/justfiles/main/chart.just \
      -o {{justfile_directory()}}/tasks/chart.just;
    @just --justfile {{justfile_directory()}}/tasks/chart.just --set chart_name "scaleodm" {{args}}

# Local Talos + Argo test-cluster lifecycle (see hotosm/justfiles k8s.just).
# Tool prerequisites (curl/jq/talosctl/kubectl) are installed by the module.
# Usage: just k8s cluster-init | cluster-available | cluster-destroy
k8s *args:
    @curl -sS https://raw.githubusercontent.com/hotosm/justfiles/main/k8s.just \
      -o {{justfile_directory()}}/tasks/k8s.just;
    @just --justfile {{justfile_directory()}}/tasks/k8s.just \
      --set cluster_name "scaleodm-test" \
      --set namespace "argo" \
      --set secret_name "scaleodm-secrets" \
      {{args}}

# Start compose services (DB, S3, API)
# Assumes Talos cluster is already running
start:
  #!/usr/bin/env bash
  set -e
  echo "Starting API..."
  docker compose up -d

# Stop compose services
stop:
  docker compose down --remove-orphans

# Setup Talos cluster and start all services for development
dev:
  just test cluster-init
  just start

# Verify chart version, appVersion, and any literal x.y.z strings in source
# files are aligned. Run before tagging a release. Pass an expected version
# (e.g. `just check-versions 0.4.0`) to compare against; in CI, the recipe
# also auto-derives the expected version from $GITHUB_REF when it points at
# a tag of the form `refs/tags/vX.Y.Z`. With no argument and no triggering
# tag the recipe just compares chart version vs appVersion and scans for
# stray literals.
check-versions expected="":
  #!/usr/bin/env bash
  set -euo pipefail

  # Extract the two top-level fields without requiring yq on the PATH; both
  # `version` and `appVersion` are simple scalars at column 0 of Chart.yaml.
  read_top_yaml_field() {
    awk -v key="$1" '
      $0 ~ "^" key ":" {
        sub("^" key ":[[:space:]]*", "", $0)
        gsub(/^["'\'']|["'\'']$/, "", $0)
        print
        exit
      }
    ' chart/Chart.yaml
  }
  CHART_VERSION="$(read_top_yaml_field version)"
  CHART_APP_VERSION="$(read_top_yaml_field appVersion)"

  EXPECTED="{{ expected }}"
  if [ -z "$EXPECTED" ] && [ -n "${GITHUB_REF:-}" ]; then
    case "$GITHUB_REF" in
      refs/tags/v*) EXPECTED="${GITHUB_REF#refs/tags/v}" ;;
      refs/tags/*)  EXPECTED="${GITHUB_REF#refs/tags/}" ;;
    esac
  fi

  echo "chart version:    $CHART_VERSION"
  echo "chart appVersion: $CHART_APP_VERSION"
  if [ -n "$EXPECTED" ]; then
    echo "expected:         $EXPECTED"
  fi

  fail=0
  if [ "$CHART_VERSION" != "$CHART_APP_VERSION" ]; then
    echo "drift: chart version != appVersion"
    fail=1
  fi

  if [ -n "$EXPECTED" ] && [ "$CHART_VERSION" != "$EXPECTED" ]; then
    echo "drift: chart version $CHART_VERSION != expected $EXPECTED"
    fail=1
  fi

  # Hunt stray x.y.z literals in source. The version package, ldflags, and
  # generated/lock files are excluded from the search.
  echo
  echo "Scanning source for stray x.y.z version literals..."
  hits="$(grep -RnE '"[0-9]+\.[0-9]+\.[0-9]+"' \
    --include='*.go' --include='*.yaml' --include='*.yml' --include='*.md' --include='Justfile' \
    --exclude-dir='.git' --exclude-dir='charts' --exclude-dir='vendor' --exclude-dir='site' \
    --exclude='Chart.yaml' --exclude='Chart.lock' --exclude='go.sum' --exclude='uv.lock' \
    . || true)"

  if [ -n "$hits" ]; then
    echo "$hits"
    echo
    echo "Review the above hits. If any are version strings that should be derived"
    echo "from the chart appVersion or the version package, update them or add an"
    echo "exclude to this recipe."
  fi

  exit "$fail"

# Seed local RustFS with example imagery from public bucket
[private]
_seed-example-imagery:
  #!/usr/bin/env bash
  set -euo pipefail

  echo "Ensuring local S3 service is running..."
  docker compose up --detach s3

  echo "Initializing RustFS bucket/credentials..."
  AWS_ACCESS_KEY_ID=admin \
  AWS_SECRET_ACCESS_KEY=somelongpassword \
  docker compose run --rm s3-init

  echo "Verifying local RustFS key access..."
  docker run --rm \
    --network host \
    -e AWS_S3_ENDPOINT=${SCALEODM_LOCAL_S3_ENDPOINT:-http://localhost:31102} \
    -e AWS_ACCESS_KEY_ID=admin \
    -e AWS_SECRET_ACCESS_KEY=somelongpassword \
    --entrypoint /bin/sh \
    docker.io/rclone/rclone:1.69 \
    -eu -c '
      printf "%s\n" \
        "[local]" \
        "type = s3" \
        "provider = Minio" \
        "env_auth = false" \
        "access_key_id = ${AWS_ACCESS_KEY_ID}" \
        "secret_access_key = ${AWS_SECRET_ACCESS_KEY}" \
        "region = us-east-1" \
        "endpoint = ${AWS_S3_ENDPOINT}" \
        "force_path_style = true" \
        "use_path_style = true" \
        > /tmp/rclone-check.conf
      rclone --config /tmp/rclone-check.conf lsd local:scaleodm-test
    '

  echo "Seeding example imagery into local RustFS..."
  docker run --rm \
    --network host \
    -e AWS_S3_ENDPOINT=${SCALEODM_LOCAL_S3_ENDPOINT:-http://localhost:31102} \
    -e AWS_ACCESS_KEY_ID=admin \
    -e AWS_SECRET_ACCESS_KEY=somelongpassword \
    --entrypoint /bin/sh \
    docker.io/rclone/rclone:1.69 \
    -eu -c '
      printf "%s\n" \
        "[public]" \
        "type = s3" \
        "provider = AWS" \
        "env_auth = false" \
        "anonymous = true" \
        "region = us-east-1" \
        "endpoint = https://s3.amazonaws.com" \
        "" \
        "[local]" \
        "type = s3" \
        "provider = Minio" \
        "env_auth = false" \
        "access_key_id = ${AWS_ACCESS_KEY_ID}" \
        "secret_access_key = ${AWS_SECRET_ACCESS_KEY}" \
        "region = us-east-1" \
        "endpoint = ${AWS_S3_ENDPOINT}" \
        "force_path_style = true" \
        "use_path_style = true" \
        > /tmp/rclone.conf

      rclone --config /tmp/rclone.conf sync \
        public:dronetm-testdata/freetown-mini/ \
        local:scaleodm-test/test/ \
        --create-empty-src-dirs \
        --progress
    '

# Unlike example-python/pyodm/curl this recipe does NOT set anything up: it
# assumes the Talos cluster, seeded imagery, workflow S3 secret, and API are
# already running (e.g. bring them up once via the setup at the top of
# `just example-python`, then run this against the live stack). It also passes
# s3Endpoint=localhost:31102 by default, which only works when the workflow pod
# can reach the host on that address.
#
# Run the manual workflow example (loads .env automatically via dotenv-load).
example-manual:
  go run examples/manual_workflow.go

# Example the API usage with curl
example-curl:
  #!/usr/bin/env bash
  set -euo pipefail
  trap 'docker compose down --remove-orphans' EXIT

  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=somelongpassword just test cluster-available
  just test build-api-image
  docker compose down --remove-orphans
  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=somelongpassword just _seed-example-imagery

  if [ -z "${SCALEODM_WORKFLOW_S3_ENDPOINT:-}" ]; then
    TALOS_GATEWAY_IP=$(docker network inspect scaleodm-test 2>/dev/null | jq -r '.[0].IPAM.Config[0].Gateway // ""' 2>/dev/null || true)
    if [ -n "$TALOS_GATEWAY_IP" ]; then
      SCALEODM_WORKFLOW_S3_ENDPOINT="http://${TALOS_GATEWAY_IP}:31102"
    else
      SCALEODM_WORKFLOW_S3_ENDPOINT="http://host.docker.internal:31102"
    fi
  fi

  API_URL="${SCALEODM_BASE_URL:-http://localhost:31100}"
  READ_S3_PATH="s3://scaleodm-test/test/"
  WRITE_S3_PATH="s3://scaleodm-test/test/output/"
  export READ_S3_PATH WRITE_S3_PATH SCALEODM_WORKFLOW_S3_ENDPOINT

  echo "Using ScaleODM API: ${API_URL}"
  echo "Using workflow S3 endpoint: ${SCALEODM_WORKFLOW_S3_ENDPOINT}"

  echo "Ensuring workflow S3 secret uses local test credentials..."
  kubectl create secret generic scaleodm-secrets -n ${K8S_NAMESPACE:-argo} \
    --from-literal=AWS_ACCESS_KEY_ID=admin \
    --from-literal=AWS_SECRET_ACCESS_KEY=somelongpassword \
    --from-literal=AWS_DEFAULT_REGION=us-east-1 \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "Starting API..."
  docker compose up --wait --detach --force-recreate api

  PAYLOAD=$(jq -n \
    --arg name "curl-test-project" \
    --arg readS3Path "$READ_S3_PATH" \
    --arg writeS3Path "$WRITE_S3_PATH" \
    --arg s3Endpoint "$SCALEODM_WORKFLOW_S3_ENDPOINT" \
    --arg s3Region "us-east-1" \
    --arg processingMode "standard" \
    --argjson s3ScanDepth 1 \
    --arg options '[{"name":"fast-orthophoto","value":true}]' \
    '{name: $name, readS3Path: $readS3Path, writeS3Path: $writeS3Path, s3Endpoint: $s3Endpoint, s3Region: $s3Region, processingMode: $processingMode, s3ScanDepth: $s3ScanDepth, options: $options}')

  echo "Creating task with curl..."
  CREATE_RESPONSE=$(curl -fsS \
    -X POST "${API_URL}/task/new" \
    -H "Content-Type: application/json" \
    --data "${PAYLOAD}")
  echo "Create response: ${CREATE_RESPONSE}"

  UUID=$(printf "%s" "${CREATE_RESPONSE}" | jq -r '.uuid // ""')
  if [ -z "$UUID" ]; then
    echo "Failed to find task UUID in create response"
    exit 1
  fi

  echo "Created task: ${UUID}"
  echo "Polling task status..."

  MAX_POLLS="${SCALEODM_EXAMPLE_MAX_POLLS:-120}"
  POLL_INTERVAL="${SCALEODM_EXAMPLE_POLL_INTERVAL:-60}"
  for _ in $(seq 1 "$MAX_POLLS"); do
    INFO_RESPONSE=$(curl -fsS "${API_URL}/task/${UUID}/info")
    STATUS_CODE=$(printf "%s" "${INFO_RESPONSE}" | jq -r '.status.code // ""')
    PROGRESS=$(printf "%s" "${INFO_RESPONSE}" | jq -r '.progress // 0')

    echo "Status: ${STATUS_CODE} progress=${PROGRESS}%"

    case "$STATUS_CODE" in
      40)
        echo "Task completed successfully."
        break
        ;;
      30)
        echo "Task failed."
        curl -fsS "${API_URL}/task/${UUID}/output?line=0" || true
        exit 1
        ;;
      50)
        echo "Task was canceled."
        exit 1
        ;;
    esac

    sleep "$POLL_INTERVAL"
  done

  if [ "$STATUS_CODE" != "40" ]; then
    echo "Task did not complete after $MAX_POLLS polls."
    exit 1
  fi

  echo "Listing tasks..."
  curl -fsS "${API_URL}/task/list"
  echo

  echo "Fetching final task info..."
  curl -fsS "${API_URL}/task/${UUID}/info"
  echo

  echo "Saving task logs to file..."
  LOG_FILE="/tmp/scaleodm-example-${UUID}.log"
  curl -fsS "${API_URL}/task/${UUID}/output?line=0" > "$LOG_FILE"
  just _log-preview "$LOG_FILE"

  # "orthophoto" is the download alias the API resolves to the real object key
  # (odm_orthophoto/odm_orthophoto.tif); a bare "orthophoto.tif" key does not
  # exist and 404s. Expect a 302 redirect to a pre-signed URL.
  echo "Checking download redirect for orthophoto..."
  curl -fsSI "${API_URL}/task/${UUID}/download/orthophoto" | sed -n '1,20p'

  echo "Shutting down containers..."
  docker compose down --remove-orphans

# Example the API usage via Python script
example-python:
  #!/usr/bin/env bash
  set -euo pipefail
  trap 'docker compose down --remove-orphans' EXIT

  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=somelongpassword just test cluster-available
  just test build-api-image
  docker compose down --remove-orphans
  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=somelongpassword just _seed-example-imagery

  if [ -z "${SCALEODM_WORKFLOW_S3_ENDPOINT:-}" ]; then
    TALOS_GATEWAY_IP=$(docker network inspect scaleodm-test 2>/dev/null | python3 -c 'import json,sys; data=json.load(sys.stdin); print((data[0].get("IPAM",{}).get("Config",[{}])[0].get("Gateway", "")) if data else "", end="")' 2>/dev/null || true)
    if [ -n "$TALOS_GATEWAY_IP" ]; then
      SCALEODM_WORKFLOW_S3_ENDPOINT="http://${TALOS_GATEWAY_IP}:31102"
    else
      SCALEODM_WORKFLOW_S3_ENDPOINT="http://host.docker.internal:31102"
    fi
  fi

  echo "Using workflow S3 endpoint: ${SCALEODM_WORKFLOW_S3_ENDPOINT}"

  echo "Ensuring workflow S3 secret uses local test credentials..."
  kubectl create secret generic scaleodm-secrets -n ${K8S_NAMESPACE:-argo} \
    --from-literal=AWS_ACCESS_KEY_ID=admin \
    --from-literal=AWS_SECRET_ACCESS_KEY=somelongpassword \
    --from-literal=AWS_DEFAULT_REGION=us-east-1 \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "Starting API..."
  docker compose up --wait --detach --force-recreate api

  UUID_FILE=$(mktemp)
  trap 'rm -f "$UUID_FILE"; docker compose down --remove-orphans' EXIT

  echo "Running Python API test inside container..."
  docker run --rm \
    --network host \
    -e SCALEODM_WORKFLOW_S3_ENDPOINT=${SCALEODM_WORKFLOW_S3_ENDPOINT} \
    -e SCALEODM_EXAMPLE_UUID_FILE=/tmp/scaleodm-example-uuid \
    -v "$UUID_FILE:/tmp/scaleodm-example-uuid" \
    -v "$PWD/examples/python:/app" \
    --workdir /app \
    -e PYTHONDONTWRITEBYTECODE=1 \
    -e PYTHONUNBUFFERED=1 \
    -e PYTHONFAULTHANDLER=1 \
    docker.io/python:3.13-slim-trixie \
    bash -lc '
      set -euo pipefail
      python -V
      pip install --no-cache-dir uv
      uv sync
      uv run python main.py
    '

  UUID=$(cat "$UUID_FILE")
  just _verify-archived-logs "$UUID"

  echo "Shutting down containers..."
  docker compose down --remove-orphans

# Example the API usage via pyodm SDK
example-pyodm:
  #!/usr/bin/env bash
  set -euo pipefail
  trap 'docker compose down --remove-orphans' EXIT

  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=somelongpassword just test cluster-available
  just test build-api-image
  docker compose down --remove-orphans
  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=somelongpassword just _seed-example-imagery

  if [ -z "${SCALEODM_WORKFLOW_S3_ENDPOINT:-}" ]; then
    TALOS_GATEWAY_IP=$(docker network inspect scaleodm-test 2>/dev/null | python3 -c 'import json,sys; data=json.load(sys.stdin); print((data[0].get("IPAM",{}).get("Config",[{}])[0].get("Gateway", "")) if data else "", end="")' 2>/dev/null || true)
    if [ -n "$TALOS_GATEWAY_IP" ]; then
      SCALEODM_WORKFLOW_S3_ENDPOINT="http://${TALOS_GATEWAY_IP}:31102"
    else
      SCALEODM_WORKFLOW_S3_ENDPOINT="http://host.docker.internal:31102"
    fi
  fi

  echo "Using workflow S3 endpoint: ${SCALEODM_WORKFLOW_S3_ENDPOINT}"

  echo "Ensuring workflow S3 secret uses local test credentials..."
  kubectl create secret generic scaleodm-secrets -n ${K8S_NAMESPACE:-argo} \
    --from-literal=AWS_ACCESS_KEY_ID=admin \
    --from-literal=AWS_SECRET_ACCESS_KEY=somelongpassword \
    --from-literal=AWS_DEFAULT_REGION=us-east-1 \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "Starting API..."
  docker compose up --wait --detach --force-recreate api

  UUID_FILE=$(mktemp)
  trap 'rm -f "$UUID_FILE"; docker compose down --remove-orphans' EXIT

  echo "Running pyodm example inside container..."
  docker run --rm \
    --network host \
    -e SCALEODM_WORKFLOW_S3_ENDPOINT=${SCALEODM_WORKFLOW_S3_ENDPOINT} \
    -e SCALEODM_EXAMPLE_UUID_FILE=/tmp/scaleodm-example-uuid \
    -v "$UUID_FILE:/tmp/scaleodm-example-uuid" \
    -v "$PWD/examples/pyodm:/app" \
    --workdir /app \
    -e PYTHONDONTWRITEBYTECODE=1 \
    -e PYTHONUNBUFFERED=1 \
    -e PYTHONFAULTHANDLER=1 \
    docker.io/python:3.13-slim-trixie \
    bash -lc '
      set -euo pipefail
      python -V
      pip install --no-cache-dir uv
      uv sync
      uv run python main.py
    '

  UUID=$(cat "$UUID_FILE")
  just _verify-archived-logs "$UUID"

  echo "Shutting down containers..."
  docker compose down --remove-orphans

[private]
_verify-archived-logs uuid:
  #!/usr/bin/env bash
  set -euo pipefail

  API_URL="${SCALEODM_BASE_URL:-http://localhost:31100}"
  NAMESPACE="${K8S_NAMESPACE:-argo}"
  LOG_FILE="/tmp/scaleodm-example-{{ uuid }}.log"

  echo "Verifying archived log fallback for workflow {{ uuid }}..."
  kubectl delete workflow -n "$NAMESPACE" "{{ uuid }}" --ignore-not-found=true

  for _ in $(seq 1 30); do
    if ! kubectl get workflow -n "$NAMESPACE" "{{ uuid }}" >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done

  curl -fsS "${API_URL}/task/{{ uuid }}/output?line=0" > "$LOG_FILE"

  # With the workflow CR deleted, /output must fall back to the Argo S3 log
  # archive (see GetWorkflowLogsWithArchiveFallback). Log capture now uses
  # Argo's built-in archive rather than the old custom aggregation, so the
  # fallback streams the raw process.log. The two strings below are the only
  # non-log sentinels that path can emit, so their presence (or an empty body)
  # means the archive fallback did not actually serve logs.
  if ! [ -s "$LOG_FILE" ]; then
    echo "Archived log fallback returned an empty body"
    exit 1
  fi
  if grep -Fq "Archived logs not configured." "$LOG_FILE" \
     || grep -Fq "No archived logs for this workflow." "$LOG_FILE"; then
    echo "Archived log fallback did not read from the Argo archive:"
    head -n 5 "$LOG_FILE"
    exit 1
  fi

  LOG_LINES=$(wc -l < "$LOG_FILE" | tr -d ' ')
  echo "Archived log fallback verified (${LOG_LINES} lines)."
  just _log-preview "$LOG_FILE"

# Print a head+tail preview of a (potentially huge) log file instead of dumping
# the whole thing to the terminal. `lines` is per end, so the default shows up
# to 200 lines total. The full log stays on disk at the given path.
[private]
[no-cd]
_log-preview file lines="100":
  #!/usr/bin/env bash
  set -euo pipefail
  FILE="{{ file }}"
  LINES="{{ lines }}"
  TOTAL=$(wc -l < "$FILE" | tr -d ' ')
  echo "----- $FILE (${TOTAL} lines; showing first/last ${LINES}) -----"
  if [ "$TOTAL" -le $(( LINES * 2 )) ]; then
    cat "$FILE"
  else
    head -n "$LINES" "$FILE"
    echo "         ... [$(( TOTAL - LINES * 2 )) lines omitted] ..."
    tail -n "$LINES" "$FILE"
  fi
  echo "----- end preview; full log: $FILE -----"

# Echo to terminal with blue colour
[no-cd]
_echo-blue text:
  #!/usr/bin/env sh
  printf "\033[0;34m%s\033[0m\n" "{{ text }}"

# Echo to terminal with yellow colour
[no-cd]
_echo-yellow text:
  #!/usr/bin/env sh
  printf "\033[0;33m%s\033[0m\n" "{{ text }}"

# Echo to terminal with red colour
[no-cd]
_echo-red text:
  #!/usr/bin/env sh
  printf "\033[0;41m%s\033[0m\n" "{{ text }}"
