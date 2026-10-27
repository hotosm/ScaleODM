package api

import (
	"os"
	"testing"

	"github.com/hotosm/scaleodm/app/workflows"
)

func testWorkflowClient(t *testing.T) workflows.WorkflowClient {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG_PATH")
	namespace := os.Getenv("K8S_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	client, err := workflows.NewClient(kubeconfig, namespace)
	if err != nil {
		t.Fatalf("Failed to create workflow client: %v", err)
	}

	return client
}
