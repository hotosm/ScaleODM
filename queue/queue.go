package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hotosm/scaleodm/db"
)

type Cluster struct {
	ClusterURL        string
	MaxConcurrentJobs int
	PriorityWeighting int
	LastHeartbeat     sql.NullTime
}

type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusClaimed   JobStatus = "claimed"
	StatusRunning   JobStatus = "running"
	StatusFailed    JobStatus = "failed"
	StatusCompleted JobStatus = "completed"
)

type Job struct {
	ID           int64           `json:"id"`
	ClusterURL   string          `json:"cluster_url"`
	JobType      string          `json:"job_type"`
	Payload      json.RawMessage `json:"payload"`
	Status       JobStatus       `json:"status"`
	Priority     int             `json:"priority"`
	CreatedAt    time.Time       `json:"created_at"`
	ClaimedAt    *sql.NullTime   `json:"claimed_at,omitempty"`
	ClaimedBy    *string         `json:"claimed_by,omitempty"`
	CompletedAt  *sql.NullTime   `json:"completed_at,omitempty"`
	ErrorMessage *string         `json:"error_message,omitempty"`
}

type Queue struct {
	db *db.DB
}

func NewQueue(db *db.DB) *Queue {
	return &Queue{db: db}
}

// Enqueue adds a new job to the queue
func (q *Queue) Enqueue(ctx context.Context, clusterID, jobType string, payload interface{}, priority int) (*Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	query := `
		INSERT INTO scaleodm_job_queue (cluster_url, job_type, payload, priority)
		VALUES ($1, $2, $3, $4)
		RETURNING id, cluster_url, job_type, payload, status, priority, created_at
	`

	job := &Job{}
	err = q.db.Pool.QueryRow(ctx, query, clusterID, jobType, payloadJSON, priority).Scan(
		&job.ID, &job.ClusterURL, &job.JobType, &job.Payload,
		&job.Status, &job.Priority, &job.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue job: %w", err)
	}

	return job, nil
}

// ClaimJob attempts to claim the next available job using SKIP LOCKED
func (q *Queue) ClaimJob(ctx context.Context, clusterID, workerID string) (*Job, error) {
	query := `
		UPDATE scaleodm_job_queue
		SET status = 'claimed', claimed_at = NOW(), claimed_by = $1
		WHERE id = (
			SELECT id FROM scaleodm_job_queue
			WHERE (cluster_url = $2 OR cluster_url = 'global') 
			  AND status = 'pending'
			ORDER BY 
				CASE WHEN cluster_url = $2 THEN 0 ELSE 1 END,  -- Prefer local jobs
				priority DESC, 
				created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, cluster_url, job_type, payload, status, priority, 
		          created_at, claimed_at, claimed_by
	`

	job := &Job{}
	err := q.db.Pool.QueryRow(ctx, query, workerID, clusterID).Scan(
		&job.ID, &job.ClusterURL, &job.JobType, &job.Payload,
		&job.Status, &job.Priority, &job.CreatedAt, &job.ClaimedAt, &job.ClaimedBy,
	)

	if err == pgx.ErrNoRows {
		return nil, nil // No jobs available
	}
	if err != nil {
		return nil, fmt.Errorf("failed to claim job: %w", err)
	}

	return job, nil
}

// CompleteJob marks a job as completed
func (q *Queue) CompleteJob(ctx context.Context, jobID int64) error {
	query := `
		UPDATE scaleodm_job_queue
		SET status=$1, completed_at=NOW(), duration_seconds = EXTRACT(EPOCH FROM (NOW() - claimed_at))
		WHERE id=$2
	`
	_, err := q.db.Pool.Exec(ctx, query, StatusCompleted, jobID)
	if err != nil {
		return fmt.Errorf("failed to complete job: %w", err)
	}
	return nil
}

// FailJob marks a job as failed with an error message
func (q *Queue) FailJob(ctx context.Context, jobID int64, errorMsg string) error {
	query := `
		UPDATE scaleodm_job_queue
		SET status = $1, completed_at = NOW(), error_message = $2
		WHERE id = $3
	`
	_, err := q.db.Pool.Exec(ctx, query, StatusFailed, errorMsg, jobID)
	if err != nil {
		return fmt.Errorf("failed to mark job as failed: %w", err)
	}
	return nil
}

func (q *Queue) ListClusters(ctx context.Context) ([]*Cluster, error) {
	rows, err := q.db.Pool.Query(ctx, `
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

func (q *Queue) UpdateClusterHeartbeat(ctx context.Context, clusterID string) error {
	_, err := q.db.Pool.Exec(ctx, `
		UPDATE scaleodm_clusters
		SET last_heartbeat = NOW()
		WHERE cluster_url = $1
	`, clusterID)
	return err
}

// UpdateClusterDetails updates the capacity information for a cluster
func (q *Queue) UpdateClusterDetails(ctx context.Context, clusterID string, maxJobs, priorityWeighting int) error {
	query := `
		INSERT INTO scaleodm_clusters (cluster_url, max_concurrent_jobs, priority_weighting, last_heartbeat)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (cluster_url) 
		DO UPDATE SET 
			max_concurrent_jobs = EXCLUDED.max_concurrent_jobs,
			priority_weighting = EXCLUDED.priority_weighting,
			last_heartbeat = NOW()
	`
	_, err := q.db.Pool.Exec(ctx, query, clusterID, maxJobs, priorityWeighting)
	return err
}

// Find the number of jobs running currently, with max jobs that can run
func (q *Queue) GetClusterCapacity(ctx context.Context, clusterID string) (maxJobs, activeJobs int, err error) {
	query := `
		SELECT c.max_concurrent_jobs, COUNT(j.id) AS active_jobs
		FROM scaleodm_clusters c
		LEFT JOIN scaleodm_job_queue j ON j.cluster_url = c.cluster_url AND j.status IN ('claimed', 'running')
		WHERE c.cluster_url = $1
		GROUP BY c.max_concurrent_jobs
	`
	err = q.db.Pool.QueryRow(ctx, query, clusterID).Scan(&maxJobs, &activeJobs)
	return
}

// Add these methods to queue.go

// GetJob retrieves a job by ID
func (q *Queue) GetJob(ctx context.Context, jobID int64) (*Job, error) {
	query := `
		SELECT id, cluster_url, job_type, payload, status, priority,
		       created_at, claimed_at, claimed_by, completed_at, error_message
		FROM scaleodm_job_queue
		WHERE id = $1
	`

	job := &Job{}
	err := q.db.Pool.QueryRow(ctx, query, jobID).Scan(
		&job.ID, &job.ClusterURL, &job.JobType, &job.Payload,
		&job.Status, &job.Priority, &job.CreatedAt, &job.ClaimedAt,
		&job.ClaimedBy, &job.CompletedAt, &job.ErrorMessage,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return job, nil
}

// ListJobs retrieves jobs with optional filters
func (q *Queue) ListJobs(ctx context.Context, clusterID, status, jobType string, limit int) ([]*Job, error) {
	baseQuery := `
		SELECT id, cluster_url, job_type, payload, status, priority,
		       created_at, claimed_at, claimed_by, completed_at, error_message
		FROM scaleodm_job_queue
		WHERE 1=1
	`
	args := []interface{}{}
	argCount := 0

	if clusterID != "" {
		argCount++
		baseQuery += fmt.Sprintf(" AND cluster_url = $%d", argCount)
		args = append(args, clusterID)
	}

	if status != "" {
		argCount++
		baseQuery += fmt.Sprintf(" AND status = $%d", argCount)
		args = append(args, status)
	}

	if jobType != "" {
		argCount++
		baseQuery += fmt.Sprintf(" AND job_type = $%d", argCount)
		args = append(args, jobType)
	}

	baseQuery += " ORDER BY created_at DESC"

	if limit > 0 {
		argCount++
		baseQuery += fmt.Sprintf(" LIMIT $%d", argCount)
		args = append(args, limit)
	}

	rows, err := q.db.Pool.Query(ctx, baseQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}
	defer rows.Close()

	jobs := []*Job{}
	for rows.Next() {
		job := &Job{}
		err := rows.Scan(
			&job.ID, &job.ClusterURL, &job.JobType, &job.Payload,
			&job.Status, &job.Priority, &job.CreatedAt, &job.ClaimedAt,
			&job.ClaimedBy, &job.CompletedAt, &job.ErrorMessage,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan job: %w", err)
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// Retry a failed job
func (q *Queue) RetryJob(ctx context.Context, jobID int64) error {
	_, err := q.db.Pool.Exec(ctx, `
		UPDATE scaleodm_job_queue
		SET status='pending', claimed_at=NULL, claimed_by=NULL, completed_at=NULL, error_message=NULL,
		    retry_count = retry_count + 1
		WHERE id=$1 AND status='failed'
	`, jobID)
	return err
}

// Cancel a pending job
func (q *Queue) CancelJob(ctx context.Context, jobID int64) error {
	query := `
		UPDATE scaleodm_job_queue
		SET status = 'failed', error_message = 'Cancelled by user'
		WHERE id = $1 AND status = 'pending'
	`
	result, err := q.db.Pool.Exec(ctx, query, jobID)
	if err != nil {
		return fmt.Errorf("failed to cancel job: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("job not found or not pending")
	}

	return nil
}

// GetQueueStatistics returns overall queue statistics
func (q *Queue) GetQueueStatistics(ctx context.Context) (map[string]interface{}, error) {
	query := `
		SELECT
			cluster_url,
			job_type,
			COUNT(*) FILTER (WHERE status = 'pending') AS pending,
			COUNT(*) FILTER (WHERE status = 'processing') AS processing,
			COUNT(*) FILTER (WHERE status = 'completed') AS completed,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed
		FROM scaleodm_job_queue
		GROUP BY cluster_url, job_type
	`

	var total, pending, processing, completed, failed int
	err := q.db.Pool.QueryRow(ctx, query).Scan(
		&total, &pending, &processing, &completed, &failed,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get queue statistics: %w", err)
	}

	stats := map[string]interface{}{
		"total_jobs":      total,
		"pending_jobs":    pending,
		"processing_jobs": processing,
		"completed_jobs":  completed,
		"failed_jobs":     failed,
	}

	return stats, nil
}

// GetJobOutput retrieves the output/result data for a completed job
func (q *Queue) GetJobOutput(ctx context.Context, jobID int64) ([]string, error) {
	query := `
		SELECT output_files FROM scaleodm_job_queue
		WHERE id = $1 AND status = 'completed'
	`

	var outputJSON []byte
	err := q.db.Pool.QueryRow(ctx, query, jobID).Scan(&outputJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to get job output: %w", err)
	}

	var output []string
	if err := json.Unmarshal(outputJSON, &output); err != nil {
		return nil, fmt.Errorf("failed to parse output: %w", err)
	}

	return output, nil
}

// GetAssetURL retrieves a download URL for a specific job asset
func (q *Queue) GetAssetURL(ctx context.Context, jobID int64, assetName string) (string, error) {
	query := `
		SELECT asset_urls FROM scaleodm_job_queue
		WHERE id = $1
	`

	var assetsJSON []byte
	err := q.db.Pool.QueryRow(ctx, query, jobID).Scan(&assetsJSON)
	if err != nil {
		return "", fmt.Errorf("failed to get assets: %w", err)
	}

	var assets map[string]string
	if err := json.Unmarshal(assetsJSON, &assets); err != nil {
		return "", fmt.Errorf("failed to parse assets: %w", err)
	}

	url, ok := assets[assetName]
	if !ok {
		return "", fmt.Errorf("asset not found: %s", assetName)
	}

	return url, nil
}

// DeleteJob removes a job from the queue
func (q *Queue) DeleteJob(ctx context.Context, jobID int64) error {
	query := `DELETE FROM scaleodm_job_queue WHERE id = $1`
	_, err := q.db.Pool.Exec(ctx, query, jobID)
	if err != nil {
		return fmt.Errorf("failed to delete job: %w", err)
	}
	return nil
}

// MarkJobForwarded marks a job as forwarded to a federated instance
func (q *Queue) MarkJobForwarded(ctx context.Context, jobID int64, instanceID string) error {
	query := `
		UPDATE scaleodm_job_queue
		SET status = 'forwarded', 
		    forwarded_to = $2,
		    forwarded_at = NOW()
		WHERE id = $1
	`
	_, err := q.db.Pool.Exec(ctx, query, jobID, instanceID)
	if err != nil {
		return fmt.Errorf("failed to mark job as forwarded: %w", err)
	}
	return nil
}

// UpdateJobStatus updates the status of a job (used for sync from federated instances)
func (q *Queue) UpdateJobStatus(ctx context.Context, jobID int64, status string, errorMsg string) error {
	query := `
		UPDATE scaleodm_job_queue
		SET status = $2, error_message = $3
		WHERE id = $1
	`
	_, err := q.db.Pool.Exec(ctx, query, jobID, status, errorMsg)
	if err != nil {
		return fmt.Errorf("failed to update job status: %w", err)
	}
	return nil
}

// StoreJobOutput stores the output files/URLs for a completed job
func (q *Queue) StoreJobOutput(ctx context.Context, jobID int64, outputFiles []string, assetURLs map[string]string) error {
	outputJSON, err := json.Marshal(outputFiles)
	if err != nil {
		return fmt.Errorf("failed to marshal output: %w", err)
	}

	assetsJSON, err := json.Marshal(assetURLs)
	if err != nil {
		return fmt.Errorf("failed to marshal assets: %w", err)
	}

	query := `
		UPDATE scaleodm_job_queue
		SET output_files = $2, asset_urls = $3
		WHERE id = $1
	`
	_, err = q.db.Pool.Exec(ctx, query, jobID, outputJSON, assetsJSON)
	if err != nil {
		return fmt.Errorf("failed to store job output: %w", err)
	}
	return nil
}

// UpdateJobProgress updates the progress percentage and message for a job
func (q *Queue) UpdateJobProgress(ctx context.Context, jobID int64, progress float64, message string) error {
	query := `
		UPDATE scaleodm_job_queue
		SET progress = $2, progress_message = $3, last_update = NOW()
		WHERE id = $1
	`
	_, err := q.db.Pool.Exec(ctx, query, jobID, progress, message)
	if err != nil {
		return fmt.Errorf("failed to update job progress: %w", err)
	}
	return nil
}

// GetJobProgress retrieves the current progress of a job
func (q *Queue) GetJobProgress(ctx context.Context, jobID int64) (float64, string, error) {
	query := `
		SELECT progress, progress_message FROM scaleodm_job_queue
		WHERE id = $1
	`

	var progress float64
	var message string
	err := q.db.Pool.QueryRow(ctx, query, jobID).Scan(&progress, &message)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get job progress: %w", err)
	}

	return progress, message, nil
}

func (q *Queue) HealthCheck(ctx context.Context) error {
	return q.db.HealthCheck(ctx)
}
