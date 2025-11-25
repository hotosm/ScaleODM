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

// testDB creates a test database connection for API tests
func testDB(t *testing.T) (*db.DB, func()) {
	t.Helper()

	dbURL := testutil.TestDBURL()

	database, err := db.NewDB(dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Initialize schema
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := database.InitSchema(ctx); err != nil {
		database.Close()
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	// Cleanup function
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Clean up test data
		_, _ = database.Pool.Exec(ctx, "TRUNCATE TABLE scaleodm_job_metadata CASCADE")
		_, _ = database.Pool.Exec(ctx, "TRUNCATE TABLE scaleodm_clusters CASCADE")
		
		database.Close()
	}

	return database, cleanup
}

// testWorkflowClient creates a real workflow client for API tests
func testWorkflowClient(t *testing.T) workflows.WorkflowClient {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG_PATH")
	namespace := os.Getenv("K8S_NAMESPACE")
	if namespace == "" {
		namespace = "argo"
	}

	client, err := workflows.NewClient(kubeconfig, namespace)
	if err != nil {
		t.Fatalf("Failed to create workflow client: %v", err)
	}

	return client
}

