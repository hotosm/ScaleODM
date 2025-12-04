package meta

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateJob(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	job, err := store.CreateJob(
		ctx,
		"http://localhost:31100",
		"test-workflow-1",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)

	require.NoError(t, err)
	assert.NotNil(t, job)
	assert.Equal(t, "test-workflow-1", job.WorkflowName)
	assert.Equal(t, "test-project", job.ODMProjectID)
	assert.Equal(t, "s3://bucket/images/", job.ReadS3Path)
	assert.Equal(t, "s3://bucket/output/", job.WriteS3Path)
	assert.Equal(t, "us-east-1", job.S3Region)
	assert.Equal(t, "queued", job.JobStatus)
	assert.NotZero(t, job.ID)
	assert.False(t, job.CreatedAt.IsZero())
}

func TestGetJob(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create a job
	created, err := store.CreateJob(
		ctx,
		"http://localhost:31100",
		"test-workflow-2",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Retrieve the job
	job, err := store.GetJob(ctx, "test-workflow-2")
	require.NoError(t, err)
	require.NotNil(t, job)

	assert.Equal(t, created.ID, job.ID)
	assert.Equal(t, "test-workflow-2", job.WorkflowName)
	assert.Equal(t, "test-project", job.ODMProjectID)
}

func TestGetJob_NotFound(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	job, err := store.GetJob(ctx, "non-existent-workflow")
	require.NoError(t, err)
	assert.Nil(t, job)
}

func TestUpdateJobStatus(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create a job
	_, err = store.CreateJob(
		ctx,
		"http://localhost:31100",
		"test-workflow-3",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	workflowName := "test-workflow-3"
	
	// Update status to running
	err = store.UpdateJobStatus(ctx, workflowName, "running", nil)
	require.NoError(t, err)

	// Verify status was updated
	job, err := store.GetJob(ctx, workflowName)
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist after updating to running")
	assert.Equal(t, "running", job.JobStatus)
	assert.NotNil(t, job.StartedAt, "StartedAt should be set when status changes to running")
	assert.False(t, job.StartedAt.IsZero())

	// Update status to completed
	err = store.UpdateJobStatus(ctx, workflowName, "completed", nil)
	require.NoError(t, err)

	// Verify status was updated
	job, err = store.GetJob(ctx, workflowName)
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist after updating to completed")
	assert.Equal(t, "completed", job.JobStatus)
	assert.NotNil(t, job.CompletedAt, "CompletedAt should be set when status changes to completed")
	assert.False(t, job.CompletedAt.IsZero())
}

func TestUpdateJobStatus_WithError(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create a job
	_, err = store.CreateJob(
		ctx,
		"http://localhost:31100",
		"test-workflow-4",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	errorMsg := "Workflow failed: timeout"
	err = store.UpdateJobStatus(ctx, "test-workflow-4", "failed", &errorMsg)
	require.NoError(t, err)

	// Verify error message was set
	job, err := store.GetJob(ctx, "test-workflow-4")
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "failed", job.JobStatus)
	assert.NotNil(t, job.ErrorMessage)
	assert.Equal(t, "Workflow failed: timeout", *job.ErrorMessage)
}

func TestMapArgoPhaseToJobStatus(t *testing.T) {
	tests := []struct {
		name     string
		phase    string
		expected string
	}{
		{"Pending", "Pending", "queued"},
		{"Running", "Running", "running"},
		{"Succeeded", "Succeeded", "completed"},
		{"Failed", "Failed", "failed"},
		{"Error", "Error", "failed"},
		{"Unknown", "Unknown", "queued"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MapArgoPhaseToJobStatus(tt.phase)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUpdateJobMetadata(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create a job
	_, err = store.CreateJob(
		ctx,
		"http://localhost:31100",
		"test-workflow-5",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Update metadata
	metadata := map[string]interface{}{
		"image_count": 10,
		"progress":    50,
		"node_name":   "test-node",
	}
	err = store.UpdateJobMetadata(ctx, "test-workflow-5", metadata)
	require.NoError(t, err)

	// Verify metadata was updated
	job, err := store.GetJob(ctx, "test-workflow-5")
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.NotNil(t, job.Metadata)
	assert.NotEmpty(t, job.Metadata)
}

func TestListJobs(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create multiple jobs
	for i := 0; i < 5; i++ {
		_, createErr := store.CreateJob(
			ctx,
			"http://localhost:31100",
			fmt.Sprintf("test-workflow-%d", i),
			"test-project",
			"s3://bucket/images/",
			"s3://bucket/output/",
			[]string{"--fast-orthophoto"},
			"us-east-1",
		)
		require.NoError(t, createErr)
	}

	// List all jobs – we mainly verify that the query executes without error.
	jobs, err := store.ListJobs(ctx, "", "", 0)
	require.NoError(t, err)

	// List with limit – the limit should cap the number of results returned,
	// regardless of how many additional jobs exist in the database.
	jobs, err = store.ListJobs(ctx, "", "", 3)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(jobs), 3)
}

func TestListJobs_ByProjectID(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	clusterURL := "http://localhost:31100"
	err := db.InitLocalClusterRecord(ctx, clusterURL)
	require.NoError(t, err)

	// Create jobs with different project IDs
	_, err = store.CreateJob(
		ctx,
		clusterURL,
		"test-workflow-project1-1",
		"project-1",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	_, err = store.CreateJob(
		ctx,
		clusterURL,
		"test-workflow-project1-2",
		"project-1",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	_, err = store.CreateJob(
		ctx,
		clusterURL,
		"test-workflow-project2-1",
		"project-2",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// List jobs for project-1
	jobs, err := store.ListJobs(ctx, "", "project-1", 0)
	require.NoError(t, err)
	assert.Len(t, jobs, 2)
	for _, job := range jobs {
		assert.Equal(t, "project-1", job.ODMProjectID)
	}
}

func TestDeleteJob(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster first (required for foreign key constraint)
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create a job
	_, err = store.CreateJob(
		ctx,
		"http://localhost:31100",
		"test-workflow-delete",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Delete the job
	err = store.DeleteJob(ctx, "test-workflow-delete")
	require.NoError(t, err)

	// Verify job is deleted
	job, err := store.GetJob(ctx, "test-workflow-delete")
	require.NoError(t, err)
	assert.Nil(t, job)
}

func TestDeleteJob_NotFound(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	err := store.DeleteJob(ctx, "non-existent-workflow")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

