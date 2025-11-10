package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hotosm/scaleodm/app/db"
)

type JobMetadata struct {
	ID           int64           `json:"id"`
	WorkflowName string          `json:"workflow_name"`
	ODMProjectID string          `json:"odm_project_id"`
	ReadS3Path   string          `json:"read_s3_path"`
	WriteS3Path  string          `json:"write_s3_path"`
	ODMFlags     json.RawMessage `json:"odm_flags"`
	S3Region     string          `json:"s3_region"`
	Status       string          `json:"status"`
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
		          odm_flags, s3_region, status, created_at
	`

	job := &JobMetadata{}
	err = s.db.Pool.QueryRow(ctx, query, workflowName, projectID, readPath, writePath, flagsJSON, s3Region).Scan(
		&job.ID, &job.WorkflowName, &job.ODMProjectID, &job.ReadS3Path,
		&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.Status, &job.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create job metadata: %w", err)
	}

	return job, nil
}

// GetJob retrieves job metadata by workflow name
func (s *Store) GetJob(ctx context.Context, workflowName string) (*JobMetadata, error) {
	query := `
		SELECT id, workflow_name, odm_project_id, read_s3_path, write_s3_path,
		       odm_flags, s3_region, status, created_at, started_at, completed_at,
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
		&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.Status,
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
func (s *Store) UpdateJobStatus(ctx context.Context, workflowName, status string, errorMsg *string) error {
	query := `
		UPDATE scaleodm_job_metadata
		SET status = $2,
		    started_at = CASE 
		        WHEN $2 = 'Running' AND started_at IS NULL THEN NOW()
		        ELSE started_at
		    END,
		    completed_at = CASE 
		        WHEN $2 IN ('Succeeded', 'Failed', 'Error') AND completed_at IS NULL THEN NOW()
		        ELSE completed_at
		    END,
		    error_message = $3
		WHERE workflow_name = $1
	`

	_, err := s.db.Pool.Exec(ctx, query, workflowName, status, errorMsg)
	if err != nil {
		return fmt.Errorf("failed to update job status: %w", err)
	}
	return nil
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

	_, err = s.db.Pool.Exec(ctx, query, workflowName, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to update job metadata: %w", err)
	}
	return nil
}

// ListJobs retrieves jobs with optional filters
func (s *Store) ListJobs(ctx context.Context, status, projectID string, limit int) ([]*JobMetadata, error) {
	query := `
		SELECT id, workflow_name, odm_project_id, read_s3_path, write_s3_path,
		       odm_flags, s3_region, status, created_at, started_at, completed_at,
		       error_message, metadata
		FROM scaleodm_job_metadata
		WHERE 1=1
	`
	args := []interface{}{}
	argCount := 0

	if status != "" {
		argCount++
		query += fmt.Sprintf(" AND status = $%d", argCount)
		args = append(args, status)
	}

	if projectID != "" {
		argCount++
		query += fmt.Sprintf(" AND odm_project_id = $%d", argCount)
		args = append(args, projectID)
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
			&job.WriteS3Path, &job.ODMFlags, &job.S3Region, &job.Status,
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
