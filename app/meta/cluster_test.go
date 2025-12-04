package meta

import (
	"context"
	"strings"
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

	clusterURL := "http://localhost:31100"
	
	// Initialize local cluster record
	err := db.InitLocalClusterRecord(ctx, clusterURL)
	require.NoError(t, err)

	// Verify cluster exists before updating heartbeat
	clusters, err := store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1, "Cluster should exist after initialization")

	// Update heartbeat
	err = store.UpdateClusterHeartbeat(ctx, clusterURL)
	require.NoError(t, err)

	// Verify heartbeat was updated - use a small retry in case of timing issues
	var updatedClusters []*Cluster
	for i := 0; i < 5; i++ {
		updatedClusters, err = store.ListClusters(ctx)
		if err == nil && len(updatedClusters) > 0 {
			break
		}
		if i < 4 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.NoError(t, err)
	require.Len(t, updatedClusters, 1, "Cluster should still exist after heartbeat update")
	clusters = updatedClusters
	assert.True(t, clusters[0].LastHeartbeat.Valid)
	assert.WithinDuration(t, time.Now(), clusters[0].LastHeartbeat.Time, 5*time.Second)
}

func TestUpdateClusterDetails(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	clusterURL := "http://localhost:31100"

	// Ensure a local cluster record exists first, mirroring how the
	// application initialises the cluster table.
	err := db.InitLocalClusterRecord(ctx, clusterURL)
	require.NoError(t, err)

	// Update cluster details (creates/updates the record)
	err = store.UpdateClusterDetails(ctx, clusterURL, 20, 50)
	require.NoError(t, err)

	// Verify details were updated
	clusters, err := store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1, "Cluster should exist after first update")
	assert.Equal(t, 20, clusters[0].MaxConcurrentJobs)
	assert.Equal(t, 50, clusters[0].PriorityWeighting)

	// Verify cluster still exists before second update
	clusters, err = store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1, "Cluster should still exist before second update")

	// Update again (use clusterURL variable for consistency)
	err = store.UpdateClusterDetails(ctx, clusterURL, 30, 75)
	require.NoError(t, err)

	// Verify details were updated again
	clusters, err = store.ListClusters(ctx)
	require.NoError(t, err)
	require.Len(t, clusters, 1, "Cluster should still exist after second update")
	assert.Equal(t, 30, clusters[0].MaxConcurrentJobs)
	assert.Equal(t, 75, clusters[0].PriorityWeighting)
}

func TestGetClusterCapacity(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	store := NewStore(db)
	ctx := context.Background()

	// Use a consistent cluster URL for the test
	clusterURL := "http://localhost:31100"

	// Initialize cluster with max 10 jobs
	err := db.InitLocalClusterRecord(ctx, clusterURL)
	require.NoError(t, err)

	// Update cluster details
	err = store.UpdateClusterDetails(ctx, clusterURL, 10, 10)
	require.NoError(t, err)

	// Get capacity (should be 0 active jobs)
	maxJobs, activeJobs, err := store.GetClusterCapacity(ctx, clusterURL)
	require.NoError(t, err)
	assert.Equal(t, 10, maxJobs)
	assert.Equal(t, 0, activeJobs)

	// Create some running jobs
	_, err = store.CreateJob(
		ctx,
		clusterURL,
		"test-workflow-1",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Wait for job to be visible before updating status (retry in case of timing issues)
	for i := 0; i < 5; i++ {
		err = store.UpdateJobStatus(ctx, "test-workflow-1", "running", nil)
		if err == nil {
			break
		}
		if i < 4 && strings.Contains(err.Error(), "not found") {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		require.NoError(t, err)
	}
	require.NoError(t, err)

	_, err = store.CreateJob(
		ctx,
		clusterURL,
		"test-workflow-2",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Wait for job to be visible before updating status (retry in case of timing issues)
	for i := 0; i < 5; i++ {
		err = store.UpdateJobStatus(ctx, "test-workflow-2", "claimed", nil)
		if err == nil {
			break
		}
		if i < 4 && strings.Contains(err.Error(), "not found") {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		require.NoError(t, err)
	}
	require.NoError(t, err)

	// Get capacity again (should be 2 active jobs)
	maxJobs, activeJobs, err = store.GetClusterCapacity(ctx, clusterURL)
	require.NoError(t, err)
	assert.Equal(t, 10, maxJobs)
	assert.Equal(t, 2, activeJobs)

	// Create a completed job (should not count)
	_, err = store.CreateJob(
		ctx,
		clusterURL,
		"test-workflow-3",
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Wait for job to be visible before updating status (retry in case of timing issues)
	for i := 0; i < 5; i++ {
		err = store.UpdateJobStatus(ctx, "test-workflow-3", "completed", nil)
		if err == nil {
			break
		}
		if i < 4 && strings.Contains(err.Error(), "not found") {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		require.NoError(t, err)
	}
	require.NoError(t, err)

	// Get capacity again (should still be 2 active jobs)
	maxJobs, activeJobs, err = store.GetClusterCapacity(ctx, clusterURL)
	require.NoError(t, err)
	assert.Equal(t, 10, maxJobs)
	assert.Equal(t, 2, activeJobs)
}

