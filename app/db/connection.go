package db

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	Pool *pgxpool.Pool // Changed to export Pool
}

// Create a new database connection pool
func NewDB(connString string) (*DB, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Configure pool settings
	config.MaxConns = 20
	config.MinConns = 5
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close closes the database connection pool
func (db *DB) Close() {
	db.Pool.Close()
}

// Create the required tables and indexes
func (db *DB) InitSchema(ctx context.Context) error {
	// Use advisory lock to prevent concurrent schema initialization
	// Lock ID 123456 is arbitrary but consistent
	lockID := int64(123456)
	
	// Use a transaction to ensure we use the same connection for lock/unlock
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	
	// Acquire advisory lock (blocking - will wait if another init is in progress)
	_, err = tx.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID)
	if err != nil {
		return fmt.Errorf("failed to acquire schema initialization lock: %w", err)
	}
	
	// Execute schema with error handling for already-existing objects
	_, err = tx.Exec(ctx, schemaSQL)
	if err != nil {
		// Check if it's a "duplicate key" error for pg_type (schema already initialized)
		// This can happen in concurrent test scenarios
		errStr := err.Error()
		if strings.Contains(errStr, "duplicate key") || strings.Contains(errStr, "already exists") {
			// Schema likely already exists, which is fine
			// Unlock and commit
			_, _ = tx.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
			_ = tx.Commit(ctx)
			return nil
		}
		return fmt.Errorf("failed to initialize schema: %w", err)
	}
	
	// Unlock before committing
	_, err = tx.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
	if err != nil {
		return fmt.Errorf("failed to release schema initialization lock: %w", err)
	}
	
	// Commit the transaction
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit schema initialization: %w", err)
	}
	
	return nil
}

// InitLocalClusterRecord creates the local cluster record if it doesn't exist
func (db *DB) InitLocalClusterRecord(ctx context.Context, clusterURL string) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO scaleodm_clusters (cluster_url, max_concurrent_jobs, priority_weighting, last_heartbeat)
		VALUES ($1, 10, 10, NOW())
		ON CONFLICT (cluster_url) DO NOTHING
	`, clusterURL)
	return err
}

// Ping the db to check its available
func (db *DB) HealthCheck(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}
