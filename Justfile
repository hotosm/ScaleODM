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

# Install curl if missing
[private]
_install-curl:
  #!/usr/bin/env bash
  set -e
  
  if ! command -v curl &> /dev/null; then
      echo "📦 Installing curl..."
      if command -v apt-get &> /dev/null; then
          sudo apt-get update -qq && sudo apt-get install -y curl
      elif command -v yum &> /dev/null; then
          sudo yum install -y curl
      elif command -v apk &> /dev/null; then
          sudo apk add --no-cache curl
      else
          echo "❌ Error: curl is not installed and no package manager found"
          echo "   Please install curl manually"
          exit 1
      fi
      echo "✓ curl installed"
  fi

# Install talosctl if missing
[private]
_install-talosctl:
  #!/usr/bin/env bash
  set -e
  
  if ! command -v talosctl &> /dev/null; then
      echo "📦 Installing talosctl..."
      curl -sL https://talos.dev/install | sh
      # Ensure its in PATH (install script usually adds to ~/.local/bin)
      if [ -f "$HOME/.local/bin/talosctl" ]; then
          export PATH="$HOME/.local/bin:$PATH"
      fi
      # Verify installation
      if ! command -v talosctl &> /dev/null; then
          echo "❌ Error: Failed to install talosctl"
          exit 1
      fi
      echo "✓ talosctl installed"
  fi

# Install kubectl if missing
[private]
_install-kubectl:
  #!/usr/bin/env bash
  set -e
  
  if ! command -v kubectl &> /dev/null; then
      echo "📦 Installing kubectl..."
      # Download latest stable kubectl
      KUBECTL_VERSION=$(curl -L -s https://dl.k8s.io/release/stable.txt)
      curl -LO "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/amd64/kubectl"
      chmod +x kubectl
      # Try to install to system path, fallback to user local bin
      if sudo mv kubectl /usr/local/bin/kubectl 2>/dev/null; then
          echo "✓ kubectl installed to /usr/local/bin"
      else
          mkdir -p "$HOME/.local/bin"
          mv kubectl "$HOME/.local/bin/kubectl"
          export PATH="$HOME/.local/bin:$PATH"
          echo "✓ kubectl installed to ~/.local/bin"
      fi
      # Verify installation
      if ! command -v kubectl &> /dev/null; then
          echo "❌ Error: Failed to install kubectl"
          exit 1
      fi
  fi

# Install Helm if missing
[private]
_install-helm:
  #!/usr/bin/env bash
  set -e

  if command -v helm &> /dev/null; then
      exit 0
  fi

  echo "📦 Installing Helm..."

  # Only Linux / amd64 automated install for now; otherwise instruct user
  UNAME_S="$(uname -s || echo unknown)"
  UNAME_M="$(uname -m || echo unknown)"

  if [ "$UNAME_S" != "Linux" ] || { [ "$UNAME_M" != "x86_64" ] && [ "$UNAME_M" != "amd64" ]; }; then
      echo "❌ Automatic Helm install only supported on Linux amd64."
      echo "   Please install Helm manually: https://helm.sh/docs/intro/install/"
      exit 1
  fi

  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT

  # Get latest Helm release tag
  HELM_TAG="$(curl -sSL https://api.github.com/repos/helm/helm/releases/latest | grep -oE '\"tag_name\":\s*\"v[0-9.]+\"' | head -1 | sed -E 's/\"tag_name\":\s*\"(v[0-9.]+)\"/\1/')"
  if [ -z "$HELM_TAG" ]; then
      echo "❌ Failed to determine latest Helm version."
      exit 1
  fi

  ARCHIVE="helm-${HELM_TAG}-linux-amd64.tar.gz"
  URL="https://get.helm.sh/${ARCHIVE}"

  echo "⬇️  Downloading ${URL}..."
  curl -sSL "$URL" -o "$TMP_DIR/helm.tar.gz"
  tar -xzf "$TMP_DIR/helm.tar.gz" -C "$TMP_DIR"

  if sudo mv "$TMP_DIR/linux-amd64/helm" /usr/local/bin/helm 2>/dev/null; then
      chmod +x /usr/local/bin/helm
      echo "✓ Helm installed to /usr/local/bin/helm"
  else
      mkdir -p "$HOME/.local/bin"
      mv "$TMP_DIR/linux-amd64/helm" "$HOME/.local/bin/helm"
      chmod +x "$HOME/.local/bin/helm"
      export PATH="$HOME/.local/bin:$PATH"
      echo "✓ Helm installed to ~/.local/bin/helm"
  fi

  if ! command -v helm &> /dev/null; then
      echo "❌ Error: Failed to install Helm"
      exit 1
  fi

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

# Seed local RustFS with example imagery from public bucket
[private]
_seed-example-imagery:
  #!/usr/bin/env bash
  set -euo pipefail

  echo "Ensuring local S3 service is running..."
  docker compose up --detach s3

  echo "Initializing RustFS bucket/credentials..."
  AWS_ACCESS_KEY_ID=odm \
  AWS_SECRET_ACCESS_KEY=somelongpassword \
  docker compose run --rm s3-init

  echo "Verifying local RustFS key access..."
  docker run --rm \
    --network host \
    -e AWS_S3_ENDPOINT=${SCALEODM_LOCAL_S3_ENDPOINT:-http://localhost:31102} \
    -e AWS_ACCESS_KEY_ID=odm \
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
    -e AWS_ACCESS_KEY_ID=odm \
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

# Run the manual workflow example (loads .env automatically via dotenv-load)
example-manual:
  go run examples/manual_workflow.go

# Example the API usage via Python script
example-python:
  #!/usr/bin/env bash
  set -euo pipefail
  trap 'docker compose down --remove-orphans' EXIT

  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=odm AWS_SECRET_ACCESS_KEY=somelongpassword just test cluster-available
  just test build-api-image
  docker compose down --remove-orphans
  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=odm AWS_SECRET_ACCESS_KEY=somelongpassword just _seed-example-imagery

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
    --from-literal=AWS_ACCESS_KEY_ID=odm \
    --from-literal=AWS_SECRET_ACCESS_KEY=somelongpassword \
    --from-literal=AWS_DEFAULT_REGION=us-east-1 \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "Starting API..."
  docker compose up --wait --detach --force-recreate api

  echo "Running Python API test inside container..."
  docker run --rm \
    --network host \
    -e SCALEODM_WORKFLOW_S3_ENDPOINT=${SCALEODM_WORKFLOW_S3_ENDPOINT} \
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

  echo "Shutting down containers..."
  docker compose down --remove-orphans

# Example the API usage via pyodm SDK
example-pyodm:
  #!/usr/bin/env bash
  set -euo pipefail
  trap 'docker compose down --remove-orphans' EXIT

  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=odm AWS_SECRET_ACCESS_KEY=somelongpassword just test cluster-available
  just test build-api-image
  docker compose down --remove-orphans
  SCALEODM_LOCAL_S3_ENDPOINT=http://localhost:31102 AWS_ACCESS_KEY_ID=odm AWS_SECRET_ACCESS_KEY=somelongpassword just _seed-example-imagery

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
    --from-literal=AWS_ACCESS_KEY_ID=odm \
    --from-literal=AWS_SECRET_ACCESS_KEY=somelongpassword \
    --from-literal=AWS_DEFAULT_REGION=us-east-1 \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "Starting API..."
  docker compose up --wait --detach --force-recreate api

  echo "Running pyodm example inside container..."
  docker run --rm \
    --network host \
    -e SCALEODM_WORKFLOW_S3_ENDPOINT=${SCALEODM_WORKFLOW_S3_ENDPOINT} \
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

  echo "Shutting down containers..."
  docker compose down --remove-orphans

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
