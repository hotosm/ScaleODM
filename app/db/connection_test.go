package db

import (
	"context"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDB(t *testing.T) (*DB, func()) {
	t.Helper()

	database, err := NewDB(testutil.TestDBURL())
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

func TestNewDB(t *testing.T) {
	dbURL := testutil.TestDBURL()

	db, err := NewDB(dbURL)
	require.NoError(t, err)
	defer db.Close()

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

	// Confirm the schema landed by checking the table exists.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	var count int
	err = db.Pool.QueryRow(ctx2, `
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = 'public'
		AND table_name IN ('scaleodm_job_metadata')
	`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
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

	assert.NotPanics(t, func() {
		cleanup()
	})
}
