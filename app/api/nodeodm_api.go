package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/danielgtaylor/huma/v2"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/workflows"
)

// isNotFound checks whether an error (possibly wrapped) represents a
// Kubernetes "not found" response.
func isNotFound(err error) bool {
	return k8serrors.IsNotFound(err)
}

// shellSafePattern matches strings that are safe to embed in shell scripts.
// Allows alphanumerics, hyphens, underscores, dots, forward slashes, colons,
// and the equals sign. This prevents shell injection via user-supplied values.
var shellSafePattern = regexp.MustCompile(`^[a-zA-Z0-9\-_./=:@]+$`)

// validateShellSafe checks that a string is safe to embed in a shell script.
func validateShellSafe(value, fieldName string) error {
	if !shellSafePattern.MatchString(value) {
		return fmt.Errorf("%s contains invalid characters: only alphanumerics, hyphens, underscores, dots, slashes, colons, equals, and @ are allowed", fieldName)
	}
	return nil
}

// NodeODM status codes
const (
	StatusCodeQueued    = 10
	StatusCodeRunning   = 20
	StatusCodeFailed    = 30
	StatusCodeCompleted = 40
	StatusCodeCanceled  = 50
)

// Response types matching NodeODM spec
type TaskNewResponse struct {
	Body struct {
		UUID string `json:"uuid" doc:"UUID of the newly created task"`
	}
}

type TaskListItem struct {
	UUID string `json:"uuid" doc:"UUID of the task"`
}

type TaskListResponse struct {
	Body []TaskListItem
}

type TaskInfoResponse struct {
	Body TaskInfo
}

type TaskInfo struct {
	UUID           string       `json:"uuid" doc:"UUID"`
	Name           string       `json:"name" doc:"Name"`
	DateCreated    int64        `json:"dateCreated" doc:"Timestamp"`
	ProcessingTime int64        `json:"processingTime" doc:"Milliseconds elapsed since task started"`
	Status         TaskStatus   `json:"status" doc:"Status object with code and optional error"`
	Options        []TaskOption `json:"options" doc:"Processing options"`
	ImagesCount    int          `json:"imagesCount" doc:"Number of images"`
	Progress       int          `json:"progress" doc:"Progress from 0 to 100"`
	Output         []string     `json:"output,omitempty" doc:"Console output (if requested)"`
}

type TaskStatus struct {
	Code         int    `json:"code" doc:"Status code (10=QUEUED, 20=RUNNING, 30=FAILED, 40=COMPLETED, 50=CANCELED)"`
	ErrorMessage string `json:"errorMessage,omitempty" doc:"Error message (present when status code is 30/FAILED)"`
}

type TaskOption struct {
	Name  string      `json:"name" doc:"Option name"`
	Value interface{} `json:"value" doc:"Option value"`
}

type InfoResponse struct {
	Body struct {
		Version          string `json:"version" doc:"Current API version"`
		TaskQueueCount   int    `json:"taskQueueCount" doc:"Number of tasks in queue"`
		MaxImages        *int   `json:"maxImages" doc:"Max images allowed (null for unlimited)"`
		MaxParallelTasks int    `json:"maxParallelTasks,omitempty" doc:"Max parallel tasks"`
		Engine           string `json:"engine" doc:"Processing engine identifier"`
		EngineVersion    string `json:"engineVersion" doc:"Engine version"`
		AvailableMemory  *int64 `json:"availableMemory,omitempty" doc:"Available RAM in bytes"`
		TotalMemory      *int64 `json:"totalMemory,omitempty" doc:"Total RAM in bytes"`
		CPUCores         int    `json:"cpuCores,omitempty" doc:"Number of CPU cores"`
	}
}

type OptionResponse struct {
	Name   string `json:"name" doc:"Option name"`
	Type   string `json:"type" doc:"Datatype (int, float, string, bool)"`
	Value  string `json:"value" doc:"Default value"`
	Domain string `json:"domain" doc:"Valid range of values"`
	Help   string `json:"help" doc:"Description"`
}

type TaskNewRequest struct {
	// Task name. If omitted or empty, a default project ID of \"odm-project\" is used.
	Name string `json:"name,omitempty" form:"name" doc:"Task name (optional; defaults to 'odm-project' if empty)"`

	// JSON array of processing options.
	// If omitted or empty, a default of --fast-orthophoto is applied.
	Options string `json:"options,omitempty" form:"options" doc:"JSON array of processing options (optional; defaults to fast-orthophoto when empty)"`

	// Webhook URL to notify when processing is complete.
	Webhook string `json:"webhook,omitempty" form:"webhook" doc:"Webhook URL (optional)"`

	// Skip point cloud tiles generation. Defaults to false.
	SkipPostProcessing bool `json:"skipPostProcessing,omitempty" form:"skipPostProcessing" default:"false" doc:"Skip point cloud tiles generation (default: false)"`

	// NOTE that NodeODM has an 'outputs' param to override default output directory all.zip creation
	// NOTE we do not implement this intentionally, to keep things simple
	// JSON array of output paths to include. Defaults to an empty array.
	// Outputs string `json:"outputs,omitempty" form:"outputs" default:"[]" doc:"JSON array of output paths to include (default: [])"`

	// URL of zip file containing images (legacy). Prefer readS3Path.
	ZipURL string `json:"zipurl,omitempty" form:"zipurl" doc:"URL of zip file containing images (deprecated, use readS3Path)"`

	// S3 path to read imagery from. Required for new API usage (unless using legacy zipurl).
	ReadS3Path string `json:"readS3Path" form:"readS3Path" doc:"S3 path (s3://bucket/path) to read imagery from"`
	// S3 path to write final products to. If omitted, defaults to an 'output/' subdirectory
	// under the readS3Path.
	WriteS3Path string `json:"writeS3Path,omitempty" form:"writeS3Path" doc:"S3 path (s3://bucket/path) to write final products to (default: readS3Path + 'output/')"`

	// Optional S3-compatible endpoint override (e.g. for MinIO or non-AWS providers).
	// If omitted, the server uses its configured default endpoint.
	S3Endpoint string `json:"s3Endpoint,omitempty" form:"s3Endpoint" doc:"Custom S3 endpoint (optional, for non-AWS S3 providers)"`
	// S3 region. Defaults to us-east-1 if omitted or empty.
	S3Region string `json:"s3Region,omitempty" form:"s3Region" default:"us-east-1" doc:"S3 region (default: us-east-1)"`

	// Optional override for creation timestamp. If omitted, the server uses the current
	// time when the job is created.
	DateCreated int64 `json:"dateCreated,omitempty" form:"dateCreated" doc:"Override creation timestamp (optional; defaults to current time when omitted)"`
}

type Response struct {
	Success bool   `json:"success" doc:"True if command succeeded"`
	Error   string `json:"error,omitempty" doc:"Error message if failed"`
}

type ErrorResponse struct {
	Body struct {
		Error string `json:"error" doc:"Error description"`
	}
}

// registerNodeODMRoutes registers NodeODM-compatible API routes
func (a *API) registerNodeODMRoutes() {

	// GET /info - Server information
	huma.Register(a.api, huma.Operation{
		OperationID: "info-get",
		Method:      http.MethodGet,
		Path:        "/info",
		Summary:     "Retrieves information about this node",
		Tags:        []string{"server"},
	}, func(ctx context.Context, input *struct {
		Token string `query:"token" doc:"Authentication token (optional)"`
	}) (*InfoResponse, error) {
		log.Printf("GET /info: token_provided=%t", input.Token != "")

		// Get workflow count from Argo
		wfList, err := a.workflowClient.ListWorkflows(ctx, "")
		queueCount := 0
		if err == nil {
			for _, wf := range wfList.Items {
				if wf.Status.Phase == wfv1.WorkflowPending || wf.Status.Phase == wfv1.WorkflowRunning {
					queueCount++
				}
			}
		}

		resp := &InfoResponse{}
		resp.Body.Version = "0.2.0" // The ScaleODM version (normally the NodeODM version)
		resp.Body.TaskQueueCount = queueCount
		resp.Body.MaxImages = nil // Unlimited
		resp.Body.Engine = "odm"
		resp.Body.EngineVersion = config.SCALEODM_ODM_IMAGE

		return resp, nil
	})

	// GET /options - Available ODM options
	huma.Register(a.api, huma.Operation{
		OperationID: "options-get",
		Method:      http.MethodGet,
		Path:        "/options",
		Summary:     "Retrieves command line options for task processing",
		Tags:        []string{"server"},
	}, func(ctx context.Context, input *struct {
		Token string `query:"token" doc:"Authentication token (optional)"`
	}) (*struct{ Body []OptionResponse }, error) {
		log.Printf("GET /options: token_provided=%t", input.Token != "")

		// Return common ODM options
		options := []OptionResponse{
			{
				Name:   "fast-orthophoto",
				Type:   "bool",
				Value:  "false",
				Domain: "bool",
				Help:   "Skips dense reconstruction and 3D model generation",
			},
			{
				Name:   "dsm",
				Type:   "bool",
				Value:  "false",
				Domain: "bool",
				Help:   "Use this tag to build a Digital Surface Model",
			},
			{
				Name:   "dtm",
				Type:   "bool",
				Value:  "false",
				Domain: "bool",
				Help:   "Use this tag to build a Digital Terrain Model",
			},
			{
				Name:   "orthophoto-resolution",
				Type:   "float",
				Value:  "5",
				Domain: "float > 0",
				Help:   "Orthophoto resolution in cm/pixel",
			},
			{
				Name:   "dem-resolution",
				Type:   "float",
				Value:  "5",
				Domain: "float > 0",
				Help:   "DEM resolution in cm/pixel",
			},
		}

		return &struct{ Body []OptionResponse }{Body: options}, nil
	})

	// POST /task/new - Create new task
	huma.Register(a.api, huma.Operation{
		OperationID: "task-new-post",
		Method:      http.MethodPost,
		Path:        "/task/new",
		Summary:     "Creates a new task",
		Description: "Creates a new task and places it at the end of the processing queue",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		Token   string `query:"token" doc:"Authentication token (optional)"`
		SetUUID string `header:"set-uuid" doc:"Optional UUID to use for this task"`
		Body    TaskNewRequest
	}) (*TaskNewResponse, error) {
		req := input.Body

		// Log incoming task creation request
		log.Printf(
			"POST /task/new: name=%q readS3Path=%q writeS3Path=%q zipurl=%q skipPostProcessing=%t webhook_set=%t s3Region=%q s3Endpoint=%q dateCreated=%d token_provided=%t setUUID_set=%t",
			req.Name,
			req.ReadS3Path,
			req.WriteS3Path,
			req.ZipURL,
			req.SkipPostProcessing,
			req.Webhook != "",
			req.S3Region,
			req.S3Endpoint,
			req.DateCreated,
			input.Token != "",
			input.SetUUID != "",
		)

		// Parse options if provided
		var options []TaskOption
		var odmFlags []string
		if req.Options != "" {
			if err := json.Unmarshal([]byte(req.Options), &options); err != nil {
				log.Printf("POST /task/new: invalid options JSON: %v", err)
				return nil, huma.NewError(400, "Invalid options JSON", err)
			}

			// Convert options to ODM flags
			for _, opt := range options {
				flag := fmt.Sprintf("--%s", opt.Name)
				if opt.Value != nil && opt.Value != false {
					if boolVal, ok := opt.Value.(bool); ok && boolVal {
						odmFlags = append(odmFlags, flag)
					} else {
						odmFlags = append(odmFlags, flag, fmt.Sprintf("%v", opt.Value))
					}
				}
			}
		}

		if len(odmFlags) == 0 {
			odmFlags = []string{"--fast-orthophoto"}
		}

		// Determine read and write paths
		var readPath, writePath string

		// New API: prefer readS3Path/writeS3Path
		if req.ReadS3Path != "" {
			readPath = strings.TrimSuffix(req.ReadS3Path, "/") + "/"
			if req.WriteS3Path != "" {
				writePath = strings.TrimSuffix(req.WriteS3Path, "/") + "/"
			} else {
				// Default: write to output subdirectory in read path
				writePath = strings.TrimSuffix(req.ReadS3Path, "/") + "/output/"
			}
		} else if req.ZipURL != "" {
			// Legacy support: zipurl parameter
			isS3Prefix := strings.HasPrefix(req.ZipURL, "s3://")
			isHTTPZip := strings.HasPrefix(req.ZipURL, "http://") || strings.HasPrefix(req.ZipURL, "https://")

			if !isS3Prefix && !isHTTPZip {
				log.Printf("POST /task/new: invalid zipurl=%q (must be s3:// or http(s) zip URL)", req.ZipURL)
				return nil, huma.NewError(400, "zipurl must be an s3://... prefix or a http(s) zip URL")
			}

			if isS3Prefix {
				readPath = strings.TrimSuffix(req.ZipURL, "/") + "/"
				writePath = strings.TrimSuffix(req.ZipURL, "/") + "-output/"
			} else {
				// HTTP zip - not supported for S3 read/write workflow
				log.Printf("POST /task/new: HTTP zip URLs not supported zipurl=%q", req.ZipURL)
				return nil, huma.NewError(400, "HTTP zip URLs not supported. Use readS3Path for S3-based processing")
			}
		} else {
			log.Printf("POST /task/new: missing required readS3Path or zipurl")
			return nil, huma.NewError(400, "readS3Path is required (or zipurl for legacy support)")
		}

		// Validate S3 paths
		if !strings.HasPrefix(readPath, "s3://") {
			log.Printf("POST /task/new: readPath must be s3:// path, got %q", readPath)
			return nil, huma.NewError(400, "readS3Path must be an s3:// path")
		}
		if !strings.HasPrefix(writePath, "s3://") {
			log.Printf("POST /task/new: writePath must be s3:// path, got %q", writePath)
			return nil, huma.NewError(400, "writeS3Path must be an s3:// path")
		}

		// Create workflow config
		projectID := req.Name
		if projectID == "" {
			projectID = "odm-project"
		}

		// Validate all values that will be embedded in shell scripts
		if err := validateShellSafe(projectID, "name"); err != nil {
			return nil, huma.NewError(400, err.Error())
		}
		for _, flag := range odmFlags {
			if err := validateShellSafe(flag, "options flag"); err != nil {
				return nil, huma.NewError(400, err.Error())
			}
		}
		if err := validateShellSafe(readPath, "readS3Path"); err != nil {
			return nil, huma.NewError(400, err.Error())
		}
		if err := validateShellSafe(writePath, "writeS3Path"); err != nil {
			return nil, huma.NewError(400, err.Error())
		}

		// Determine S3 region & optional endpoint
		s3Region := req.S3Region
		if s3Region == "" {
			s3Region = "us-east-1"
		}
		s3Endpoint := req.S3Endpoint

		// S3 credentials are configured at the server level and injected into
		// workflow pods via Kubernetes Secret references (secretKeyRef).
		// No per-request credential handling needed.

		wfConfig := workflows.NewDefaultODMConfig(
			projectID,
			readPath,
			writePath,
			odmFlags,
		)
		wfConfig.S3Region = s3Region
		wfConfig.S3Endpoint = s3Endpoint

		// Submit workflow to Argo
		wf, err := a.workflowClient.CreateODMWorkflow(ctx, wfConfig)
		if err != nil {
			log.Printf("Failed to create workflow: %v", err)
			return nil, huma.NewError(500, "Failed to create workflow", err)
		}

		log.Printf(
			"POST /task/new: created workflow name=%q projectID=%q readPath=%q writePath=%q odmFlags=%v s3Region=%q",
			wf.Name,
			projectID,
			readPath,
			writePath,
			odmFlags,
			s3Region,
		)

		// Record metadata in database. If this fails, the workflow exists in
		// Argo but won't be visible via the API - treat as a hard error so the
		// caller knows to retry rather than losing track of the workflow.
		_, err = a.metadataStore.CreateJob(
			ctx,
			wf.Name,
			projectID,
			readPath,
			writePath,
			odmFlags,
			s3Region,
		)
		if err != nil {
			log.Printf("ERROR: Failed to record job metadata for workflow %q: %v", wf.Name, err)
			return nil, huma.NewError(500, "Workflow created but failed to record metadata - retry the request", err)
		}

		resp := &TaskNewResponse{}
		resp.Body.UUID = wf.Name
		return resp, nil
	})

	// GET /task/list - List all tasks
	huma.Register(a.api, huma.Operation{
		OperationID: "task-list-get",
		Method:      http.MethodGet,
		Path:        "/task/list",
		Summary:     "Gets the list of tasks",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		Token string `query:"token" doc:"Authentication token (optional)"`
	}) (*TaskListResponse, error) {
		log.Printf("GET /task/list: token_provided=%t", input.Token != "")

		wfList, err := a.workflowClient.ListWorkflows(ctx, "")
		if err != nil {
			log.Printf("GET /task/list: failed to list workflows: %v", err)
			return nil, huma.NewError(500, "Failed to list tasks", err)
		}

		resp := &TaskListResponse{}
		resp.Body = make([]TaskListItem, 0, len(wfList.Items))

		for _, wf := range wfList.Items {
			resp.Body = append(resp.Body, TaskListItem{UUID: wf.Name})
		}

		log.Printf("GET /task/list: returned %d tasks", len(resp.Body))

		return resp, nil
	})

	// GET /task/{uuid}/info - Get task information
	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-info-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/info",
		Summary:     "Gets information about a task",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		UUID       string `path:"uuid" doc:"UUID of the task"`
		Token      string `query:"token" doc:"Authentication token (optional)"`
		WithOutput int    `query:"with_output" default:"0" doc:"Line number to start console output from"`
	}) (*TaskInfoResponse, error) {
		log.Printf("GET /task/%s/info: token_provided=%t with_output=%d", input.UUID, input.Token != "", input.WithOutput)

		// Look up job metadata first. If we don't have metadata, the task
		// truly doesn't exist for this ScaleODM instance.
		job, err := a.metadataStore.GetJob(ctx, input.UUID)
		if err != nil {
			log.Printf("GET /task/%s/info: failed to retrieve task metadata: %v", input.UUID, err)
			return nil, huma.NewError(500, "Failed to retrieve task metadata", err)
		}

		if job == nil {
			log.Printf("GET /task/%s/info: task not found in metadata store", input.UUID)
			return nil, huma.NewError(404, "Task not found")
		}

		// Build task info response purely from metadata. This allows the
		// endpoint to continue working even if the backing Argo workflow
		// has been garbage-collected or is otherwise unavailable.
		status := TaskStatus{
			Code: jobStatusToStatusCode(job.JobStatus),
		}
		if job.ErrorMessage != nil {
			status.ErrorMessage = *job.ErrorMessage
		}

		info := TaskInfo{
			UUID:        job.WorkflowName,
			Name:        job.ODMProjectID,
			DateCreated: job.CreatedAt.Unix(),
			Status:      status,
			ImagesCount: 0, // Not yet tracked
			Progress:    jobStatusToProgress(job.JobStatus),
		}

		// Calculate processing time from metadata timestamps, if present.
		if job.StartedAt != nil {
			endTime := time.Now()
			if job.CompletedAt != nil {
				endTime = *job.CompletedAt
			}
			info.ProcessingTime = endTime.Sub(*job.StartedAt).Milliseconds()
		}

		// Add options from metadata
		if len(job.ODMFlags) > 0 {
			var flags []string
			if err := json.Unmarshal(job.ODMFlags, &flags); err == nil {
				info.Options = make([]TaskOption, 0, len(flags))
				for _, flag := range flags {
					info.Options = append(info.Options, TaskOption{
						Name:  strings.TrimPrefix(flag, "--"),
						Value: true,
					})
				}
			} else {
				log.Printf("GET /task/%s/info: failed to unmarshal stored ODM flags: %v", input.UUID, err)
			}
		}

		// Get console output if requested
		if input.WithOutput > 0 {
			var logBuilder strings.Builder
			// Use S3 path if available for fallback
			if job.WriteS3Path != "" {
				s3Client := s3.GetS3Client()
				if err := a.workflowClient.GetWorkflowLogsWithS3Path(ctx, input.UUID, job.WriteS3Path, s3Client, &logBuilder); err == nil {
					lines := strings.Split(logBuilder.String(), "\n")
					if input.WithOutput < len(lines) {
						info.Output = lines[input.WithOutput:]
					}
				} else {
					log.Printf("GET /task/%s/info: failed to get workflow logs with S3 path: %v", input.UUID, err)
				}
			} else {
				// Fallback to regular log retrieval
				if err := a.workflowClient.GetWorkflowLogs(ctx, input.UUID, &logBuilder); err == nil {
					lines := strings.Split(logBuilder.String(), "\n")
					if input.WithOutput < len(lines) {
						info.Output = lines[input.WithOutput:]
					}
				} else {
					log.Printf("GET /task/%s/info: failed to get workflow logs: %v", input.UUID, err)
				}
			}
		}

		log.Printf("GET /task/%s/info: returning status=%d progress=%d", input.UUID, info.Status.Code, info.Progress)

		return &TaskInfoResponse{Body: info}, nil
	})

	// GET /task/{uuid}/output - Get task console output
	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-output-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/output",
		Summary:     "Retrieves console output",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		UUID  string `path:"uuid" doc:"UUID of the task"`
		Token string `query:"token" doc:"Authentication token (optional)"`
		Line  int    `query:"line" default:"0" doc:"Line number to start from"`
	}) (*struct{ Body string }, error) {
		log.Printf("GET /task/%s/output: token_provided=%t line=%d", input.UUID, input.Token != "", input.Line)

		// Get job metadata to retrieve write path for S3 fallback
		job, err := a.metadataStore.GetJob(ctx, input.UUID)
		if err != nil {
			log.Printf("GET /task/%s/output: failed to retrieve job metadata: %v", input.UUID, err)
			return nil, huma.NewError(500, "Failed to retrieve job metadata", err)
		}
		if job == nil {
			log.Printf("GET /task/%s/output: task not found", input.UUID)
			return nil, huma.NewError(404, "Task not found")
		}

		// Get logs - try workflow first, fallback to S3 if workflow is deleted
		var logBuilder strings.Builder
		if job.WriteS3Path != "" {
			// Use S3 path for fallback
			s3Client := s3.GetS3Client()
			err = a.workflowClient.GetWorkflowLogsWithS3Path(ctx, input.UUID, job.WriteS3Path, s3Client, &logBuilder)
			if err != nil {
				log.Printf("GET /task/%s/output: failed to retrieve logs from workflow or S3: %v", input.UUID, err)
				return nil, huma.NewError(500, "Failed to retrieve logs", err)
			}
		} else {
			// No S3 path available, try workflow only
			err = a.workflowClient.GetWorkflowLogs(ctx, input.UUID, &logBuilder)
			if err != nil {
				log.Printf("GET /task/%s/output: failed to retrieve workflow logs: %v", input.UUID, err)
				return nil, huma.NewError(500, "Failed to retrieve logs", err)
			}
		}

		output := logBuilder.String()
		if input.Line > 0 {
			lines := strings.Split(output, "\n")
			if input.Line < len(lines) {
				output = strings.Join(lines[input.Line:], "\n")
			}
		}

		log.Printf("GET /task/%s/output: returned %d bytes of output", input.UUID, len(output))

		return &struct{ Body string }{Body: output}, nil
	})

	// POST /task/cancel - Cancel a task
	huma.Register(a.api, huma.Operation{
		OperationID: "task-cancel-post",
		Method:      http.MethodPost,
		Path:        "/task/cancel",
		Summary:     "Cancels a task",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		Token string `query:"token" doc:"Authentication token (optional)"`
		Body  struct {
			UUID string `json:"uuid" doc:"UUID of the task"`
		}
	}) (*Response, error) {
		log.Printf("POST /task/cancel: uuid=%q token_provided=%t", input.Body.UUID, input.Token != "")

		err := a.workflowClient.DeleteWorkflow(ctx, input.Body.UUID)
		if err != nil {
			if isNotFound(err) {
				log.Printf("POST /task/cancel: task %q not found", input.Body.UUID)
				return nil, huma.NewError(404, "Task not found")
			}
			log.Printf("POST /task/cancel: failed to cancel task %q: %v", input.Body.UUID, err)
			return nil, huma.NewError(500, "Failed to cancel task", err)
		}

		// Update metadata to canceled status
		if err := a.metadataStore.UpdateJobStatus(ctx, input.Body.UUID, "canceled", nil); err != nil {
			log.Printf("POST /task/cancel: failed to update job status for %q: %v", input.Body.UUID, err)
		}

		log.Printf("POST /task/cancel: task %q canceled", input.Body.UUID)

		return &Response{Success: true}, nil
	})

	// POST /task/remove - Remove a task
	huma.Register(a.api, huma.Operation{
		OperationID: "task-remove-post",
		Method:      http.MethodPost,
		Path:        "/task/remove",
		Summary:     "Removes a task and deletes all assets",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		Token string `query:"token" doc:"Authentication token (optional)"`
		Body  struct {
			UUID string `json:"uuid" doc:"UUID of the task"`
		}
	}) (*Response, error) {
		log.Printf("POST /task/remove: uuid=%q token_provided=%t", input.Body.UUID, input.Token != "")

		// Delete from Argo
		err := a.workflowClient.DeleteWorkflow(ctx, input.Body.UUID)
		if err != nil && !isNotFound(err) {
			log.Printf("POST /task/remove: failed to delete workflow for %q: %v", input.Body.UUID, err)
			return nil, huma.NewError(500, "Failed to remove task", err)
		}

		// Delete metadata
		err = a.metadataStore.DeleteJob(ctx, input.Body.UUID)
		if err != nil {
			log.Printf("Warning: Failed to delete metadata: %v", err)
		}

		log.Printf("POST /task/remove: task %q removed (workflow+metadata)", input.Body.UUID)

		return &Response{Success: true}, nil
	})

	// POST /task/restart - Restart a task
	huma.Register(a.api, huma.Operation{
		OperationID: "task-restart-post",
		Method:      http.MethodPost,
		Path:        "/task/restart",
		Summary:     "Restarts a task",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		Token string `query:"token" doc:"Authentication token (optional)"`
		Body  struct {
			UUID    string `json:"uuid" doc:"UUID of the task"`
			Options string `json:"options,omitempty" doc:"New options (optional)"`
		}
	}) (*Response, error) {
		log.Printf("POST /task/restart: uuid=%q token_provided=%t options_set=%t", input.Body.UUID, input.Token != "", input.Body.Options != "")

		// Get existing task metadata
		metadata, err := a.metadataStore.GetJob(ctx, input.Body.UUID)
		if err != nil {
			log.Printf("POST /task/restart: failed to retrieve metadata for %q: %v", input.Body.UUID, err)
			return nil, huma.NewError(404, "Task not found")
		}

		// Parse new options if provided
		var odmFlags []string
		if input.Body.Options != "" {
			var options []TaskOption
			if err := json.Unmarshal([]byte(input.Body.Options), &options); err == nil {
				for _, opt := range options {
					flag := fmt.Sprintf("--%s", opt.Name)
					odmFlags = append(odmFlags, flag)
					if opt.Value != nil && opt.Value != true {
						odmFlags = append(odmFlags, fmt.Sprintf("%v", opt.Value))
					}
				}
			} else {
				log.Printf("POST /task/restart: invalid options JSON for %q: %v", input.Body.UUID, err)
			}
		} else {
			// Use original flags
			json.Unmarshal(metadata.ODMFlags, &odmFlags)
		}

		// Delete old workflow
		if err := a.workflowClient.DeleteWorkflow(ctx, input.Body.UUID); err != nil && !isNotFound(err) {
			log.Printf("POST /task/restart: failed to delete old workflow for %q: %v", input.Body.UUID, err)
		}

		// Create new workflow with same UUID prefix
		wfConfig := workflows.NewDefaultODMConfig(
			metadata.ODMProjectID,
			metadata.ReadS3Path,
			metadata.WriteS3Path,
			odmFlags,
		)

		wf, err := a.workflowClient.CreateODMWorkflow(ctx, wfConfig)
		if err != nil {
			log.Printf("POST /task/restart: failed to create new workflow for %q: %v", input.Body.UUID, err)
			return nil, huma.NewError(500, "Failed to restart task", err)
		}

		// Update metadata with new workflow name
		if err := a.metadataStore.DeleteJob(ctx, input.Body.UUID); err != nil {
			log.Printf("POST /task/restart: failed to delete old job metadata for %q: %v", input.Body.UUID, err)
		}
		if _, err := a.metadataStore.CreateJob(
			ctx,
			wf.Name,
			metadata.ODMProjectID,
			metadata.ReadS3Path,
			metadata.WriteS3Path,
			odmFlags,
			metadata.S3Region,
		); err != nil {
			log.Printf("POST /task/restart: failed to create new job metadata for %q (new workflow %q): %v", input.Body.UUID, wf.Name, err)
		}

		log.Printf("POST /task/restart: task %q restarted as workflow %q", input.Body.UUID, wf.Name)

		return &Response{Success: true}, nil
	})

	// GET /task/{uuid}/download/{asset} - Download task asset (redirects to pre-signed URL)
	// Registered as a raw HTTP handler on the mux for reliable redirect support.
	// Huma handlers return structured responses which can't express HTTP redirects,
	// so we handle this endpoint outside Huma.
	a.downloadHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uuid := r.PathValue("uuid")
		asset := r.PathValue("asset")
		log.Printf("GET /task/%s/download/%s", uuid, asset)

		metadata, err := a.metadataStore.GetJob(r.Context(), uuid)
		if err != nil {
			log.Printf("GET /task/%s/download/%s: failed to retrieve metadata: %v", uuid, asset, err)
			http.Error(w, `{"error":"Failed to retrieve task metadata"}`, http.StatusInternalServerError)
			return
		}
		if metadata == nil {
			log.Printf("GET /task/%s/download/%s: task not found", uuid, asset)
			http.Error(w, `{"error":"Task not found"}`, http.StatusNotFound)
			return
		}
		if metadata.WriteS3Path == "" {
			log.Printf("GET /task/%s/download/%s: write S3 path not available", uuid, asset)
			http.Error(w, `{"error":"Write S3 path not available for this task"}`, http.StatusBadRequest)
			return
		}

		s3Client := s3.GetS3Client()
		presignedURL, err := s3.GeneratePresignedURL(r.Context(), s3Client, metadata.WriteS3Path, asset, 1*time.Hour)
		if err != nil {
			log.Printf("GET /task/%s/download/%s: failed to generate pre-signed URL: %v", uuid, asset, err)
			http.Error(w, fmt.Sprintf(`{"error":"File not found: %s"}`, asset), http.StatusNotFound)
			return
		}

		log.Printf("GET /task/%s/download/%s: redirecting to pre-signed URL (expires in 1 hour)", uuid, asset)
		http.Redirect(w, r, presignedURL, http.StatusFound)
	})
}

// Helper functions

func workflowToStatusCode(phase wfv1.WorkflowPhase) int {
	switch phase {
	case wfv1.WorkflowPending:
		return StatusCodeQueued
	case wfv1.WorkflowRunning:
		return StatusCodeRunning
	case wfv1.WorkflowSucceeded:
		return StatusCodeCompleted
	case wfv1.WorkflowFailed, wfv1.WorkflowError:
		return StatusCodeFailed
	default:
		return StatusCodeQueued
	}
}

func workflowToProgress(phase wfv1.WorkflowPhase) int {
	switch phase {
	case wfv1.WorkflowPending:
		return 0
	case wfv1.WorkflowRunning:
		return 50
	case wfv1.WorkflowSucceeded:
		return 100
	case wfv1.WorkflowFailed, wfv1.WorkflowError:
		return 0
	default:
		return 0
	}
}

// jobStatusToProgress provides a coarse progress estimate based solely on the
// stored job status.
func jobStatusToProgress(status string) int {
	switch strings.ToLower(status) {
	case "queued", "claimed": // 'claimed' is internal state, same progress as queued
		return 0
	case "running":
		return 50
	case "completed":
		return 100
	case "failed", "canceled":
		return 0
	default:
		return 0
	}
}

// jobStatusToStatusCode maps internal job status strings stored in the metadata
// database to NodeODM-compatible status codes.
// Database statuses align with NodeODM labels: 'queued', 'running', 'completed', 'failed', 'canceled'
// Note: 'claimed' is an internal state for job queue management that maps to QUEUED (10)
func jobStatusToStatusCode(status string) int {
	switch strings.ToLower(status) {
	case "queued", "claimed": // 'claimed' is internal state, maps to QUEUED
		return StatusCodeQueued
	case "running":
		return StatusCodeRunning
	case "completed":
		return StatusCodeCompleted
	case "failed":
		return StatusCodeFailed
	case "canceled":
		return StatusCodeCanceled
	default:
		return StatusCodeQueued
	}
}
