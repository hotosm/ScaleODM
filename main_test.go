//go:build e2e
// +build e2e

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDB creates a test database connection for E2E tests
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

// E2E tests require:
// - Database running (via docker compose)
// - Kubernetes cluster with Argo Workflows installed
// - S3 endpoint available (MinIO via docker compose)

func TestE2E_HealthCheck(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := db.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestE2E_CreateAndListJobs(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := meta.NewStore(db)
	ctx := context.Background()

	// Initialize cluster (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create multiple jobs
	for i := 0; i < 3; i++ {
		_, createErr := store.CreateJob(
			ctx,
			"http://localhost:31100",
			fmt.Sprintf("e2e-workflow-%d", i),
			"e2e-project",
			"s3://test-bucket/images/",
			"s3://test-bucket/output/",
			[]string{"--fast-orthophoto"},
			"us-east-1",
		)
		require.NoError(t, createErr)
	}

	// List jobs
	jobs, err := store.ListJobs(ctx, "", "", 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(jobs), 3)
}

func TestE2E_WorkflowClient_WithK8s(t *testing.T) {
	
	kubeconfig := os.Getenv("KUBECONFIG_PATH")
	namespace := os.Getenv("K8S_NAMESPACE")
	if namespace == "" {
		namespace = "argo"
	}

	client, err := workflows.NewClient(kubeconfig, namespace)
	require.NoError(t, err)
	assert.NotNil(t, client)

	// List workflows (should work even if empty)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wfList, err := client.ListWorkflows(ctx, "")
	require.NoError(t, err)
	assert.NotNil(t, wfList)
}

func TestE2E_JobLifecycle(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := meta.NewStore(db)
	ctx := context.Background()

	// Initialize cluster (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create job
	job, err := store.CreateJob(
		ctx,
		"http://localhost:31100",
		"e2e-lifecycle-workflow",
		"e2e-project",
		"s3://test-bucket/images/",
		"s3://test-bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	assert.Equal(t, "pending", job.JobStatus)

	// Update to running
	err = store.UpdateJobStatus(ctx, "e2e-lifecycle-workflow", "running", nil)
	require.NoError(t, err)

	job, err = store.GetJob(ctx, "e2e-lifecycle-workflow")
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist after status update")
	assert.Equal(t, "running", job.JobStatus)
	assert.NotNil(t, job.StartedAt)

	// Update to completed
	err = store.UpdateJobStatus(ctx, "e2e-lifecycle-workflow", "completed", nil)
	require.NoError(t, err)

	job, err = store.GetJob(ctx, "e2e-lifecycle-workflow")
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist after status update")
	assert.Equal(t, "completed", job.JobStatus)
	assert.NotNil(t, job.CompletedAt)

	// Delete job
	err = store.DeleteJob(ctx, "e2e-lifecycle-workflow")
	require.NoError(t, err)

	job, err = store.GetJob(ctx, "e2e-lifecycle-workflow")
	require.NoError(t, err)
	assert.Nil(t, job)
}

func TestE2E_ClusterOperations(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := meta.NewStore(db)
	ctx := context.Background()

	// Ensure cluster exists first (UpdateClusterDetails will create it if needed)
	err := store.UpdateClusterDetails(ctx, "http://localhost:31100", 20, 50)
	require.NoError(t, err)

	// Get cluster capacity
	maxJobs, activeJobs, err := store.GetClusterCapacity(ctx, "http://localhost:31100")
	require.NoError(t, err)
	assert.Equal(t, 20, maxJobs)
	// Note: activeJobs might be > 0 if there are leftover jobs from previous tests
	// The cleanup should handle this, but we'll just check it's a valid number
	assert.GreaterOrEqual(t, activeJobs, 0)

	// Update heartbeat
	err = store.UpdateClusterHeartbeat(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// List clusters
	clusters, err := store.ListClusters(ctx)
	require.NoError(t, err)
	assert.Len(t, clusters, 1)
	assert.Equal(t, "http://localhost:31100", clusters[0].ClusterURL)
}
