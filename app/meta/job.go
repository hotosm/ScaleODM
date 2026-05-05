package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/app/observability"
)

type JobMetadata struct {
	ID           int64           `json:"id"`
	WorkflowName string          `json:"workflow_name"`
	ODMProjectID string          `json:"odm_project_id"`
	ReadS3Path   string          `json:"read_s3_path"`
	WriteS3Path  string          `json:"write_s3_path"`
	ODMFlags     json.RawMessage `json:"odm_flags"`
	S3Region     string          `json:"s3_region"`
	JobStatus    string          `json:"job_status"`
	CreatedAt    time.Time       `json:"created_at"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	ErrorMessage *string         `json:"error_message,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

type Store struct {
	db *db.DB
}

func NewStore(db *db.DB) *Store {
	return &Store{db: db}
}

// isDeadlockError checks if an error is a PostgreSQL deadlock error
func isDeadlockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "deadlock") || strings.Contains(errStr, "40P01")
}

// retryOnDeadlock retries a function if it encounters a deadlock error
func retryOnDeadlock(ctx context.Context, maxRetries int, fn func() error) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !isDeadlockError(lastErr) {
			return lastErr
		}
		// Wait before retrying (exponential backoff)
		waitTime := time.Duration(i+1) * 10 * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitTime):
		}
	}
	return lastErr
}

// CreateJob records a new job metadata entry
func (s *Store) CreateJob(ctx context.Context, workflowName, projectID, readPath, writePath string, odmFlags []string, s3Region string) (*JobMetadata, error) {
	flagsJSON, err := json.Marshal(odmFlags)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal odm_flags: %w", err)
	}

	query := `
		INSERT INTO scaleodm_job_metadata
		(workflow_name, odm_project_id, read_s3_path, write_s3_path, odm_flags, s3_region)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, workflow_name, odm_project_id, read_s3_path, write_s3_path,
		          odm_flags, s3_region, job_status, created_at
	`

	var job *JobMetadata
	err = retryOnDeadlock(ctx, 3, func() error {
		job = &JobMetadata{}
		scanErr := s.db.Pool.QueryRow(ctx, query, workflowName, projectID, readPath, writePath, flagsJSON, s3Region).Scan(
			&job.ID, &job.WorkflowName, &job.ODMProjectID, &job.ReadS3Path,
			&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.JobStatus, &job.CreatedAt,
		)
		if scanErr != nil {
			return scanErr
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create job metadata: %w", err)
	}

	return job, nil
}

// GetJob retrieves job metadata by workflow name
func (s *Store) GetJob(ctx context.Context, workflowName string) (*JobMetadata, error) {
	query := `
		SELECT id, workflow_name, odm_project_id, read_s3_path, write_s3_path,
		       odm_flags, s3_region, job_status, created_at, started_at, completed_at,
		       error_message, metadata
		FROM scaleodm_job_metadata
		WHERE workflow_name = $1
	`

	job := &JobMetadata{}
	var startedAt, completedAt sql.NullTime
	var errorMsg sql.NullString
	var metadataJSON []byte

	err := s.db.Pool.QueryRow(ctx, query, workflowName).Scan(
		&job.ID, &job.WorkflowName, &job.ODMProjectID, &job.ReadS3Path,
		&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.JobStatus,
		&job.CreatedAt, &startedAt, &completedAt, &errorMsg, &metadataJSON,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	if errorMsg.Valid {
		job.ErrorMessage = &errorMsg.String
	}
	if len(metadataJSON) > 0 {
		job.Metadata = metadataJSON
	}

	return job, nil
}

// UpdateJobStatus updates the job status from workflow phase
// status should be one of: 'queued', 'claimed', 'running', 'failed', 'completed', 'canceled'
// Note: 'claimed' is an internal state for job queue management (maps to QUEUED/10 in API)
func (s *Store) UpdateJobStatus(ctx context.Context, workflowName, status string, errorMsg *string) error {
	started := time.Now()
	reason := "none"
	if errorMsg != nil && strings.TrimSpace(*errorMsg) != "" {
		reason = "error_message_present"
	}
	query := `
		UPDATE scaleodm_job_metadata
		SET job_status = $2,
		    started_at = CASE 
		        WHEN $2 = 'running' AND started_at IS NULL THEN NOW()
		        ELSE started_at
		    END,
		    completed_at = CASE 
		        WHEN $2 IN ('completed', 'failed', 'canceled') AND completed_at IS NULL THEN NOW()
		        ELSE completed_at
		    END,
		    error_message = $3
		WHERE workflow_name = $1
	`

	// Normalise the error message parameter so the driver always sees either a
	// concrete string or nil, not a pointer type.
	var errValue interface{}
	if errorMsg != nil {
		errValue = *errorMsg
	} else {
		errValue = nil
	}

	result, err := s.db.Pool.Exec(ctx, query, workflowName, status, errValue)
	if err != nil {
		observability.RecordJobStatusUpdate("failure", status, "db_exec_failed", time.Since(started))
		return fmt.Errorf("failed to update job status: %w", err)
	}
	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		observability.RecordJobStatusUpdate("failure", status, "job_not_found", time.Since(started))
		return fmt.Errorf("job not found: %s", workflowName)
	}
	observability.RecordJobStatusUpdate("success", status, reason, time.Since(started))
	return nil
}

// MapArgoPhaseToJobStatus converts Argo workflow phase to database job status
// Returns NodeODM-aligned status labels: 'queued', 'running', 'completed', 'failed', 'canceled'
func MapArgoPhaseToJobStatus(phase string) string {
	switch phase {
	case "Pending":
		return "queued"
	case "Running":
		return "running"
	case "Succeeded":
		return "completed"
	case "Failed", "Error":
		return "failed"
	default:
		return "queued"
	}
}

// IsForwardJobStatusTransition returns true when moving from src to dst is a
// valid forward progression. Reconciliation callers use this to avoid
// regressing a persisted job status from a later state to an earlier one.
func IsForwardJobStatusTransition(src, dst string) bool {
	order := map[string]int{
		"queued":    0,
		"claimed":   1,
		"running":   2,
		"completed": 3,
		"failed":    3,
		"canceled":  3,
	}
	src = strings.ToLower(strings.TrimSpace(src))
	dst = strings.ToLower(strings.TrimSpace(dst))
	s, sOk := order[src]
	d, dOk := order[dst]
	if !sOk || !dOk {
		return false
	}
	return d > s
}

// UpdateJobMetadata stores additional workflow metadata
func (s *Store) UpdateJobMetadata(ctx context.Context, workflowName string, metadata map[string]interface{}) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		UPDATE scaleodm_job_metadata
		SET metadata = $2
		WHERE workflow_name = $1
	`

	result, err := s.db.Pool.Exec(ctx, query, workflowName, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to update job metadata: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("job not found: %s", workflowName)
	}
	return nil
}

// MergeJobMetadata applies a patch onto existing metadata JSON for a workflow.
func (s *Store) MergeJobMetadata(ctx context.Context, workflowName string, patch map[string]interface{}) error {
	if len(patch) == 0 {
		return nil
	}

	return retryOnDeadlock(ctx, 3, func() error {
		tx, err := s.db.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		merged, err := s.mergeMetadataInTx(ctx, tx, workflowName, patch)
		if err != nil {
			return err
		}
		if !merged {
			return fmt.Errorf("job not found: %s", workflowName)
		}

		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) mergeMetadataInTx(ctx context.Context, tx pgx.Tx, workflowName string, patch map[string]interface{}) (bool, error) {
	current := map[string]interface{}{}
	var metadataJSON []byte
	query := `SELECT metadata FROM scaleodm_job_metadata WHERE workflow_name = $1 FOR UPDATE`
	err := tx.QueryRow(ctx, query, workflowName).Scan(&metadataJSON)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &current); err != nil {
			return false, fmt.Errorf("failed to decode existing metadata: %w", err)
		}
	}

	for key, value := range patch {
		if value == nil {
			delete(current, key)
			continue
		}
		current[key] = value
	}

	updatedJSON, err := json.Marshal(current)
	if err != nil {
		return false, fmt.Errorf("failed to encode merged metadata: %w", err)
	}

	updateQuery := `UPDATE scaleodm_job_metadata SET metadata = $2 WHERE workflow_name = $1`
	result, err := tx.Exec(ctx, updateQuery, workflowName, updatedJSON)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

// RestartJobMetadata atomically creates new metadata row, carries forward and patches
// metadata, and removes the old row in a single transaction.
func (s *Store) RestartJobMetadata(
	ctx context.Context,
	oldWorkflowName string,
	newWorkflowName string,
	projectID string,
	readPath string,
	writePath string,
	odmFlags []string,
	s3Region string,
	metadataPatch map[string]interface{},
) error {
	flagsJSON, err := json.Marshal(odmFlags)
	if err != nil {
		return fmt.Errorf("failed to marshal odm_flags: %w", err)
	}

	return retryOnDeadlock(ctx, 3, func() error {
		tx, err := s.db.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		mergedMetadata := map[string]interface{}{}
		var oldMetadataJSON []byte
		selectOldQuery := `SELECT metadata FROM scaleodm_job_metadata WHERE workflow_name = $1 FOR UPDATE`
		err = tx.QueryRow(ctx, selectOldQuery, oldWorkflowName).Scan(&oldMetadataJSON)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("job not found: %s", oldWorkflowName)
		}
		if err != nil {
			return fmt.Errorf("failed to fetch old metadata: %w", err)
		}
		if len(oldMetadataJSON) > 0 {
			if err := json.Unmarshal(oldMetadataJSON, &mergedMetadata); err != nil {
				return fmt.Errorf("failed to decode old metadata: %w", err)
			}
		}
		for key, value := range metadataPatch {
			if value == nil {
				delete(mergedMetadata, key)
				continue
			}
			mergedMetadata[key] = value
		}
		newMetadataJSON, err := json.Marshal(mergedMetadata)
		if err != nil {
			return fmt.Errorf("failed to encode new metadata: %w", err)
		}

		insertQuery := `
			INSERT INTO scaleodm_job_metadata
			(workflow_name, odm_project_id, read_s3_path, write_s3_path, odm_flags, s3_region, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`
		if _, err := tx.Exec(ctx, insertQuery, newWorkflowName, projectID, readPath, writePath, flagsJSON, s3Region, newMetadataJSON); err != nil {
			return fmt.Errorf("failed to insert restarted job metadata: %w", err)
		}

		deleteQuery := `DELETE FROM scaleodm_job_metadata WHERE workflow_name = $1`
		deleteResult, err := tx.Exec(ctx, deleteQuery, oldWorkflowName)
		if err != nil {
			return fmt.Errorf("failed to delete old job metadata: %w", err)
		}
		if deleteResult.RowsAffected() == 0 {
			return fmt.Errorf("job not found: %s", oldWorkflowName)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("failed to commit restart metadata transaction: %w", err)
		}
		return nil
	})
}

// ListJobs retrieves jobs with optional filters
func (s *Store) ListJobs(ctx context.Context, status, projectID string, limit int) ([]*JobMetadata, error) {
	query := `
		SELECT id, workflow_name, odm_project_id, read_s3_path, write_s3_path,
		       odm_flags, s3_region, job_status, created_at, started_at, completed_at,
		       error_message, metadata
		FROM scaleodm_job_metadata
		WHERE 1=1
	`
	args := []interface{}{}
	argCount := 0

	if status != "" {
		argCount++
		query += fmt.Sprintf(" AND job_status = $%d", argCount)
		args = append(args, status)
	}

	if projectID != "" {
		argCount++
		query += fmt.Sprintf(" AND odm_project_id ILIKE $%d", argCount)
		args = append(args, "%"+projectID+"%")
	}

	query += " ORDER BY created_at DESC"

	if limit > 0 {
		argCount++
		query += fmt.Sprintf(" LIMIT $%d", argCount)
		args = append(args, limit)
	}

	rows, err := s.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}
	defer rows.Close()

	jobs := []*JobMetadata{}
	for rows.Next() {
		job := &JobMetadata{}
		var startedAt, completedAt sql.NullTime
		var errorMsg sql.NullString
		var metadataJSON []byte

		err := rows.Scan(
			&job.ID, &job.WorkflowName, &job.ODMProjectID, &job.ReadS3Path,
			&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.JobStatus,
			&job.CreatedAt, &startedAt, &completedAt, &errorMsg, &metadataJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan job: %w", err)
		}

		if startedAt.Valid {
			job.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		if errorMsg.Valid {
			job.ErrorMessage = &errorMsg.String
		}
		if len(metadataJSON) > 0 {
			job.Metadata = metadataJSON
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// ListActiveJobs returns jobs that are not yet in a terminal state and were
// created on or after since. Used by the background reconciler to keep the DB
// scan bounded: filtering at the SQL level means the query cost stays
// proportional to genuinely active jobs rather than growing with all-time
// history.
func (s *Store) ListActiveJobs(ctx context.Context, since time.Time) ([]*JobMetadata, error) {
	query := `
		SELECT id, workflow_name, odm_project_id, read_s3_path, write_s3_path,
		       odm_flags, s3_region, job_status, created_at, started_at, completed_at,
		       error_message, metadata
		FROM scaleodm_job_metadata
		WHERE job_status NOT IN ('completed', 'failed', 'canceled')
		  AND created_at >= $1
		ORDER BY created_at DESC
	`
	rows, err := s.db.Pool.Query(ctx, query, since)
	if err != nil {
		return nil, fmt.Errorf("failed to list active jobs: %w", err)
	}
	defer rows.Close()

	jobs := []*JobMetadata{}
	for rows.Next() {
		job := &JobMetadata{}
		var startedAt, completedAt sql.NullTime
		var errorMsg sql.NullString
		var metadataJSON []byte

		err := rows.Scan(
			&job.ID, &job.WorkflowName, &job.ODMProjectID, &job.ReadS3Path,
			&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.JobStatus,
			&job.CreatedAt, &startedAt, &completedAt, &errorMsg, &metadataJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan job: %w", err)
		}

		if startedAt.Valid {
			job.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		if errorMsg.Valid {
			job.ErrorMessage = &errorMsg.String
		}
		if len(metadataJSON) > 0 {
			job.Metadata = metadataJSON
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// DeleteJob removes job metadata
func (s *Store) DeleteJob(ctx context.Context, workflowName string) error {
	query := `DELETE FROM scaleodm_job_metadata WHERE workflow_name = $1`
	result, err := s.db.Pool.Exec(ctx, query, workflowName)
	if err != nil {
		return fmt.Errorf("failed to delete job: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("job not found")
	}

	return nil
}
