-- Tables

CREATE TABLE IF NOT EXISTS scaleodm_clusters (
    cluster_url TEXT PRIMARY KEY,
    max_concurrent_jobs INTEGER DEFAULT 10,  -- Max concurrent processing jobs at one time
    priority_weighting INTEGER DEFAULT 10,   -- 0 lowest, 100 highest
    last_heartbeat TIMESTAMPTZ  -- No default, we wait for a poll
);

CREATE TABLE scaleodm_job_metadata (
    id BIGSERIAL PRIMARY KEY,
    cluster_url TEXT NOT NULL,
    workflow_name TEXT NOT NULL UNIQUE,
    odm_project_id TEXT NOT NULL,
    job_type TEXT DEFAULT 'standard' CONSTRAINT job_type_check
        CHECK (job_type IN ('standard', 'splitmerge')),
    job_status TEXT DEFAULT 'pending' CONSTRAINT job_queue_status_check
        CHECK (status IN ('pending', 'claimed', 'running', 'failed', 'completed')),
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

-- Foreign keys

DO $$ 
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint 
        WHERE conname = 'fk_job_queue_cluster'
    ) THEN
        ALTER TABLE scaleodm_job_queue
            ADD CONSTRAINT fk_job_queue_cluster
            FOREIGN KEY (cluster_url)
            REFERENCES scaleodm_clusters (cluster_url)
            ON DELETE CASCADE;
    END IF;
END $$;

-- Indexes

-- Index for looking up jobs by workflow name
CREATE INDEX IF NOT EXISTS idx_workflow_name 
    ON scaleodm_job_metadata(workflow_name);

-- Index for listing jobs by status
CREATE INDEX IF NOT EXISTS idx_job_status 
    ON scaleodm_job_metadata(status, created_at DESC);

-- Index for project lookups
CREATE INDEX IF NOT EXISTS idx_project_id 
    ON scaleodm_job_metadata(odm_project_id, created_at DESC);
