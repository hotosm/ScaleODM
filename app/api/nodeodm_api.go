package api

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel/attribute"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/observability"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/version"
	"github.com/hotosm/scaleodm/app/workflows"
)

// registerNodeODMRoutes registers NodeODM-compatible API routes
func (a *API) registerNodeODMRoutes() {
	huma.Register(a.api, huma.Operation{
		OperationID: "info-get",
		Method:      http.MethodGet,
		Path:        "/info",
		Summary:     "Retrieves information about this node",
		Tags:        []string{"server"},
	}, a.getInfo)

	huma.Register(a.api, huma.Operation{
		OperationID: "options-get",
		Method:      http.MethodGet,
		Path:        "/options",
		Summary:     "Retrieves command line options for task processing",
		Tags:        []string{"server"},
	}, a.getOptions)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-new-post",
		Method:      http.MethodPost,
		Path:        "/task/new",
		Summary:     "Creates a new task",
		Description: "Creates a new task and places it at the end of the processing queue",
		Tags:        []string{"task"},
	}, a.createTask)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-list-get",
		Method:      http.MethodGet,
		Path:        "/task/list",
		Summary:     "Gets the list of tasks",
		Tags:        []string{"task"},
	}, a.listTasks)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-info-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/info",
		Summary:     "Gets information about a task",
		Tags:        []string{"task"},
	}, a.getTaskInfo)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-output-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/output",
		Summary:     "Retrieves console output",
		Tags:        []string{"task"},
	}, a.getTaskOutput)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-assets-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/assets",
		Summary:     "Lists primary and optional additional output assets",
		Tags:        []string{"task"},
	}, a.getTaskAssets)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-cancel-post",
		Method:      http.MethodPost,
		Path:        "/task/cancel",
		Summary:     "Cancels a task",
		Tags:        []string{"task"},
	}, a.cancelTask)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-remove-post",
		Method:      http.MethodPost,
		Path:        "/task/remove",
		Summary:     "Removes a task and deletes all assets",
		Tags:        []string{"task"},
	}, a.removeTask)

	huma.Register(a.api, huma.Operation{
		OperationID: "task-restart-post",
		Method:      http.MethodPost,
		Path:        "/task/restart",
		Summary:     "Restarts a task",
		Tags:        []string{"task"},
	}, a.restartTask)
}

type tokenInput struct {
	Token string `query:"token" doc:"Authentication token (optional)"`
}

func (a *API) getInfo(ctx context.Context, input *tokenInput) (*InfoResponse, error) {
	log.Printf("GET /info: token_provided=%t", input.Token != "")

	queueCount := 0
	if wfList, err := a.workflowClient.ListWorkflows(ctx, ""); err == nil {
		for _, wf := range wfList.Items {
			if wf.Status.Phase == wfv1.WorkflowPending || wf.Status.Phase == wfv1.WorkflowRunning {
				queueCount++
			}
		}
	}

	resp := &InfoResponse{}
	resp.Body.Version = version.Version // The ScaleODM version (normally the NodeODM version)
	resp.Body.TaskQueueCount = queueCount
	resp.Body.MaxImages = nil // Unlimited
	resp.Body.Engine = "odm"
	resp.Body.EngineVersion = config.SCALEODM_ODM_IMAGE
	return resp, nil
}

func (a *API) getOptions(ctx context.Context, input *tokenInput) (*struct{ Body []OptionResponse }, error) {
	log.Printf("GET /options: token_provided=%t", input.Token != "")

	options := []OptionResponse{
		{Name: "fast-orthophoto", Type: "bool", Value: "false", Domain: "bool", Help: "Skips dense reconstruction and 3D model generation"},
		{Name: "dsm", Type: "bool", Value: "false", Domain: "bool", Help: "Use this tag to build a Digital Surface Model"},
		{Name: "dtm", Type: "bool", Value: "false", Domain: "bool", Help: "Use this tag to build a Digital Terrain Model"},
		{Name: "orthophoto-resolution", Type: "float", Value: "5", Domain: "float > 0", Help: "Orthophoto resolution in cm/pixel"},
		{Name: "dem-resolution", Type: "float", Value: "5", Domain: "float > 0", Help: "DEM resolution in cm/pixel"},
		{
			Name:   workflows.BoundaryOptionName,
			Type:   "string",
			Domain: "GeoJSON Polygon, s3:// URL, or path",
			Help:   "Clips the reconstruction to one Polygon AOI. Accepts inline GeoJSON, an s3:// URL or a path",
		},
		{
			Name:   workflows.CamerasOptionName,
			Type:   "string",
			Domain: "cameras.json, s3:// URL, or path",
			Help:   "Supplies camera intrinsics instead of solving them. Accepts inline JSON, an s3:// URL or a path",
		},
	}
	return &struct{ Body []OptionResponse }{Body: options}, nil
}

type taskNewInput struct {
	Token   string `query:"token" doc:"Authentication token (optional)"`
	SetUUID string `header:"set-uuid" doc:"Optional UUID to use for this task"`
	Body    TaskNewRequest
}

func (a *API) createTask(ctx context.Context, input *taskNewInput) (*TaskNewResponse, error) {
	start := time.Now()
	metricResult := "failure"
	metricReason := "unknown"
	ctx, span := observability.Tracer().Start(ctx, "task.new")
	defer func() {
		span.SetAttributes(
			attribute.String("task.new.result", metricResult),
			attribute.String("task.new.reason", metricReason),
		)
		span.End()
		observability.RecordTaskNew(metricResult, metricReason, time.Since(start))
	}()
	// No log line needed: withTaskNewErrorLogging logs every failed response body.
	fail := func(rej *rejection) (*TaskNewResponse, error) {
		metricReason = rej.reason
		return nil, rej.humaError()
	}

	req := input.Body
	log.Printf(
		"POST /task/new: name=%q readS3Path=%q writeS3Path=%q zipurl=%q skipPostProcessing=%t webhook_set=%t s3Region=%q s3Endpoint=%q dateCreated=%d processingMode=%q token_provided=%t setUUID_set=%t",
		req.Name, req.ReadS3Path, req.WriteS3Path, req.ZipURL, req.SkipPostProcessing, req.Webhook != "",
		req.S3Region, req.S3Endpoint, req.DateCreated, req.ProcessingMode, input.Token != "", input.SetUUID != "",
	)

	spec, rej := parseTaskNewRequest(req)
	if rej != nil {
		return fail(rej)
	}
	log.Printf("POST /task/new: endpoint selection endpoint=%q region=%q allowlist_enforced=%t", spec.s3Endpoint, spec.s3Region, config.SCALEODM_ENFORCE_S3_ENDPOINT_ALLOWLIST)

	// Count images before workflow submission so resources can be sized.
	taskClient, err := s3.GetS3ClientForEndpoint(spec.workflowS3Endpoint())
	if err != nil {
		return fail(reject("s3_client_init_failed", 500, "Failed to initialize S3 client", err))
	}
	imageCount, imageTotalBytes, err := s3.CountImageStatsInS3Path(ctx, taskClient, spec.readPath, spec.excludePatterns())
	if err != nil {
		return fail(reject("image_count_failed", 400, "Unable to read imagery from readS3Path", err))
	}
	if option, err := preflightWorkspaceFiles(ctx, taskClient, spec.files); err != nil {
		return fail(reject(option+"_unreadable", 400, err.Error()))
	}

	wf, err := a.workflowClient.CreateODMWorkflow(ctx, spec.workflowConfig(imageCount, imageTotalBytes))
	if err != nil {
		log.Printf("workflow creation rejected reason=argo_create_failed project_id=%q error=%v", spec.projectID, err)
		return fail(reject("argo_create_failed", 500, "Failed to create workflow", err))
	}
	log.Printf("workflow creation accepted workflow=%q project_id=%q", wf.Name, spec.projectID)
	log.Printf(
		"POST /task/new: created workflow name=%q projectID=%q readPath=%q writePath=%q odmFlags=%v s3Region=%q imageCount=%d imageTotalBytes=%d endpoint=%q",
		wf.Name, spec.projectID, spec.readPath, spec.writePath, spec.odmFlags, spec.s3Region, imageCount, imageTotalBytes, spec.s3Endpoint,
	)

	// If this fails, the workflow exists in Argo but won't be visible via the
	// API - treat as a hard error so the caller retries rather than losing it.
	_, err = a.metadataStore.CreateJob(ctx, wf.Name, spec.projectID, spec.readPath, spec.writePath, spec.odmFlags, spec.s3Region,
		// Preserve file sources for restart.
		workspaceFileMetadata(spec.files))
	if err != nil {
		log.Printf("workflow created but metadata update failed workflow=%q reason=metadata_create_failed error=%v", wf.Name, err)
		if rollbackErr := a.workflowClient.DeleteWorkflow(ctx, wf.Name); rollbackErr != nil && !k8serrors.IsNotFound(rollbackErr) {
			log.Printf("POST /task/new: failed cleanup delete of unmanaged workflow %q after metadata create failure: %v", wf.Name, rollbackErr)
		} else {
			log.Printf("POST /task/new: compensated orphan workflow %q after metadata create failure", wf.Name)
		}
		return fail(reject("metadata_create_failed", 500, "Workflow created but failed to record metadata - retry the request", err))
	}

	if err := a.metadataStore.MergeJobMetadata(ctx, wf.Name, spec.newTaskMetadata(imageCount, imageTotalBytes, req.Webhook)); err != nil {
		log.Printf("workflow created but metadata enrichment failed workflow=%q reason=metadata_enrichment_failed error=%v", wf.Name, err)
	}

	resp := &TaskNewResponse{}
	resp.Body.UUID = wf.Name
	metricResult = "success"
	metricReason = "none"
	return resp, nil
}

func (a *API) listTasks(ctx context.Context, input *tokenInput) (*TaskListResponse, error) {
	log.Printf("GET /task/list: token_provided=%t", input.Token != "")

	wfList, err := a.workflowClient.ListWorkflows(ctx, "")
	if err != nil {
		log.Printf("GET /task/list: failed to list workflows: %v", err)
		return nil, huma.NewError(500, "Failed to list tasks", err)
	}

	resp := &TaskListResponse{Body: make([]TaskListItem, 0, len(wfList.Items))}
	for _, wf := range wfList.Items {
		resp.Body = append(resp.Body, TaskListItem{UUID: wf.Name})
	}
	log.Printf("GET /task/list: returned %d tasks", len(resp.Body))
	return resp, nil
}

type taskInfoInput struct {
	UUID       string `path:"uuid" doc:"UUID of the task"`
	Token      string `query:"token" doc:"Authentication token (optional)"`
	WithOutput int    `query:"with_output" default:"0" doc:"Line number to start console output from"`
}

func (a *API) getTaskInfo(ctx context.Context, input *taskInfoInput) (*TaskInfoResponse, error) {
	log.Printf("GET /task/%s/info: token_provided=%t with_output=%d", input.UUID, input.Token != "", input.WithOutput)

	// Without metadata the task doesn't exist for this ScaleODM instance.
	job, err := a.metadataStore.GetJob(ctx, input.UUID)
	if err != nil {
		log.Printf("GET /task/%s/info: failed to retrieve task metadata: %v", input.UUID, err)
		return nil, huma.NewError(500, "Failed to retrieve task metadata", err)
	}
	if job == nil {
		log.Printf("GET /task/%s/info: task not found in metadata store", input.UUID)
		return nil, huma.NewError(404, "Task not found")
	}

	status, progress := a.taskStatus(ctx, input.UUID, job)
	info := TaskInfo{
		UUID:        job.WorkflowName,
		Name:        job.ODMProjectID,
		DateCreated: job.CreatedAt.Unix(),
		Status:      status,
		Options:     taskOptions(job),
		ImagesCount: parseTaskMetadata(job.Metadata).ImageCount,
		Progress:    progress,
	}
	if job.StartedAt != nil {
		endTime := time.Now()
		if job.CompletedAt != nil {
			endTime = *job.CompletedAt
		}
		info.ProcessingTime = endTime.Sub(*job.StartedAt).Milliseconds()
	}

	if input.WithOutput > 0 {
		var logs strings.Builder
		if err := a.taskLogs(ctx, job, &logs); err != nil {
			log.Printf("GET /task/%s/info: load logs: %v", input.UUID, err)
		} else if lines := strings.Split(logs.String(), "\n"); input.WithOutput < len(lines) {
			info.Output = lines[input.WithOutput:]
		}
	}

	log.Printf("GET /task/%s/info: returning status=%d progress=%d", input.UUID, info.Status.Code, info.Progress)
	return &TaskInfoResponse{Body: info}, nil
}

// taskStatus never writes, since the reconciler owns persisting status.
func (a *API) taskStatus(ctx context.Context, uuid string, job *meta.JobMetadata) (TaskStatus, int) {
	status, msg := job.JobStatus, ""
	if !status.IsTerminal() {
		wf, err := a.workflowClient.GetWorkflow(ctx, uuid)
		switch {
		case err == nil:
			status, msg = workflows.LiveStatus(job.JobStatus, wf)
		case !k8serrors.IsNotFound(err):
			log.Printf("GET /task/%s/info: failed to fetch live workflow state: %v", uuid, err)
		}
	}
	if msg == "" && job.ErrorMessage != nil {
		msg = *job.ErrorMessage
	}
	return TaskStatus{Code: status.NodeODMCode(), ErrorMessage: msg}, status.Progress()
}

func taskOptions(job *meta.JobMetadata) []TaskOption {
	var options []TaskOption
	if len(job.ODMFlags) > 0 {
		var flags []string
		if err := json.Unmarshal(job.ODMFlags, &flags); err != nil {
			log.Printf("GET /task/%s/info: failed to unmarshal stored ODM flags: %v", job.WorkflowName, err)
		} else {
			options = optionsFromFlags(flags)
		}
	}

	storedFiles := parseTaskMetadata(job.Metadata).workspaceFiles()
	for _, option := range []string{workflows.BoundaryOptionName, workflows.CamerasOptionName} {
		source, ok := storedFiles[option]
		if !ok {
			continue
		}
		options = append(options, TaskOption{Name: option, Value: cmp.Or(source.Inline, source.S3Path)})
	}
	return options
}

func optionsFromFlags(flags []string) []TaskOption {
	options := make([]TaskOption, 0, len(flags))
	for _, flag := range flags {
		if !strings.HasPrefix(flag, "--") {
			// Bare value from old pair-format storage: attach to previous option.
			if len(options) > 0 {
				options[len(options)-1].Value = flag
			}
			continue
		}
		name := strings.TrimPrefix(flag, "--")
		var value interface{} = true
		if key, val, ok := strings.Cut(name, "="); ok {
			name = key
			value = val
		}
		options = append(options, TaskOption{Name: name, Value: value})
	}
	return options
}

// taskLogs falls back to archived logs when the task has an output path.
func (a *API) taskLogs(ctx context.Context, job *meta.JobMetadata, w io.Writer) error {
	if job.WriteS3Path != "" {
		return a.workflowClient.GetWorkflowLogsWithArchiveFallback(ctx, job.WorkflowName, w)
	}
	return a.workflowClient.GetWorkflowLogs(ctx, job.WorkflowName, w)
}

type taskOutputInput struct {
	UUID  string `path:"uuid" doc:"UUID of the task"`
	Token string `query:"token" doc:"Authentication token (optional)"`
	Line  int    `query:"line" default:"0" doc:"Line number to start from"`
}

func (a *API) getTaskOutput(ctx context.Context, input *taskOutputInput) (*struct{ Body string }, error) {
	log.Printf("GET /task/%s/output: token_provided=%t line=%d", input.UUID, input.Token != "", input.Line)

	job, err := a.metadataStore.GetJob(ctx, input.UUID)
	if err != nil {
		log.Printf("GET /task/%s/output: failed to retrieve job metadata: %v", input.UUID, err)
		return nil, huma.NewError(500, "Failed to retrieve job metadata", err)
	}
	if job == nil {
		log.Printf("GET /task/%s/output: task not found", input.UUID)
		return nil, huma.NewError(404, "Task not found")
	}

	var logs strings.Builder
	if err := a.taskLogs(ctx, job, &logs); err != nil {
		log.Printf("GET /task/%s/output: load logs: %v", input.UUID, err)
		return nil, huma.NewError(500, "Failed to retrieve logs", err)
	}

	output := logs.String()
	if input.Line > 0 {
		if lines := strings.Split(output, "\n"); input.Line < len(lines) {
			output = strings.Join(lines[input.Line:], "\n")
		}
	}
	log.Printf("GET /task/%s/output: returned %d bytes of output", input.UUID, len(output))
	return &struct{ Body string }{Body: output}, nil
}

// Inputs with a Body are aliases of anonymous structs: Huma names a body
// schema after a named parent type, which would rename the OpenAPI schemas.
type taskUUIDInput = struct {
	Token string `query:"token" doc:"Authentication token (optional)"`
	Body  struct {
		UUID string `json:"uuid" doc:"UUID of the task"`
	}
}

func (a *API) cancelTask(ctx context.Context, input *taskUUIDInput) (*SuccessResponse, error) {
	uuid := input.Body.UUID
	log.Printf("POST /task/cancel: uuid=%q token_provided=%t", uuid, input.Token != "")

	if err := a.workflowClient.DeleteWorkflow(ctx, uuid); err != nil {
		if k8serrors.IsNotFound(err) {
			log.Printf("POST /task/cancel: task %q not found", uuid)
			return nil, huma.NewError(404, "Task not found")
		}
		log.Printf("POST /task/cancel: failed to cancel task %q: %v", uuid, err)
		return nil, huma.NewError(500, "Failed to cancel task", err)
	}
	if err := a.metadataStore.UpdateJobStatus(ctx, uuid, meta.JobCanceled, nil); err != nil {
		log.Printf("POST /task/cancel: failed to update job status for %q: %v", uuid, err)
	}

	log.Printf("POST /task/cancel: task %q canceled", uuid)
	return successResponse(), nil
}

func (a *API) removeTask(ctx context.Context, input *taskUUIDInput) (*SuccessResponse, error) {
	uuid := input.Body.UUID
	log.Printf("POST /task/remove: uuid=%q token_provided=%t", uuid, input.Token != "")

	if err := a.workflowClient.DeleteWorkflow(ctx, uuid); err != nil && !k8serrors.IsNotFound(err) {
		log.Printf("POST /task/remove: failed to delete workflow for %q: %v", uuid, err)
		return nil, huma.NewError(500, "Failed to remove task", err)
	}
	if err := a.metadataStore.DeleteJob(ctx, uuid); err != nil {
		log.Printf("Warning: Failed to delete metadata: %v", err)
	}

	log.Printf("POST /task/remove: task %q removed (workflow+metadata)", uuid)
	return successResponse(), nil
}

type taskRestartInput = struct {
	Token string `query:"token" doc:"Authentication token (optional)"`
	Body  struct {
		UUID    string `json:"uuid" doc:"UUID of the task"`
		Options string `json:"options,omitempty" doc:"New options (optional)"`
	}
}

func (a *API) restartTask(ctx context.Context, input *taskRestartInput) (*SuccessResponse, error) {
	oldWorkflowName := input.Body.UUID
	log.Printf("POST /task/restart: uuid=%q token_provided=%t options_set=%t", oldWorkflowName, input.Token != "", input.Body.Options != "")

	job, err := a.metadataStore.GetJob(ctx, oldWorkflowName)
	if err != nil {
		log.Printf("POST /task/restart: failed to retrieve metadata for %q: %v", oldWorkflowName, err)
		return nil, huma.NewError(500, "Failed to retrieve task metadata", err)
	}
	if job == nil {
		return nil, huma.NewError(404, "Task not found")
	}
	metadata := parseTaskMetadata(job.Metadata)

	spec, rej := restartSpec(job, metadata, input.Body.Options)
	if rej != nil {
		log.Printf("POST /task/restart: rejected %q: %s %v", oldWorkflowName, rej.msg, rej.errs)
		return nil, rej.humaError()
	}
	log.Printf("POST /task/restart: endpoint selection endpoint=%q allowlist_enforced=%t", spec.s3Endpoint, config.SCALEODM_ENFORCE_S3_ENDPOINT_ALLOWLIST)

	taskClient, taskClientErr := s3.GetS3ClientForEndpoint(spec.workflowS3Endpoint())

	// Recount only missing stats. A failed count keeps the stored values.
	imageCount, imageTotalBytes := metadata.ImageCount, metadata.ImageTotalBytes
	if (imageCount == 0 || imageTotalBytes == 0) && taskClientErr == nil {
		if counted, totalBytes, err := s3.CountImageStatsInS3Path(ctx, taskClient, spec.readPath, spec.excludePatterns()); err == nil {
			imageCount = cmp.Or(imageCount, counted)
			imageTotalBytes = cmp.Or(imageTotalBytes, totalBytes)
		}
	}

	// File validation requires a working S3 client.
	if hasS3WorkspaceFile(spec.files) {
		if taskClientErr != nil {
			log.Printf("POST /task/restart: cannot verify workspace files, S3 client init failed endpoint=%q: %v", spec.s3Endpoint, taskClientErr)
			return nil, huma.NewError(500, "Failed to initialize S3 client to verify task files", taskClientErr)
		}
		if option, err := preflightWorkspaceFiles(ctx, taskClient, spec.files); err != nil {
			log.Printf("POST /task/restart: %s preflight failed endpoint=%q: %v", option, spec.s3Endpoint, err)
			return nil, huma.NewError(400, err.Error())
		}
	}

	wf, err := a.workflowClient.CreateODMWorkflow(ctx, spec.workflowConfig(imageCount, imageTotalBytes))
	if err != nil {
		log.Printf("POST /task/restart: failed to create new workflow for %q: %v", oldWorkflowName, err)
		return nil, huma.NewError(500, "Failed to restart task", err)
	}

	metadataPatch := workspaceFileMetadata(spec.files)
	metadataPatch[metadataImageCountKey] = imageCount
	metadataPatch[metadataImageTotalBytesKey] = imageTotalBytes
	metadataPatch[meta.MetadataWorkflowMissingFirstSeenKey] = nil
	if spec.s3Endpoint != "" {
		metadataPatch[metadataS3EndpointKey] = spec.s3Endpoint
	}
	err = a.metadataStore.RestartJobMetadata(ctx, oldWorkflowName, wf.Name, job.ODMProjectID, job.ReadS3Path, job.WriteS3Path, spec.odmFlags, job.S3Region, metadataPatch)
	if err != nil {
		log.Printf("POST /task/restart: failed to swap metadata for %q -> %q: %v", oldWorkflowName, wf.Name, err)
		if wf.Name != oldWorkflowName {
			if delErr := a.workflowClient.DeleteWorkflow(ctx, wf.Name); delErr != nil && !k8serrors.IsNotFound(delErr) {
				log.Printf("POST /task/restart: failed cleanup delete of unmanaged workflow %q: %v", wf.Name, delErr)
			}
		}
		return nil, huma.NewError(500, "Failed to persist restarted task metadata", err)
	}

	if wf.Name != oldWorkflowName {
		if err := a.workflowClient.DeleteWorkflow(ctx, oldWorkflowName); err != nil && !k8serrors.IsNotFound(err) {
			log.Printf("POST /task/restart: failed post-cutover cleanup delete of old workflow %q: %v", oldWorkflowName, err)
		}
	}

	log.Printf("POST /task/restart: task %q restarted as workflow %q imageCount=%d imageTotalBytes=%d endpoint=%q", oldWorkflowName, wf.Name, imageCount, imageTotalBytes, spec.s3Endpoint)
	return successResponse(), nil
}
