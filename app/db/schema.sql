-- Tables

CREATE TABLE IF NOT EXISTS scaleodm_job_metadata (
    id BIGSERIAL PRIMARY KEY,
    workflow_name TEXT NOT NULL UNIQUE,
    odm_project_id TEXT NOT NULL,
    job_type TEXT DEFAULT 'standard' CONSTRAINT job_type_check
        CHECK (job_type IN ('standard', 'splitmerge')),
    job_status TEXT DEFAULT 'queued' CONSTRAINT job_queue_status_check
        CHECK (job_status IN ('queued', 'claimed', 'running', 'failed', 'completed', 'canceled')),
    read_s3_path TEXT NOT NULL,
    write_s3_path TEXT NOT NULL,
    odm_flags JSONB,
    s3_region TEXT DEFAULT 'us-east-1',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    error_message TEXT,
    metadata JSONB
);

-- Indexes

-- Index for looking up jobs by workflow name
CREATE INDEX IF NOT EXISTS idx_workflow_name
    ON scaleodm_job_metadata(workflow_name);

-- Index for listing jobs by status
CREATE INDEX IF NOT EXISTS idx_job_status
    ON scaleodm_job_metadata(job_status, created_at DESC);

-- Index for project lookups
CREATE INDEX IF NOT EXISTS idx_project_id
    ON scaleodm_job_metadata(odm_project_id, created_at DESC);
