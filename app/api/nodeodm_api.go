package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"
	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"

	"github.com/hotosm/scaleodm/app/workflows"
)

// Response types
type EnqueueResponse struct {
	Body struct {
		JobID     string    `json:"job_id" doc:"Argo workflow name"`
		JobStatus string    `json:"job_status" doc:"Current job status"`
		CreatedAt time.Time `json:"created_at" doc:"Job creation timestamp"`
	}
}

type JobResponse struct {
	ID           string                 `json:"id"`
	JobStatus    string                 `json:"job_status"`
	CreatedAt    time.Time              `json:"created_at"`
	StartedAt    *time.Time             `json:"started_at,omitempty"`
	CompletedAt  *time.Time             `json:"completed_at,omitempty"`
	ErrorMessage *string                `json:"error_message,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

type JobListResponse struct {
	Body struct {
		Jobs  []JobResponse `json:"jobs"`
		Total int           `json:"total"`
	}
}

type LogsResponse struct {
	Body struct {
		Logs string `json:"logs"`
	}
}

type InfoResponse struct {
	Body struct {
		Name          string `json:"name"`
		Version       string `json:"version"`
		Engine        string `json:"engine"`
		EngineVersion string `json:"engine_version"`
	}
}

// registerNodeODMRoutes registers all NodeODM-compatible API routes
func (a *API) registerNodeODMRoutes() {
	// Create/Submit job - creates metadata and immediately submits Argo workflow
	huma.Register(a.api, huma.Operation{
		OperationID: "create-job",
		Method:      http.MethodPost,
		Path:        "/api/v1/jobs",
		Summary:     "Create a new ODM processing job",
		Description: "Records job metadata and immediately submits Argo workflow",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		Body struct {
			ODMProjectID string   `json:"odm_project_id" minLength:"1" doc:"Project identifier"`
			ReadS3Path   string   `json:"read_s3_path" minLength:"1" doc:"S3 path for input imagery"`
			WriteS3Path  string   `json:"write_s3_path" minLength:"1" doc:"S3 path for outputs"`
			ODMFlags     []string `json:"odm_flags" doc:"ODM command line flags"`
			S3Region     string   `json:"s3_region,omitempty" doc:"S3 region (default: us-east-1)"`
		}
	}) (*EnqueueResponse, error) {
		req := input.Body

		// Validate required fields
		if req.ODMProjectID == "" {
			return nil, huma.NewError(400, "odm_project_id is required")
		}
		if req.ReadS3Path == "" {
			return nil, huma.NewError(400, "read_s3_path is required")
		}
		if req.WriteS3Path == "" {
			return nil, huma.NewError(400, "write_s3_path is required")
		}

		// Set defaults
		if req.S3Region == "" {
			req.S3Region = "us-east-1"
		}
		if req.ODMFlags == nil {
			req.ODMFlags = []string{"--fast-orthophoto"}
		}

		// Create workflow config
		config := workflows.NewDefaultODMConfig(
			req.ODMProjectID,
			req.ReadS3Path,
			req.WriteS3Path,
			req.ODMFlags,
		)
		config.S3Region = req.S3Region

		// Submit workflow to Argo
		wf, err := a.workflowClient.CreateODMWorkflow(ctx, config)
		if err != nil {
			log.Printf("Failed to create workflow: %v", err)
			return nil, huma.NewError(500, "Failed to create workflow", err)
		}

		// Record metadata in database
		_, err = a.metadataStore.CreateJob(
			ctx,
			wf.Name,
			req.ODMProjectID,
			req.ReadS3Path,
			req.WriteS3Path,
			req.ODMFlags,
			req.S3Region,
		)
		if err != nil {
			log.Printf("Warning: Failed to record job metadata: %v", err)
			// Don't fail the request - workflow is already submitted
		}

		resp := &EnqueueResponse{}
		resp.Body.JobID = wf.Name
		resp.Body.JobStatus = string(wf.Status.Phase)
		if resp.Body.JobStatus == "" {
			resp.Body.JobStatus = "Pending"
		}
		resp.Body.CreatedAt = wf.CreationTimestamp.Time
		return resp, nil
	})

	// Get job status - checks both Argo and metadata store
	huma.Register(a.api, huma.Operation{
		OperationID: "get-job",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs/{jobID}",
		Summary:     "Get job status by workflow name",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		JobID string `path:"jobID" minLength:"1"`
	}) (*JobResponse, error) {
		// Get current workflow status from Argo
		wf, err := a.workflowClient.GetWorkflow(ctx, input.JobID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return nil, huma.NewError(404, "Job not found")
			}
			return nil, huma.NewError(500, "Failed to retrieve job", err)
		}

		// Update metadata store with current status
		status := string(wf.Status.Phase)
		if status == "" {
			status = "Pending"
		}
		
		var errorMsg *string
		if wf.Status.Phase == wfv1.WorkflowFailed || wf.Status.Phase == wfv1.WorkflowError {
			errorMsg = &wf.Status.Message
		}

		err = a.metadataStore.UpdateJobStatus(ctx, input.JobID, status, errorMsg)
		if err != nil {
			log.Printf("Warning: Failed to update job status in metadata: %v", err)
		}

		// Update workflow metadata
		metadata := extractWorkflowMetadata(wf)
		err = a.metadataStore.UpdateJobMetadata(ctx, input.JobID, metadata)
		if err != nil {
			log.Printf("Warning: Failed to update job metadata: %v", err)
		}

		return workflowToResponse(wf), nil
	})

	// List jobs - combines Argo workflows with metadata
	huma.Register(a.api, huma.Operation{
		OperationID: "list-jobs",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs",
		Summary:     "List all workflows",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		Status    string `query:"status" doc:"Filter by workflow phase (Running, Succeeded, Failed, etc.)"`
		ProjectID string `query:"project_id" doc:"Filter by ODM project ID"`
		Limit     int    `query:"limit" doc:"Max results to return" default:"100"`
	}) (*JobListResponse, error) {
		// Get workflows from Argo
		wfList, err := a.workflowClient.ListWorkflows(ctx, "")
		if err != nil {
			return nil, huma.NewError(500, "Failed to list jobs", err)
		}

		resp := &JobListResponse{}
		resp.Body.Jobs = make([]JobResponse, 0)

		for _, wf := range wfList.Items {
			// Filter by status if provided
			if input.Status != "" && string(wf.Status.Phase) != input.Status {
				continue
			}

			// Apply limit
			if input.Limit > 0 && len(resp.Body.Jobs) >= input.Limit {
				break
			}

			resp.Body.Jobs = append(resp.Body.Jobs, *workflowToResponse(&wf))
		}

		resp.Body.Total = len(resp.Body.Jobs)
		return resp, nil
	})

	// Cancel/Delete job - deletes from both Argo and metadata
	huma.Register(a.api, huma.Operation{
		OperationID: "cancel-job",
		Method:      http.MethodDelete,
		Path:        "/api/v1/jobs/{jobID}",
		Summary:     "Cancel and delete a workflow",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		JobID string `path:"jobID"`
	}) (*MessageResponse, error) {
		// Delete workflow from Argo
		err := a.workflowClient.DeleteWorkflow(ctx, input.JobID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return nil, huma.NewError(404, "Job not found")
			}
			return nil, huma.NewError(500, "Failed to cancel job", err)
		}

		// Delete metadata (optional - could keep for history)
		err = a.metadataStore.DeleteJob(ctx, input.JobID)
		if err != nil {
			log.Printf("Warning: Failed to delete job metadata: %v", err)
		}

		resp := &MessageResponse{}
		resp.Body.Message = "Job cancelled successfully"
		return resp, nil
	})

	// Get job logs
	huma.Register(a.api, huma.Operation{
		OperationID: "get-job-logs",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs/{jobID}/logs",
		Summary:     "Get logs for a workflow",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		JobID string `path:"jobID"`
	}) (*LogsResponse, error) {
		// Check if workflow exists
		_, err := a.workflowClient.GetWorkflow(ctx, input.JobID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return nil, huma.NewError(404, "Job not found")
			}
			return nil, huma.NewError(500, "Failed to retrieve job", err)
		}

		// Get logs
		var logBuilder strings.Builder
		err = a.workflowClient.GetWorkflowLogs(ctx, input.JobID, &logBuilder)
		if err != nil {
			return nil, huma.NewError(500, "Failed to retrieve logs", err)
		}

		resp := &LogsResponse{}
		resp.Body.Logs = logBuilder.String()
		return resp, nil
	})

	// Health/Info endpoint (NodeODM compatible)
	huma.Register(a.api, huma.Operation{
		OperationID: "get-info",
		Method:      http.MethodGet,
		Path:        "/api/v1/info",
		Summary:     "Get ScaleODM info",
		Tags:        []string{"Info"},
	}, func(ctx context.Context, input *struct{}) (*InfoResponse, error) {
		resp := &InfoResponse{}
		resp.Body.Name = "ScaleODM"
		resp.Body.Version = "1.0.0"
		resp.Body.Engine = "argo-workflows"
		resp.Body.EngineVersion = "3.x"
		return resp, nil
	})
}

// Helper: convert Argo workflow to JobResponse
func workflowToResponse(wf *wfv1.Workflow) *JobResponse {
	resp := &JobResponse{
		ID:        wf.Name,
		JobStatus: string(wf.Status.Phase),
		CreatedAt: wf.CreationTimestamp.Time,
	}

	if resp.JobStatus == "" {
		resp.JobStatus = "Pending"
	}

	// Set completion time if workflow is done
	if !wf.Status.FinishedAt.IsZero() {
		t := wf.Status.FinishedAt.Time
		resp.CompletedAt = &t
	}

	// Set start time
	if !wf.Status.StartedAt.IsZero() {
		t := wf.Status.StartedAt.Time
		resp.StartedAt = &t
	}

	// Set error message if failed
	if wf.Status.Phase == wfv1.WorkflowFailed || wf.Status.Phase == wfv1.WorkflowError {
		resp.ErrorMessage = &wf.Status.Message
	}

	resp.Metadata = extractWorkflowMetadata(wf)
	return resp
}

// Helper: extract metadata from workflow
func extractWorkflowMetadata(wf *wfv1.Workflow) map[string]interface{} {
	metadata := make(map[string]interface{})
	metadata["namespace"] = wf.Namespace
	metadata["uid"] = string(wf.UID)

	if wf.Status.Progress != "" {
		metadata["progress"] = wf.Status.Progress
	}

	// Add resource duration if available
	if wf.Status.ResourcesDuration != nil {
		metadata["cpu_seconds"] = wf.Status.ResourcesDuration.String()
	}

	return metadata
}
