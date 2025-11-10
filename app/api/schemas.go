// General schemas not specific to a router

package api

// General

type HealthResponse struct {
	Body struct {
		HealthStatus string `json:"status" example:"healthy"`
		Timestamp    string `json:"timestamp" example:"2025-04-05T12:00:00Z"`
	}
}

type MessageResponse struct {
	Body struct {
		Message string `json:"message"`
	}
}

// ScaleODM

type AddClusterRequest struct {
	ClusterURL string                 `json:"cluster_url" doc:"Cluster to enqueue job on"`
	JobType    string                 `json:"job_type" doc:"Type of job (e.g. odm)"`
	Payload    map[string]interface{} `json:"payload" doc:"Job payload"`
	Priority   int                    `json:"priority,omitempty" doc:"Job priority (higher = sooner)"`
}

type ClusterStatusResponse struct {
	Body struct {
		ClusterURL        string `json:"cluster_url"`
		MaxConcurrent     int    `json:"max_concurrent_jobs"`
		ActiveJobs        int    `json:"active_jobs"`
		PriorityWeighting int    `json:"priority_weighting"`
		AvailableWorkers  int    `json:"available_workers"`
	} `json:"body"`
}

type ClusterJobsResponse struct {
	Body struct {
		ClusterURL string        `json:"cluster_url"`
		Jobs       []JobResponse `json:"jobs"`
		Total      int           `json:"total"`
	} `json:"body"`
}

type CapacityUpdateRequest struct {
	MaxConcurrentJobs int `json:"max_concurrent_jobs" minimum:"1" example:"10"`
	PriorityWeighting int `json:"priority_weighting" minimum:"1" maximum:"100"`
}

type QueueStatsResponse struct {
	Body struct {
		TotalJobs         int            `json:"total_jobs"`
		PendingJobs       int            `json:"pending_jobs"`
		ProcessingJobs    int            `json:"processing_jobs"`
		CompletedJobs     int            `json:"completed_jobs"`
		FailedJobs        int            `json:"failed_jobs"`
		CancelledJobs     int            `json:"cancelled_jobs"`
		ByCluster         map[string]int `json:"by_cluster,omitempty"`
		ByJobType         map[string]int `json:"by_job_type,omitempty"`
		AvgProcessingTime *float64       `json:"avg_processing_time,omitempty"`
	} `json:"body"`
}
