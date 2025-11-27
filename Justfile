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
      uv run python api_test.py
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
