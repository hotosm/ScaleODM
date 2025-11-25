set dotenv-load

# List available commands
[private]
default:
  just help

# List available commands
help:
  just --justfile {{justfile()}} --list

# Setup Talos Kubernetes cluster for testing
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
  
  # Check if talosctl is installed
  if ! command -v talosctl &> /dev/null; then
      echo "❌ Error: talosctl is not installed"
      echo "   Install from: https://www.talos.dev/latest/introduction/install/"
      exit 1
  fi
  
  # Check if cluster already exists
  if talosctl config info --cluster "$CLUSTER_NAME" &> /dev/null; then
      echo "ℹ️  Cluster '$CLUSTER_NAME' already exists"
      read -p "   Do you want to destroy and recreate it? (y/N) " -n 1 -r
      echo
      if [[ $REPLY =~ ^[Yy]$ ]]; then
          echo "🗑️  Destroying existing cluster..."
          talosctl cluster destroy --name "$CLUSTER_NAME" || true
      else
          echo "✓ Using existing cluster"
          echo ""
          echo "To manually destroy the cluster later:"
          echo "  just test-cluster-destroy"
          exit 0
      fi
  fi
  
  # Create the cluster
  echo "📦 Creating Talos cluster..."
  talosctl cluster create \
      --name "$CLUSTER_NAME" \
      --workers 1 \
      --memory-workers "${WORKER_MEMORY}" \
      --memory-control-plane "${CONTROL_PLANE_MEMORY}" \
      --cpus-workers 2 \
      --cpus-control-plane 2 \
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
test:
  #!/usr/bin/env bash
  set -e
  echo "Ensuring compose services are running..."
  docker compose up -d db s3
  echo "Waiting for services to be healthy..."
  if ! timeout 60 bash -c 'until docker compose ps | grep -q "healthy.*db" && docker compose ps | grep -q "healthy.*s3"; do sleep 2; done'; then
      echo "❌ Error: Services failed to become healthy within 60 seconds"
      docker compose ps
      exit 1
  fi
  echo "✓ Services are healthy"
  echo "Running tests..."
  docker compose run --rm api

# Run unit tests only (no external dependencies)
test-unit:
  go test -v -short ./app/api/helpers_test.go ./app/api/helpers.go ./app/workflows/workflow_test.go

# Run integration tests only (requires DB)
test-integration:
  #!/usr/bin/env bash
  set -e
  echo "Ensuring compose services are running..."
  docker compose up -d db s3
  echo "Waiting for services to be healthy..."
  if ! timeout 60 bash -c 'until docker compose ps | grep -q "healthy.*db" && docker compose ps | grep -q "healthy.*s3"; do sleep 2; done'; then
      echo "❌ Error: Services failed to become healthy within 60 seconds"
      docker compose ps
      exit 1
  fi
  echo "✓ Services are healthy"
  echo "Running integration tests..."
  docker compose run --rm api go test -v -short ./app/meta/... ./app/db/... ./app/api/...

# Run E2E tests only (requires DB, S3, and K8s)
test-e2e:
  #!/usr/bin/env bash
  set -e
  echo "Ensuring compose services are running..."
  docker compose up -d db s3
  echo "Waiting for services to be healthy..."
  if ! timeout 60 bash -c 'until docker compose ps | grep -q "healthy.*db" && docker compose ps | grep -q "healthy.*s3"; do sleep 2; done'; then
      echo "❌ Error: Services failed to become healthy within 60 seconds"
      docker compose ps
      exit 1
  fi
  echo "✓ Services are healthy"
  echo "Running E2E tests..."
  docker compose run --rm api go test -v -tags=e2e .

# Start compose services (DB, S3, API)
# Assumes Talos cluster is already running
start:
  #!/usr/bin/env bash
  set -e
  echo "Starting compose services..."
  docker compose up -d db s3
  echo "Waiting for services to be healthy..."
  if ! timeout 60 bash -c 'until docker compose ps | grep -q "healthy.*db" && docker compose ps | grep -q "healthy.*s3"; do sleep 2; done'; then
      echo "⚠️  Warning: Services may not be healthy yet"
      docker compose ps
  fi
  echo "✓ Services ready, starting API..."
  docker compose up api

# Stop compose services
stop:
  docker compose down

# Setup Talos cluster and start all services for development
dev: test-cluster-init start

# Run the manual workflow example (loads .env automatically via dotenv-load)
run-example:
  go run examples/manual_workflow.go

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
