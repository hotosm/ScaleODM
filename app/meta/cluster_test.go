package meta

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListClusters(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize local cluster record
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// List clusters
	clusters, err := store.ListClusters(ctx)
	require.NoError(t, err)
	assert.Len(t, clusters, 1)
	assert.Equal(t, "http://localhost:31100", clusters[0].ClusterURL)
	assert.Equal(t, 10, clusters[0].MaxConcurrentJobs)
	assert.Equal(t, 10, clusters[0].PriorityWeighting)
}

func TestUpdateClusterHeartbeat(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize local cluster record
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Update heartbeat
	err = store.UpdateClusterHeartbeat(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Verify heartbeat was updated
	clusters, err := store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	assert.True(t, clusters[0].LastHeartbeat.Valid)
	assert.WithinDuration(t, time.Now(), clusters[0].LastHeartbeat.Time, 5*time.Second)
}

func TestUpdateClusterDetails(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Update cluster details (creates if not exists)
	err := store.UpdateClusterDetails(ctx, "http://localhost:31100", 20, 50)
	require.NoError(t, err)

	// Verify details were updated
	clusters, err := store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	assert.Equal(t, 20, clusters[0].MaxConcurrentJobs)
	assert.Equal(t, 50, clusters[0].PriorityWeighting)

	// Update again
	err = store.UpdateClusterDetails(ctx, "http://localhost:31100", 30, 75)
	require.NoError(t, err)

	// Verify details were updated again
	clusters, err = store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	assert.Equal(t, 30, clusters[0].MaxConcurrentJobs)
	assert.Equal(t, 75, clusters[0].PriorityWeighting)
}

func TestGetClusterCapacity(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Initialize cluster with max 10 jobs
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Update cluster details
	err = store.UpdateClusterDetails(ctx, "http://localhost:31100", 10, 10)
	require.NoError(t, err)

	// Get capacity (should be 0 active jobs)
	maxJobs, activeJobs, err := store.GetClusterCapacity(ctx, "http://localhost:31100")
	require.NoError(t, err)
	assert.Equal(t, 10, maxJobs)
	assert.Equal(t, 0, activeJobs)

	// Create some running jobs
	_, err = store.CreateJob(
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

	err = store.UpdateJobStatus(ctx, "test-workflow-1", "running", nil)
	require.NoError(t, err)

	_, err = store.CreateJob(
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

	err = store.UpdateJobStatus(ctx, "test-workflow-2", "claimed", nil)
	require.NoError(t, err)

	// Get capacity again (should be 2 active jobs)
	maxJobs, activeJobs, err = store.GetClusterCapacity(ctx, "http://localhost:31100")
	require.NoError(t, err)
	assert.Equal(t, 10, maxJobs)
	assert.Equal(t, 2, activeJobs)

	// Create a completed job (should not count)
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

	err = store.UpdateJobStatus(ctx, "test-workflow-3", "completed", nil)
	require.NoError(t, err)

	// Get capacity again (should still be 2 active jobs)
	maxJobs, activeJobs, err = store.GetClusterCapacity(ctx, "http://localhost:31100")
	require.NoError(t, err)
	assert.Equal(t, 10, maxJobs)
	assert.Equal(t, 2, activeJobs)
}

