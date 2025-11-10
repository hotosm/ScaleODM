// We implement a NodeODM-compatible API so ScaleODM can be used
// as a direct replacement (with scaling built in).

package api

// import (
// 	"fmt"
// 	"context"
// 	"net/http"

// 	"github.com/danielgtaylor/huma/v2"
// 	_ "github.com/danielgtaylor/huma/v2/formats/cbor"

// 	"github.com/hotosm/scaleodm/app/meta"
// )

// // registerRoutes registers all API operations
// func (a *API) registerScaleODMRoutes() {
// 	// Cluster status
// 	huma.Register(a.api, huma.Operation{
// 		OperationID: "get-cluster-status",
// 		Method:      http.MethodGet,
// 		Path:        "/api/v1/clusters/{clusterID}/status",
// 		Summary:     "Get cluster status",
// 		Tags:        []string{"Clusters"},
// 	}, func(ctx context.Context, input *struct {
// 		ClusterURL string `path:"clusterID"`
// 	}) (*ClusterStatusResponse, error) {
// 		maxJobs, activeJobs, err := a.queue.GetClusterCapacity(ctx, input.ClusterURL)
// 		if err != nil {
// 			maxJobs, activeJobs = 10, 0
// 		}

// 		resp := &ClusterStatusResponse{}
// 		resp.Body.ClusterURL = input.ClusterURL
// 		resp.Body.ActiveJobs = activeJobs
// 		resp.Body.MaxConcurrent = maxJobs
// 		resp.Body.AvailableWorkers = max(0, maxJobs-activeJobs)
// 		return resp, nil
// 	})

// 	// Update cluster details
// 	huma.Register(a.api, huma.Operation{
// 		OperationID: "update-cluster",
// 		Method:      http.MethodPost,
// 		Path:        "/api/v1/clusters/{clusterID}",
// 		Summary:     "Update cluster",
// 		Tags:        []string{"Clusters"},
// 	}, func(ctx context.Context, input *struct {
// 		ClusterURL string                `path:"clusterID"`
// 		Body       CapacityUpdateRequest `json:"body"`
// 	}) (*MessageResponse, error) {
// 		if input.Body.MaxConcurrentJobs <= 0 {
// 			return nil, huma.NewError(400, "max_concurrent_jobs must be positive")
// 		}

// 		if input.Body.PriorityWeighting < 1 || input.Body.PriorityWeighting > 100 {
// 			return nil, huma.NewError(400, "priority_weighting must be between 1 and 100")
// 		}

// 		err := a.queue.UpdateClusterDetails(ctx, input.ClusterURL, input.Body.MaxConcurrentJobs, input.Body.PriorityWeighting)
// 		if err != nil {
// 			return nil, huma.NewError(500, "Failed to update capacity", err)
// 		}

// 		resp := &MessageResponse{}
// 		resp.Body.Message = "Cluster capacity updated successfully"
// 		return resp, nil
// 	})

// 	// Cluster jobs
// 	huma.Register(a.api, huma.Operation{
// 		OperationID: "get-cluster-jobs",
// 		Method:      http.MethodGet,
// 		Path:        "/api/v1/clusters/{clusterID}/jobs",
// 		Summary:     "List jobs for a cluster",
// 		Tags:        []string{"Clusters"},
// 	}, func(ctx context.Context, input *struct {
// 		ClusterURL    string `path:"clusterID"`
// 		ClusterStatus string `query:"status"`
// 		Limit         int    `query:"limit" minimum:"1" maximum:"1000" default:"50"`
// 	}) (*ClusterJobsResponse, error) {
// 		jobs, err := a.queue.ListJobs(ctx, input.ClusterURL, input.ClusterStatus, "", input.Limit)
// 		if err != nil {
// 			return nil, huma.NewError(500, "Failed to get jobs", err)
// 		}

// 		resp := &ClusterJobsResponse{}
// 		resp.Body.ClusterURL = input.ClusterURL
// 		resp.Body.Jobs = make([]JobResponse, len(jobs))
// 		for i, j := range jobs {
// 			resp.Body.Jobs[i] = *jobToResponse(j)
// 		}
// 		resp.Body.Total = len(jobs)
// 		return resp, nil
// 	})

// 	// Queue stats
// 	huma.Register(a.api, huma.Operation{
// 		OperationID: "get-queue-stats",
// 		Method:      http.MethodGet,
// 		Path:        "/api/v1/queue/stats",
// 		Summary:     "Get queue statistics",
// 		Tags:        []string{"Queue"},
// 	}, func(ctx context.Context, input *struct{}) (*QueueStatsResponse, error) {
// 		stats, err := a.queue.GetQueueStatistics(ctx)
// 		if err != nil {
// 			return nil, huma.NewError(500, "Failed to get stats", err)
// 		}

// 		resp := &QueueStatsResponse{}
// 		if stats != nil {
// 			resp.Body.TotalJobs = getInt(stats, "total_jobs")
// 			resp.Body.PendingJobs = getInt(stats, "pending_jobs")
// 			resp.Body.ProcessingJobs = getInt(stats, "processing_jobs")
// 			resp.Body.CompletedJobs = getInt(stats, "completed_jobs")
// 			resp.Body.FailedJobs = getInt(stats, "failed_jobs")
// 			resp.Body.CancelledJobs = getInt(stats, "cancelled_jobs")
// 			resp.Body.ByCluster = getMap(stats, "by_cluster")
// 			resp.Body.ByJobType = getMap(stats, "by_job_type")
// 			if v, ok := stats["avg_processing_time"].(float64); ok {
// 				resp.Body.AvgProcessingTime = &v
// 			}
// 		}
// 		return resp, nil
// 	})
// }

// // jobToResponse converts an internal Job struct to an API-compatible JobResponse
// // jobToResponse converts a queue.Job to an API JobResponse
// func jobToResponse(j *meta.Job) *JobResponse {
// 	resp := &JobResponse{
// 		ID:        fmt.Sprintf("%d", j.ID),
// 		JobStatus: string(j.Status),
// 		CreatedAt: j.CreatedAt,
// 		Metadata:  make(map[string]interface{}),
// 	}

// 	// Map optional timestamps
// 	if j.ClaimedAt != nil && j.ClaimedAt.Valid {
// 		start := j.ClaimedAt.Time
// 		resp.StartedAt = &start
// 	}

// 	if j.CompletedAt != nil && j.CompletedAt.Valid {
// 		end := j.CompletedAt.Time
// 		resp.CompletedAt = &end
// 	}

// 	// Map optional error
// 	if j.ErrorMessage != nil {
// 		resp.ErrorMessage = j.ErrorMessage
// 	}

// 	// Metadata
// 	if j.ClusterURL != "" {
// 		resp.Metadata["cluster_url"] = j.ClusterURL
// 	}
// 	if j.JobType != "" {
// 		resp.Metadata["job_type"] = j.JobType
// 	}
// 	if len(j.Payload) > 0 {
// 		resp.Metadata["payload"] = string(j.Payload)
// 	}
// 	if j.Priority > 0 {
// 		resp.Metadata["priority"] = j.Priority
// 	}
// 	if j.ClaimedBy != nil {
// 		resp.Metadata["claimed_by"] = *j.ClaimedBy
// 	}

// 	return resp
// }

// // Helpers for stats
// func getInt(m map[string]interface{}, key string) int {
// 	if v, ok := m[key].(int); ok {
// 		return v
// 	}
// 	return 0
// }

// func getMap(m map[string]interface{}, key string) map[string]int {
// 	if v, ok := m[key].(map[string]int); ok {
// 		return v
// 	}
// 	return nil
// }
