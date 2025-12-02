set dotenv-load

mod test 'tasks/test'
mod chart 'tasks/chart'

# List available commands
[private]
default:
  just help

# List available commands
help:
  just --justfile {{justfile()}} --list

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
  docker compose run --rm -d api run main.go

# Stop compose services
stop:
  docker compose down --remove-orphans

# Setup Talos cluster and start all services for development
dev:
  just test cluster-init
  just start

# Run the manual workflow example (loads .env automatically via dotenv-load)
example-manual:
  go run examples/manual_workflow.go

# Example the API usage via Python script
example-python:
  #!/usr/bin/env bash
  set -euo pipefail

  just test cluster-available

  echo "Starting API..."
  docker compose up --wait --detach api

  echo "Running Python API test inside container..."
  docker run --rm \
    --network host \
    --env-file .env \
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
