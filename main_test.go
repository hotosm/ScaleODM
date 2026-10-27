//go:build e2e
// +build e2e

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// E2E tests require:
// - Database running (via docker compose)
// - Kubernetes cluster with Argo Workflows installed
// - S3 endpoint available (MinIO via docker compose)

func TestE2E_HealthCheck(t *testing.T) {
	db, cleanup := testutil.TestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := db.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestE2E_CreateAndListJobs(t *testing.T) {
	db, cleanup := testutil.TestDB(t)
	defer cleanup()

	store := meta.NewStore(db)
	ctx := context.Background()

	err := testutil.SetupTestS3Bucket(ctx, "test-bucket")
	require.NoError(t, err, "Failed to set up test S3 bucket")

	for i := 0; i < 3; i++ {
		_, createErr := store.CreateJob(
			ctx,
			fmt.Sprintf("e2e-workflow-%d", i),
			"e2e-project",
			"s3://test-bucket/images/",
			"s3://test-bucket/output/",
			[]string{"--fast-orthophoto"},
			"us-east-1",
			nil,
		)
		require.NoError(t, createErr)
	}

	jobs, err := store.ListJobs(ctx, "", "", 0, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(jobs), 3)
}

func TestE2E_WorkflowClient_WithK8s(t *testing.T) {

	kubeconfig := os.Getenv("KUBECONFIG_PATH")
	namespace := os.Getenv("K8S_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	client, err := workflows.NewClient(kubeconfig, namespace)
	require.NoError(t, err)
	assert.NotNil(t, client)

	// Listing must succeed even when no workflows exist.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wfList, err := client.ListWorkflows(ctx, "")
	require.NoError(t, err)
	assert.NotNil(t, wfList)
}

func TestE2E_JobLifecycle(t *testing.T) {
	db, cleanup := testutil.TestDB(t)
	defer cleanup()

	store := meta.NewStore(db)
	ctx := context.Background()

	err := testutil.SetupTestS3Bucket(ctx, "test-bucket")
	require.NoError(t, err, "Failed to set up test S3 bucket")

	workflowName := "e2e-lifecycle-workflow"
	job, err := store.CreateJob(
		ctx,
		workflowName,
		"e2e-project",
		"s3://test-bucket/images/",
		"s3://test-bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, job, "Job should be created successfully")
	// New jobs default to 'queued' in the metadata store.
	assert.Equal(t, meta.JobQueued, job.JobStatus)

	// Retry the read to absorb any replication/timing lag before the update.
	var retrievedJob *meta.JobMetadata
	for i := 0; i < 5; i++ {
		retrievedJob, err = store.GetJob(ctx, workflowName)
		if err == nil && retrievedJob != nil {
			break
		}
		if i < 4 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.NoError(t, err)
	require.NotNil(t, retrievedJob, "Job should exist before status update")
	job = retrievedJob

	err = store.UpdateJobStatus(ctx, workflowName, "running", nil)
	require.NoError(t, err)

	job, err = store.GetJob(ctx, workflowName)
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist after status update")
	assert.Equal(t, meta.JobRunning, job.JobStatus)
	assert.NotNil(t, job.StartedAt)

	err = store.UpdateJobStatus(ctx, workflowName, "completed", nil)
	require.NoError(t, err)

	job, err = store.GetJob(ctx, workflowName)
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist after status update")
	assert.Equal(t, meta.JobCompleted, job.JobStatus)
	assert.NotNil(t, job.CompletedAt)

	err = store.DeleteJob(ctx, workflowName)
	require.NoError(t, err)

	job, err = store.GetJob(ctx, workflowName)
	require.NoError(t, err)
	assert.Nil(t, job)
}
