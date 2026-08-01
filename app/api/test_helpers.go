package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
)

func testDB(t *testing.T) (*db.DB, func()) {
	t.Helper()

	database, err := db.NewDB(testutil.TestDBURL())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := database.InitSchema(ctx); err != nil {
		database.Close()
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = database.Pool.Exec(ctx, "TRUNCATE TABLE scaleodm_job_metadata CASCADE")
		database.Close()
	}

	return database, cleanup
}

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
