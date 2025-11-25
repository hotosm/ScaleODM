package testutil

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDBURL returns the database URL from SCALEODM_DATABASE_URL environment variable
func TestDBURL() string {
	dbURL := os.Getenv("SCALEODM_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://odm:odm@localhost:31101/scaleodm?sslmode=disable"
	}
	return dbURL
}

// WaitForDB waits for the database to be available
func WaitForDB(dbURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		config, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			return fmt.Errorf("failed to parse connection string: %w", err)
		}

		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = pool.Ping(ctx)
		cancel()
		pool.Close()

		if err == nil {
			return nil
		}

		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("database not available after %v", timeout)
}

