package meta

import (
	"context"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/testutil"
)

// testDB creates a test database connection for meta tests
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

