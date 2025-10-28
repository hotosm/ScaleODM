package db

import (
	"context"
	_ "embed"
	"fmt"
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
	_, err := db.Pool.Exec(ctx, schemaSQL)
	return err
}

// InitSchema creates the required tables and indexes
func (db *DB) InitLocalClusterRecord(ctx context.Context) error {
	// FIXME consider setting via env var?
	clusterUrl := "http://localhost:8080"

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO scaleodm_clusters
			(cluster_url)
		VALUES
			($1);
	`, clusterUrl)
	return err
}

// Ping the db to check its available
func (db *DB) HealthCheck(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}
