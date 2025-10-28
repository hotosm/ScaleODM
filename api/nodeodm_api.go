// We implement a NodeODM-compatible API so ScaleODM can be used
// as a direct replacement (with scaling built in).

package api

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"

	"github.com/hotosm/scaleodm/queue"
)

// registerRoutes registers all API operations
func (a *API) registerNodeODMRoutes() {
	// Enqueue job
	huma.Register(a.api, huma.Operation{
		OperationID: "enqueue-job",
		Method:      http.MethodPost,
		Path:        "/api/v1/jobs",
		Summary:     "Enqueue a new job",
		Description: "Adds a job to the processing queue",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		Body EnqueueRequest `json:"body"`
	}) (*EnqueueResponse, error) {
		req := input.Body

		if req.ClusterURL == "" {
			return nil, huma.NewError(400, "cluster_url is required")
		}
		if req.JobType == "" {
			return nil, huma.NewError(400, "job_type is required")
		}
		if req.Payload == nil {
			return nil, huma.NewError(400, "payload is required")
		}

		job, err := a.queue.Enqueue(ctx, req.ClusterURL, req.JobType, req.Payload, req.Priority)
		if err != nil {
			log.Printf("Failed to enqueue job: %v", err)
			return nil, huma.NewError(500, "Failed to enqueue job", err)
		}

		resp := &EnqueueResponse{}
		resp.Body.JobID = job.ID
		resp.Body.JobStatus = job.Status
		resp.Body.CreatedAt = job.CreatedAt
		return resp, nil
	})

	// Get job
	huma.Register(a.api, huma.Operation{
		OperationID: "get-job",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs/{jobID}",
		Summary:     "Get job by ID",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		JobID int64 `path:"jobID" minimum:"1"`
	}) (*JobResponse, error) {
		job, err := a.queue.GetJob(ctx, input.JobID)
		if err != nil {
			return nil, huma.NewError(500, "Failed to retrieve job", err)
		}
		if job == nil {
			return nil, huma.NewError(404, "Job not found")
		}
		return jobToResponse(job), nil
	})

	// List jobs
	huma.Register(a.api, huma.Operation{
		OperationID: "list-jobs",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs",
		Summary:     "List jobs",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		ClusterURL string `query:"cluster_url"`
		JobStatus  string `query:"status"`
		JobType    string `query:"job_type"`
		Limit      int    `query:"limit" minimum:"1" maximum:"1000" default:"50"`
	}) (*JobListResponse, error) {
		jobs, err := a.queue.ListJobs(ctx, input.ClusterURL, input.JobStatus, input.JobType, input.Limit)
		if err != nil {
			return nil, huma.NewError(500, "Failed to list jobs", err)
		}

		resp := &JobListResponse{}
		resp.Body.Jobs = make([]JobResponse, len(jobs))
		for i, j := range jobs {
			resp.Body.Jobs[i] = *jobToResponse(j)
		}
		resp.Body.Total = len(jobs)
		return resp, nil
	})

	// Cancel job
	huma.Register(a.api, huma.Operation{
		OperationID: "cancel-job",
		Method:      http.MethodDelete,
		Path:        "/api/v1/jobs/{jobID}",
		Summary:     "Cancel a pending job",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		JobID int64 `path:"jobID"`
	}) (*MessageResponse, error) {
		err := a.queue.CancelJob(ctx, input.JobID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return nil, huma.NewError(404, "Job not found or not pending")
			}
			return nil, huma.NewError(500, "Failed to cancel job", err)
		}
		resp := &MessageResponse{}
		resp.Body.Message = "Job cancelled successfully"
		return resp, nil
	})
}

// Helper: convert queue.Job to JobResponse
func jobToResponse(job *queue.Job) *JobResponse {
	return &JobResponse{
		ID:           job.ID,
		ClusterURL:   job.ClusterURL,
		JobType:      job.JobType,
		Payload:      job.Payload,
		JobStatus:    job.Status,
		Priority:     job.Priority,
		CreatedAt:    job.CreatedAt,
		ClaimedAt:    job.ClaimedAt,
		ClaimedBy:    job.ClaimedBy,
		CompletedAt:  job.CompletedAt,
		ErrorMessage: job.ErrorMessage,
	}
}
