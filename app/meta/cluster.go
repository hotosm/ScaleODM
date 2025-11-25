package meta

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type Cluster struct {
	ClusterURL        string
	MaxConcurrentJobs int
	PriorityWeighting int
	LastHeartbeat     sql.NullTime
}

func (s *Store) ListClusters(ctx context.Context) ([]*Cluster, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT
			cluster_url, max_concurrent_jobs,
			priority_weighting, last_heartbeat
		FROM scaleodm_clusters
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}
	defer rows.Close()

	var clusters []*Cluster
	for rows.Next() {
		c := &Cluster{}
		err := rows.Scan(&c.ClusterURL, &c.MaxConcurrentJobs, &c.PriorityWeighting, &c.LastHeartbeat)
		if err != nil {
			return nil, fmt.Errorf("failed to scan cluster: %w", err)
		}
		clusters = append(clusters, c)
	}
	return clusters, nil
}

func (s *Store) UpdateClusterHeartbeat(ctx context.Context, clusterID string) error {
	// Use INSERT ... ON CONFLICT to ensure cluster exists before updating heartbeat
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO scaleodm_clusters (cluster_url, max_concurrent_jobs, priority_weighting, last_heartbeat)
		VALUES ($1, 10, 10, NOW())
		ON CONFLICT (cluster_url) 
		DO UPDATE SET last_heartbeat = NOW()
	`, clusterID)
	return err
}

// UpdateClusterDetails updates the capacity information for a cluster
func (s *Store) UpdateClusterDetails(ctx context.Context, clusterID string, maxJobs, priorityWeighting int) error {
	query := `
		INSERT INTO scaleodm_clusters (cluster_url, max_concurrent_jobs, priority_weighting, last_heartbeat)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (cluster_url) 
		DO UPDATE SET 
			max_concurrent_jobs = EXCLUDED.max_concurrent_jobs,
			priority_weighting = EXCLUDED.priority_weighting,
			last_heartbeat = NOW()
	`
	_, err := s.db.Pool.Exec(ctx, query, clusterID, maxJobs, priorityWeighting)
	return err
}

// Find the number of jobs running currently, with max jobs that can run
func (s *Store) GetClusterCapacity(ctx context.Context, clusterID string) (maxJobs, activeJobs int, err error) {
	query := `
		SELECT 
			c.max_concurrent_jobs, 
			COUNT(j.id) AS active_jobs
		FROM scaleodm_clusters c
		LEFT JOIN scaleodm_job_metadata j ON j.cluster_url = c.cluster_url AND j.job_status IN ('claimed', 'running')
		WHERE c.cluster_url = $1
		GROUP BY c.max_concurrent_jobs
	`
	err = s.db.Pool.QueryRow(ctx, query, clusterID).Scan(&maxJobs, &activeJobs)
	if err != nil {
		// Check if it's a "no rows" error using pgx error
		if err == pgx.ErrNoRows {
			return 0, 0, fmt.Errorf("cluster not found: %s", clusterID)
		}
		return 0, 0, err
	}
	return
}
