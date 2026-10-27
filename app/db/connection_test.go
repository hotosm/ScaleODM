package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDB(t *testing.T) {
	dbURL := testutil.TestDBURL()

	database, err := db.NewDB(dbURL)
	require.NoError(t, err)
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = database.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestInitSchema(t *testing.T) {
	dbURL := testutil.TestDBURL()
	database, err := db.NewDB(dbURL)
	require.NoError(t, err)
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = database.InitSchema(ctx)
	require.NoError(t, err)

	// Confirm the schema landed by checking the table exists.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	var count int
	err = database.Pool.QueryRow(ctx2, `
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = 'public'
		AND table_name IN ('scaleodm_job_metadata')
	`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestHealthCheck(t *testing.T) {
	database, cleanup := testutil.TestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := database.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestClose(t *testing.T) {
	_, cleanup := testutil.TestDB(t)
	defer cleanup()

	assert.NotPanics(t, func() {
		cleanup()
	})
}
