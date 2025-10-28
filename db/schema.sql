-- Tables

CREATE TABLE IF NOT EXISTS scaleodm_clusters (
    cluster_url TEXT PRIMARY KEY,
    max_concurrent_jobs INTEGER DEFAULT 10,  -- Max concurrent processing jobs at one time
    priority_weighting INTEGER DEFAULT 10,   -- 0 lowest, 100 highest
    last_heartbeat TIMESTAMPTZ  -- No default, we wait for a poll
);

CREATE TABLE IF NOT EXISTS scaleodm_job_queue (
    id BIGSERIAL PRIMARY KEY,
    cluster_url TEXT NOT NULL,
    job_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT DEFAULT 'pending' CONSTRAINT job_queue_status_check
        CHECK (status IN ('pending', 'claimed', 'running', 'failed', 'completed')),
    priority INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    claimed_by TEXT,
    duration_seconds DOUBLE PRECISION,
    completed_at TIMESTAMPTZ,
    retry_count INTEGER DEFAULT 0,
    error_message TEXT
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

CREATE INDEX IF NOT EXISTS idx_job_queue_pending 
    ON scaleodm_job_queue(cluster_url, status, priority DESC, created_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_job_queue_claimed 
    ON scaleodm_job_queue(claimed_by, status)
    WHERE status IN ('claimed', 'running');

