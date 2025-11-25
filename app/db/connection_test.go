package db

import (
	"context"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDB creates a test database connection
func testDB(t *testing.T) (*DB, func()) {
	t.Helper()

	dbURL := testutil.TestDBURL()

	database, err := NewDB(dbURL)
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

func TestNewDB(t *testing.T) {
	dbURL := testutil.TestDBURL()

	db, err := NewDB(dbURL)
	require.NoError(t, err)
	defer db.Close()

	// Test ping
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = db.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestInitSchema(t *testing.T) {
	dbURL := testutil.TestDBURL()
	db, err := NewDB(dbURL)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = db.InitSchema(ctx)
	require.NoError(t, err)

	// Schema should already be initialized
	// But we can verify it by checking if tables exist
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	var count int
	err = db.Pool.QueryRow(ctx2, `
		SELECT COUNT(*) 
		FROM information_schema.tables 
		WHERE table_schema = 'public' 
		AND table_name IN ('scaleodm_clusters', 'scaleodm_job_metadata')
	`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestInitLocalClusterRecord(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clusterURL := "http://localhost:31100"
	err := db.InitLocalClusterRecord(ctx, clusterURL)
	require.NoError(t, err)

	// Verify cluster was created
	var exists bool
	err = db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM scaleodm_clusters WHERE cluster_url = $1)
	`, clusterURL).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	// Call again - should not error (ON CONFLICT DO NOTHING)
	err = db.InitLocalClusterRecord(ctx, clusterURL)
	require.NoError(t, err)
}

func TestHealthCheck(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := db.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestClose(t *testing.T) {
	_, cleanup := testDB(t)
	defer cleanup()

	// Close should not panic
	assert.NotPanics(t, func() {
		cleanup()
	})
}

