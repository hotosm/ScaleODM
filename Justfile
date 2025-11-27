set dotenv-load

# List available commands
[private]
default:
  just help

# List available commands
help:
  just --justfile {{justfile()}} --list

# Check if Kubernetes cluster is available and configured
[private]
_check-cluster:
  #!/usr/bin/env bash
  set +e
  
  # Check if kubectl is available
  if ! command -v kubectl &> /dev/null; then
      exit 1
  fi
  
  # Check if we can connect to the cluster
  if ! kubectl cluster-info &> /dev/null; then
      exit 1
  fi
  
  # Check if we can get nodes (basic connectivity test)
  if ! kubectl get nodes &> /dev/null; then
      exit 1
  fi
  
  exit 0

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

# Check test k8s cluster available
[private]
_cluster_available:
  #!/usr/bin/env bash
  set -e

  echo "Checking for Kubernetes cluster..."
  if ! just _check-cluster; then
      echo "⚠️  Kubernetes cluster not available"
      echo "📦 Initializing test cluster (this may take a few minutes)..."
      # Non-interactive mode will be auto-detected (no TTY in CI)
      just test-cluster-init
  else
      echo "✓ Kubernetes cluster is available"
  fi
  echo "" 

# Setup Talos Kubernetes cluster for testing (idempotent)
test-cluster-init:
  #!/usr/bin/env bash
  set -e
  
  CLUSTER_NAME="${TALOS_CLUSTER_NAME:-scaleodm-test}"
  WORKER_MEMORY="${TALOS_WORKER_MEMORY:-5142}"
  CONTROL_PLANE_MEMORY="${TALOS_CONTROL_PLANE_MEMORY:-2048}"
  NAMESPACE="${K8S_NAMESPACE:-argo}"
  ARGO_VERSION="${ARGO_WORKFLOWS_VERSION:-v3.7.3}"
  
  echo "🚀 Setting up Talos cluster for ScaleODM testing..."
  echo "   Cluster name: $CLUSTER_NAME"
  echo "   Worker memory: ${WORKER_MEMORY}MB"
  echo "   Control plane memory: ${CONTROL_PLANE_MEMORY}MB"
  echo ""
  
  # Ensure br_netfilter kernel module is loaded (required for Flannel networking in Docker)
  if ! lsmod | grep -q "^br_netfilter"; then
      echo "📦 Loading br_netfilter kernel module..."
      if sudo modprobe br_netfilter 2>/dev/null; then
          echo "✓ br_netfilter module loaded"
      else
          echo "⚠️  Warning: Failed to load br_netfilter module"
          echo "   This may cause issues with Kubernetes networking in Docker"
          echo "   If cluster creation fails, ensure the module can be loaded"
      fi
  else
      echo "✓ br_netfilter module already loaded"
  fi
  
  # Install required tools if missing
  just _install-curl
  just _install-talosctl
  just _install-kubectl
  
  # Check if the expected cluster exists and is accessible
  CLUSTER_SHOW_OUTPUT=$(talosctl cluster show 2>/dev/null || echo "")
  if [ -n "$CLUSTER_SHOW_OUTPUT" ]; then
      CURRENT_CLUSTER=$(echo "$CLUSTER_SHOW_OUTPUT" | grep "^NAME" | awk '{print $2}' | head -1 | tr -d '\n\r' || echo "")
      NODE_COUNT=$(echo "$CLUSTER_SHOW_OUTPUT" | grep -E "^[a-z].*-.*(controlplane|worker)" | wc -l | tr -d '[:space:]')
      
      if [ -z "$NODE_COUNT" ] || ! [ "$NODE_COUNT" -ge 0 ] 2>/dev/null; then
          NODE_COUNT=0
      fi
      
      # Check if a different cluster is running
      if [ -n "$CURRENT_CLUSTER" ] && [ "$NODE_COUNT" -gt 0 ] && [ "$CURRENT_CLUSTER" != "$CLUSTER_NAME" ]; then
          echo "❌ Error: A different cluster '$CURRENT_CLUSTER' is currently running"
          echo "   Expected cluster: $CLUSTER_NAME"
          echo "   Please destroy the existing cluster or set TALOS_CLUSTER_NAME=$CURRENT_CLUSTER"
          exit 1
      fi
      
      # Check if expected cluster exists but is not accessible
      if [ -n "$CURRENT_CLUSTER" ] && [ "$CURRENT_CLUSTER" = "$CLUSTER_NAME" ] && [ "$NODE_COUNT" -gt 0 ]; then
          if ! kubectl cluster-info &> /dev/null || ! kubectl get nodes &> /dev/null; then
              echo "❌ Error: Cluster '$CLUSTER_NAME' exists but is not accessible via kubectl"
              echo "   Please check your cluster status and fix any issues"
              exit 1
          fi
          
          # Cluster exists and is accessible - verify configuration
          echo "✓ Cluster '$CLUSTER_NAME' found and accessible"
          
          # Verify namespace exists
          if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
              echo "⚠️  Namespace '$NAMESPACE' missing, will create"
          else
              echo "✓ Namespace '$NAMESPACE' exists"
          fi
          
          # Verify Argo Workflows is installed
          if ! kubectl get crd workflows.argoproj.io &> /dev/null; then
              echo "⚠️  Argo Workflows not installed, will install"
          else
              echo "✓ Argo Workflows CRD exists"
              
              # If everything is configured, we are done
              if kubectl get namespace "$NAMESPACE" &> /dev/null; then
                  echo ""
                  echo "✅ Cluster is already configured and ready!"
                  echo ""
                  
                  # Ensure service account and RBAC exist (idempotent)
                  echo "📦 Verifying service account and RBAC..."
                  kubectl create serviceaccount argo-odm -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - &> /dev/null
                  kubectl create clusterrolebinding argo-odm-admin \
                      --clusterrole=admin \
                      --serviceaccount="$NAMESPACE:argo-odm" \
                      --dry-run=client -o yaml | kubectl apply -f - &> /dev/null
                  echo "✓ Service account and RBAC verified"
                  echo ""
                  exit 0
              fi
          fi
      fi
  fi
  
  echo "📦 Creating Talos cluster $CLUSTER_NAME..."
  
  talosctl cluster create \
      --name "$CLUSTER_NAME" \
      --workers 1 \
      --memory "${CONTROL_PLANE_MEMORY}" \
      --memory-workers "${WORKER_MEMORY}" \
      --cpus 1 \
      --cpus-workers 2 \
      --wait
  
  echo ""
  echo "✓ Talos cluster created successfully!"
  echo ""
  
  # Wait for cluster to be ready
  echo "⏳ Waiting for cluster to be ready..."
  kubectl wait --for=condition=Ready nodes --all --timeout=5m || {
      echo "⚠️  Warning: Some nodes may not be ready yet"
  }
  
  # Get kubeconfig context name
  CONTEXT=$(kubectl config current-context)
  echo "✓ Cluster is ready!"
  echo "   Kubeconfig context: $CONTEXT"
  echo ""
  
  # Install Argo Workflows if not already installed
  if ! kubectl get namespace "$NAMESPACE" &> /dev/null; then
      echo "📦 Creating namespace: $NAMESPACE"
      kubectl create namespace "$NAMESPACE"
  fi
  
  if ! kubectl get crd workflows.argoproj.io &> /dev/null; then
      echo "📦 Installing Argo Workflows $ARGO_VERSION..."
      kubectl apply -n "$NAMESPACE" -f "https://github.com/argoproj/argo-workflows/releases/download/${ARGO_VERSION}/install.yaml"
      
      # Wait for Argo Workflows to be ready
      echo "⏳ Waiting for Argo Workflows to be ready..."
      kubectl wait --for=condition=ready pod -l app=workflow-controller -n "$NAMESPACE" --timeout=5m || true
      kubectl wait --for=condition=ready pod -l app=argo-server -n "$NAMESPACE" --timeout=5m || true
  else
      echo "✓ Argo Workflows already installed"
  fi
  
  # Create service account for workflows
  echo "📦 Setting up service account for workflows..."
  kubectl create serviceaccount argo-odm -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
  
  # Bind admin role to service account for workflow execution
  echo "📦 Setting up RBAC for workflows..."
  kubectl create clusterrolebinding argo-odm-admin \
      --clusterrole=admin \
      --serviceaccount="$NAMESPACE:argo-odm" \
      --dry-run=client -o yaml | kubectl apply -f -
  
  echo ""
  echo "✅ Talos cluster setup complete!"
  echo ""
  echo "Next steps:"
  echo "  1. Start compose services: just start"
  echo "  2. Check cluster status: kubectl get nodes"
  echo "  3. Check Argo Workflows: kubectl get pods -n $NAMESPACE"
  echo ""
  echo "To destroy the cluster later:"
  echo "  just test-cluster-destroy"

# Destroy the Talos test cluster
test-cluster-destroy:
  #!/usr/bin/env bash
  set -e
  
  CLUSTER_NAME="${TALOS_CLUSTER_NAME:-scaleodm-test}"
  
  echo "🗑️  Destroying Talos cluster: $CLUSTER_NAME"
  talosctl cluster destroy --name "$CLUSTER_NAME"
  echo "✓ Cluster destroyed"



# Run all tests (requires Talos cluster and compose services)
# Will start DB and S3 if not already running
# Automatically initializes cluster if not available
test:
  #!/usr/bin/env bash
  set -e

  just _cluster_available

  echo "Running tests..."
  docker compose -f compose.yaml -f compose.test.yaml run --rm api

# Run unit tests only (no external dependencies)
test-unit:
  go test -v -short ./app/api/helpers_test.go ./app/api/helpers.go ./app/workflows/workflow_test.go

# Run integration tests only (requires DB)
test-integration:
  #!/usr/bin/env bash
  set -e
  echo "Running integration tests..."
  docker compose -f compose.yaml -f compose.test.yaml run --rm api go test -v -short ./app/meta/... ./app/db/... ./app/api/...

# Run E2E tests only (requires DB, S3, and K8s)
test-e2e:
  #!/usr/bin/env bash
  set -e
  just _cluster_available
  echo "Running E2E tests..."
  docker compose -f compose.yaml -f compose.test.yaml run --rm api go test -v -tags=e2e .

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
dev: test-cluster-init start

# Run the manual workflow example (loads .env automatically via dotenv-load)
example-manual:
  go run examples/manual_workflow.go

# Example the API usage via Python script
example-python:
  #!/usr/bin/env bash
  set -euo pipefail

  just _cluster_available

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
