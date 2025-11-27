package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/danielgtaylor/huma/v2"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/workflows"
)

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
	Status         int          `json:"status" doc:"Status code (10=QUEUED, 20=RUNNING, 30=FAILED, 40=COMPLETED, 50=CANCELED)"`
	Options        []TaskOption `json:"options" doc:"Processing options"`
	ImagesCount    int          `json:"imagesCount" doc:"Number of images"`
	Progress       int          `json:"progress" doc:"Progress from 0 to 100"`
	Output         []string     `json:"output,omitempty" doc:"Console output (if requested)"`
}

type TaskStatus struct {
	Code int `json:"code" doc:"Status code (10=QUEUED, 20=RUNNING, 30=FAILED, 40=COMPLETED, 50=CANCELED)"`
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

	// JSON array of output paths to include. Defaults to an empty array.
	Outputs string `json:"outputs,omitempty" form:"outputs" default:"[]" doc:"JSON array of output paths to include (default: [])"`

	// URL of zip file containing images (legacy). Prefer readS3Path.
	ZipURL string `json:"zipurl,omitempty" form:"zipurl" doc:"URL of zip file containing images (deprecated, use readS3Path)"`

	// S3 path to read imagery from. Required for new API usage (unless using legacy zipurl).
	ReadS3Path string `json:"readS3Path" form:"readS3Path" doc:"S3 path (s3://bucket/path) to read imagery from"`

	// S3 path to write final products to. If omitted, defaults to an 'output/' subdirectory
	// under the readS3Path.
	WriteS3Path string `json:"writeS3Path,omitempty" form:"writeS3Path" doc:"S3 path (s3://bucket/path) to write final products to (default: readS3Path + 'output/')"`

	// S3 credentials. Optional; if omitted, credentials will be resolved from environment
	// variables (e.g. SCALEODM_S3_ACCESS_KEY / SCALEODM_S3_SECRET_KEY).
	S3AccessKeyID     string `json:"s3AccessKeyID,omitempty" form:"s3AccessKeyID" doc:"S3 access key ID (optional, for authenticated buckets)"`
	S3SecretAccessKey string `json:"s3SecretAccessKey,omitempty" form:"s3SecretAccessKey" doc:"S3 secret access key (optional, for authenticated buckets)"`
	S3SessionToken    string `json:"s3SessionToken,omitempty" form:"s3SessionToken" doc:"S3 session token (optional, for STS credentials)"`

	// S3 region. Defaults to us-east-1 if omitted or empty.
	S3Region string `json:"s3Region,omitempty" form:"s3Region" default:"us-east-1" doc:"S3 region (default: us-east-1)"`

	// Optional override for creation timestamp. If omitted, the server uses the current
	// time when the job is created.
	DateCreated int64 `json:"dateCreated,omitempty" form:"dateCreated" doc:"Override creation timestamp (optional; defaults to current time when omitted)"`
}

// NewTaskNewRequest creates a new TaskNewRequest with default values
func NewTaskNewRequest() *TaskNewRequest {
	return &TaskNewRequest{
		SkipPostProcessing: false,
		Outputs:            "[]",
		Webhook:            "",
		ZipURL:             "",
		S3Region:           "us-east-1",
		S3AccessKeyID:      "",
		S3SecretAccessKey:  "",
		S3SessionToken:     "",
		DateCreated:        time.Now().Unix(),
	}
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
		resp.Body.Version = "0.1.0" // The ScaleODM version (normally the NodeODM version)
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

		// Log incoming task creation request (avoid logging secrets directly)
		log.Printf(
			"POST /task/new: name=%q readS3Path=%q writeS3Path=%q zipurl=%q skipPostProcessing=%t outputs=%q webhook_set=%t s3Region=%q s3AccessKeyID_set=%t s3SessionToken_set=%t dateCreated=%d token_provided=%t setUUID_set=%t",
			req.Name,
			req.ReadS3Path,
			req.WriteS3Path,
			req.ZipURL,
			req.SkipPostProcessing,
			req.Outputs,
			req.Webhook != "",
			req.S3Region,
			req.S3AccessKeyID != "",
			req.S3SessionToken != "",
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

		// Determine S3 region
		s3Region := req.S3Region
		if s3Region == "" {
			s3Region = "us-east-1"
		}

		// Handle S3 credentials - always required
		// 1. API parameters (if provided)
		// 2. Environment variables (fallback)
		var providedCreds *s3.S3Credentials
		if req.S3AccessKeyID != "" && req.S3SecretAccessKey != "" {
			providedCreds = &s3.S3Credentials{
				AccessKeyID:     req.S3AccessKeyID,
				SecretAccessKey: req.S3SecretAccessKey,
				SessionToken:    req.S3SessionToken,
			}
		}

		// Resolve credentials - always required
		// 1. API parameters (if provided)
		// 2. Environment variables (SCALEODM_S3_ACCESS_KEY, etc.)
		s3Creds, err := s3.ResolveCredentials(providedCreds, true, s3Region)
		if err != nil {
			log.Printf("Failed to resolve S3 credentials: %v", err)
			return nil, huma.NewError(400, "S3 credentials are required. Provide s3AccessKeyID and s3SecretAccessKey, or configure SCALEODM_S3_ACCESS_KEY and SCALEODM_S3_SECRET_KEY environment variables", err)
		}

		if s3Creds == nil {
			return nil, huma.NewError(400, "S3 credentials are required. Provide s3AccessKeyID and s3SecretAccessKey, or configure SCALEODM_S3_ACCESS_KEY and SCALEODM_S3_SECRET_KEY environment variables")
		}

		credSource := "environment variables"
		if providedCreds != nil {
			credSource = "API parameters"
		}
		log.Printf("Using S3 credentials for job (from %s)", credSource)

		wfConfig := workflows.NewDefaultODMConfig(
			projectID,
			readPath,
			writePath,
			odmFlags,
		)
		wfConfig.S3Region = s3Region
		wfConfig.S3Credentials = s3Creds

		// Submit workflow to Argo
		wf, err := a.workflowClient.CreateODMWorkflow(ctx, wfConfig)
		if err != nil {
			log.Printf("Failed to create workflow: %v", err)
			return nil, huma.NewError(500, "Failed to create workflow", err)
		}

		log.Printf(
			"POST /task/new: created workflow name=%q projectID=%q readPath=%q writePath=%q odmFlags=%v s3Region=%q credSource=%s",
			wf.Name,
			projectID,
			readPath,
			writePath,
			odmFlags,
			s3Region,
			credSource,
		)

		// Record metadata in database
		// Use local cluster URL for jobs created on this instance
		clusterURL := config.SCALEODM_CLUSTER_URL
		_, err = a.metadataStore.CreateJob(
			ctx,
			clusterURL,
			wf.Name,
			projectID,
			readPath,
			writePath,
			odmFlags,
			s3Region,
		)
		if err != nil {
			log.Printf("Warning: Failed to record job metadata: %v", err)
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
		info := TaskInfo{
			UUID:        job.WorkflowName,
			Name:        job.ODMProjectID,
			DateCreated: job.CreatedAt.Unix(),
			Status:      jobStatusToStatusCode(job.JobStatus),
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

		log.Printf("GET /task/%s/info: returning status=%d progress=%d", input.UUID, info.Status, info.Progress)

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
		err = a.workflowClient.GetWorkflowLogs(ctx, input.UUID, &logBuilder)
		if err != nil {
			// If workflow not found and we have write path, try S3
			if strings.Contains(err.Error(), "not found") && job.WriteS3Path != "" {
				s3Client := s3.GetS3Client()
				logContent, s3Err := s3.GetWorkflowLogsFromS3(ctx, s3Client, job.WriteS3Path)
				if s3Err == nil {
					logBuilder.WriteString(logContent)
				} else {
					// If S3 fetch also fails, return the original error
					log.Printf("GET /task/%s/output: failed to retrieve logs from workflow or S3: %v (s3Err=%v)", input.UUID, err, s3Err)
					return nil, huma.NewError(500, "Failed to retrieve logs from workflow or S3", err)
				}
			} else {
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
			if strings.Contains(err.Error(), "not found") {
				log.Printf("POST /task/cancel: task %q not found", input.Body.UUID)
				return nil, huma.NewError(404, "Task not found")
			}
			log.Printf("POST /task/cancel: failed to cancel task %q: %v", input.Body.UUID, err)
			return nil, huma.NewError(500, "Failed to cancel task", err)
		}

		// Update metadata to canceled status (map to failed for now, could add 'canceled' to schema later)
		if err := a.metadataStore.UpdateJobStatus(ctx, input.Body.UUID, "failed", nil); err != nil {
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
		if err != nil && !strings.Contains(err.Error(), "not found") {
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
		if err := a.workflowClient.DeleteWorkflow(ctx, input.Body.UUID); err != nil && !strings.Contains(err.Error(), "not found") {
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
		// Get cluster URL from original metadata (would need to add it to JobMetadata struct)
		// For now, use local cluster URL
		clusterURL := config.SCALEODM_CLUSTER_URL
		if err := a.metadataStore.DeleteJob(ctx, input.Body.UUID); err != nil {
			log.Printf("POST /task/restart: failed to delete old job metadata for %q: %v", input.Body.UUID, err)
		}
		if _, err := a.metadataStore.CreateJob(
			ctx,
			clusterURL,
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

	// GET /task/{uuid}/download/{asset} - Download task asset
	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-download-asset-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/download/{asset}",
		Summary:     "Download task output asset",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		UUID  string `path:"uuid" doc:"UUID of the task"`
		Asset string `path:"asset" doc:"Asset type (all.zip, orthophoto.tif, etc)"`
		Token string `query:"token" doc:"Authentication token (optional)"`
	}) (*ErrorResponse, error) {
		log.Printf("GET /task/%s/download/%s: token_provided=%t", input.UUID, input.Asset, input.Token != "")

		// This would need S3 integration to actually download files
		// For now, return the S3 path where the file should be
		metadata, err := a.metadataStore.GetJob(ctx, input.UUID)
		if err != nil {
			log.Printf("GET /task/%s/download/%s: failed to retrieve metadata: %v", input.UUID, input.Asset, err)
			return nil, huma.NewError(404, "Task not found")
		}

		// Return error with S3 path info
		s3Path := fmt.Sprintf("%s/%s", metadata.WriteS3Path, input.Asset)
		errResp := &ErrorResponse{}
		errResp.Body.Error = fmt.Sprintf("Direct download not implemented. File available at: %s", s3Path)

		log.Printf("GET /task/%s/download/%s: returning placeholder response with s3Path=%q", input.UUID, input.Asset, s3Path)

		return errResp, nil
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
	case "pending", "claimed":
		return 0
	case "running":
		return 50
	case "completed":
		return 100
	case "failed":
		return 0
	default:
		return 0
	}
}

// jobStatusToStatusCode maps internal job status strings stored in the metadata
// database to NodeODM-compatible status codes.
func jobStatusToStatusCode(status string) int {
	switch strings.ToLower(status) {
	case "pending", "claimed":
		return StatusCodeQueued
	case "running":
		return StatusCodeRunning
	case "completed":
		return StatusCodeCompleted
	case "failed":
		return StatusCodeFailed
	default:
		return StatusCodeQueued
	}
}
