package api

import (
	"bytes"
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
	"github.com/minio/minio-go/v7"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/observability"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/version"
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

const (
	metadataS3EndpointKey            = "s3_endpoint"
	metadataImageCountKey            = "image_count"
	metadataImageTotalBytesKey       = "image_total_bytes"
	metadataWorkflowMissingFirstSeen = "workflow_missing_first_seen_at"
	metadataProcessingModeKey        = "processing_mode"
	metadataExcludePathsKey          = "exclude_paths"
	metadataUseDefaultExcludesKey    = "use_default_excludes"
	metadataS3ScanDepthKey           = "s3_scan_depth"
	metadataCapacityTypeKey          = "capacity_type"
)

const (
	taskAssetsDefaultAdditionalLimit = 100
	taskAssetsMaxAdditionalLimit     = 1000
)

func normalizeOptionalS3Endpoint(endpoint string) (string, error) {
	if strings.TrimSpace(endpoint) == "" {
		return "", nil
	}
	return s3.NormalizeEndpoint(endpoint)
}

func parseMetadataMap(metadataJSON []byte) map[string]interface{} {
	if len(metadataJSON) == 0 {
		return map[string]interface{}{}
	}
	metaMap := map[string]interface{}{}
	if err := json.Unmarshal(metadataJSON, &metaMap); err != nil {
		return map[string]interface{}{}
	}
	return metaMap
}

func normalizedEndpointFromMetadata(metadataJSON []byte) string {
	metaMap := parseMetadataMap(metadataJSON)
	endpoint, _ := metaMap[metadataS3EndpointKey].(string)
	normalized, err := normalizeOptionalS3Endpoint(endpoint)
	if err != nil {
		return ""
	}
	return normalized
}

func parseAllowedEndpointAllowlist(raw string) map[string]struct{} {
	allowed := map[string]struct{}{}
	for _, candidate := range strings.Split(raw, ",") {
		normalized, err := normalizeOptionalS3Endpoint(candidate)
		if err != nil || normalized == "" {
			continue
		}
		allowed[normalized] = struct{}{}
	}
	return allowed
}

func enforceEndpointAllowlist(endpoint string) error {
	if !config.SCALEODM_ENFORCE_S3_ENDPOINT_ALLOWLIST || endpoint == "" {
		return nil
	}
	allowed := parseAllowedEndpointAllowlist(config.SCALEODM_ALLOWED_S3_ENDPOINTS)
	if _, ok := allowed[endpoint]; ok {
		return nil
	}
	return fmt.Errorf("s3 endpoint %q is not in SCALEODM_ALLOWED_S3_ENDPOINTS", endpoint)
}

func resolveTaskS3Client(metadataJSON []byte) (*minio.Client, string, error) {
	endpoint := normalizedEndpointFromMetadata(metadataJSON)
	if endpoint != "" {
		client, err := s3.GetS3ClientForEndpoint(endpoint)
		if err != nil {
			return nil, endpoint, err
		}
		return client, endpoint, nil
	}
	return s3.GetS3Client(), "", nil
}

func taskS3Client(metadataJSON []byte) (*minio.Client, error) {
	client, _, err := resolveTaskS3Client(metadataJSON)
	return client, err
}

func metadataImageCount(metadataJSON []byte) int {
	metaMap := parseMetadataMap(metadataJSON)
	value, ok := metaMap[metadataImageCountKey]
	if !ok {
		return 0
	}
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func metadataProcessingMode(metadataJSON []byte) string {
	metaMap := parseMetadataMap(metadataJSON)
	if mode, ok := metaMap[metadataProcessingModeKey].(string); ok && mode != "" {
		return mode
	}
	return workflows.ProcessingModeStandard
}

func metadataCapacityType(metadataJSON []byte) string {
	metaMap := parseMetadataMap(metadataJSON)
	if ct, ok := metaMap[metadataCapacityTypeKey].(string); ok && workflows.IsValidCapacityType(ct) {
		return ct
	}
	return config.SCALEODM_WORKFLOW_CAPACITY_TYPE
}

func metadataS3ScanDepth(metadataJSON []byte) int {
	metaMap := parseMetadataMap(metadataJSON)
	value, ok := metaMap[metadataS3ScanDepthKey]
	if !ok {
		return workflows.DefaultS3ScanDepth
	}
	switch n := value.(type) {
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	}
	return workflows.DefaultS3ScanDepth
}

func metadataExcludePaths(metadataJSON []byte) ([]string, bool) {
	metaMap := parseMetadataMap(metadataJSON)
	rawList, ok := metaMap[metadataExcludePathsKey]
	if !ok {
		return nil, false
	}
	listVal, ok := rawList.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(listVal))
	for _, item := range listVal {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

func metadataUseDefaultExcludes(metadataJSON []byte) bool {
	metaMap := parseMetadataMap(metadataJSON)
	if v, ok := metaMap[metadataUseDefaultExcludesKey].(bool); ok {
		return v
	}
	// Absent/legacy jobs default to true to preserve safer behaviour.
	return true
}

func metadataImageTotalBytes(metadataJSON []byte) int64 {
	metaMap := parseMetadataMap(metadataJSON)
	value, ok := metaMap[metadataImageTotalBytesKey]
	if !ok {
		return 0
	}
	switch n := value.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func detectWorkflowInfraFailure(wf *wfv1.Workflow) string {
	infraFailure := func(msg string) bool {
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "createcontainerconfigerror") {
			return true
		}
		if strings.Contains(lower, "secret") && strings.Contains(lower, "not found") {
			return true
		}
		if strings.Contains(lower, "errimagepull") || strings.Contains(lower, "imagepullbackoff") {
			return true
		}
		if strings.Contains(lower, "failed to pull image") || strings.Contains(lower, "back-off pulling image") {
			return true
		}
		return false
	}

	if infraFailure(wf.Status.Message) {
		return wf.Status.Message
	}

	for _, node := range wf.Status.Nodes {
		if infraFailure(node.Message) {
			return node.Message
		}
	}

	return ""
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
	// If omitted or empty, ODM runs in standard mode (no extra flags).
	Options string `json:"options,omitempty" form:"options" doc:"JSON array of processing options (optional; empty runs standard ODM with no extra flags)"`

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
	// S3 region. Defaults to "garage" when s3Endpoint is set, otherwise "us-east-1".
	S3Region string `json:"s3Region,omitempty" form:"s3Region" default:"us-east-1" doc:"S3 region (default: us-east-1, or garage when s3Endpoint is set)"`

	// Optional override for creation timestamp. If omitted, the server uses the current
	// time when the job is created.
	DateCreated int64 `json:"dateCreated,omitempty" form:"dateCreated" doc:"Override creation timestamp (optional; defaults to current time when omitted)"`

	// ProcessingMode selects the pipeline shape. Pick the mode that matches
	// the post-processing needs; the depth/scope of the input scan is
	// configured separately via S3ScanDepth.
	//
	// Supported today:
	//   - "standard" (default): the regular ODM pipeline (download → process
	//     → upload). Imagery under readS3Path is gathered into a single ODM
	//     run; how deep the scan walks beneath readS3Path is controlled by
	//     s3ScanDepth.
	//
	// Reserved (return 501 today):
	//   - "merge-existing": stitch already-processed per-task outputs (orthos,
	//     DEMs, point clouds) into a single set of products via the merge
	//     half of split-merge. Much cheaper than re-running per-task
	//     processing.
	//   - "thermal": thermal imagery pipeline with dedicated pre-processing
	//     before ODM (radiometric handling, alignment).
	//   - "city-scale": large-area (>40 km²) projects with iterative
	//     corrective alignment from a central task using prior LAZ point
	//     clouds, plus a final alignment pass against a global DEM.
	ProcessingMode string `json:"processingMode,omitempty" form:"processingMode" doc:"Pipeline mode: 'standard' (default). Reserved: 'merge-existing', 'thermal', 'city-scale'."`

	// CapacityType selects the Karpenter node pool for workflow pods.
	// Use "on-demand" for VIP or time-sensitive jobs that cannot tolerate spot
	// interruption. Defaults to "spot" when omitted.
	CapacityType string `json:"capacityType,omitempty" form:"capacityType" doc:"Node capacity type for workflow pods: 'spot' (default) or 'on-demand' for VIP jobs."`

	// S3ScanDepth caps how deep the download stage walks beneath readS3Path.
	// Defaults to 1 (only files directly under the given path - the right
	// choice for layouts like '.../task-id/images/'). Use a higher value to
	// roll up multiple task subdirs under a project root (e.g. depth 3 with
	// readS3Path 'projectid/' picks up 'projectid/taskid/images/*.jpg').
	// Range: 1..10. A nil/zero value resolves to the default.
	S3ScanDepth *int `json:"s3ScanDepth,omitempty" form:"s3ScanDepth" doc:"Max depth for the rclone scan beneath readS3Path (default 1, max 10). Use >1 to pick up imagery under nested task subdirs."`

	// ExcludePaths is an optional JSON array of rclone-style filter patterns
	// applied to the download stage on top of the default set. Patterns must
	// be relative (no leading '/'), contain no '..' segments, and use only
	// glob metacharacters. Example: ["scratch/**", "*.bak"].
	ExcludePaths string `json:"excludePaths,omitempty" form:"excludePaths" doc:"JSON array of rclone-style exclude patterns appended to the default set"`

	// UseDefaultExcludes controls whether DefaultProjectExcludes is applied.
	// Defaults to true; set to false only when you genuinely want to
	// re-process a previous ODM run's output as input.
	UseDefaultExcludes *bool `json:"useDefaultExcludes,omitempty" form:"useDefaultExcludes" doc:"Apply the built-in ODM-output exclude list (default: true)"`
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

type TaskAssetsPrimaryItem struct {
	ID          string `json:"id" doc:"Logical asset identifier"`
	Asset       string `json:"asset" doc:"Asset key used by /task/{uuid}/download/{asset}"`
	Exists      bool   `json:"exists" doc:"Whether this asset currently exists in task output"`
	DownloadURL string `json:"downloadUrl,omitempty" doc:"ScaleODM download URL when the asset exists"`
}

type TaskAssetsAdditionalItem struct {
	Asset       string `json:"asset" doc:"Discovered additional asset key"`
	DownloadURL string `json:"downloadUrl" doc:"ScaleODM download URL for this asset"`
}

type TaskAssets struct {
	Primary    []TaskAssetsPrimaryItem    `json:"primary" doc:"Primary ODM assets with existence status"`
	Additional []TaskAssetsAdditionalItem `json:"additional,omitempty" doc:"Optional additional discovered assets"`
}

type TaskAssetsResponse struct {
	Body TaskAssets
}

type taskPrimaryAssetDefinition struct {
	ID     string
	Assets []string
}

var taskPrimaryAssetDefinitions = []taskPrimaryAssetDefinition{
	{ID: "all_zip", Assets: []string{"all.zip"}},
	{ID: "orthophoto", Assets: []string{"odm_orthophoto/odm_orthophoto.tif", "orthophoto.tif"}},
	{ID: "dsm", Assets: []string{"odm_dem/dsm.tif", "dsm.tif"}},
	{ID: "dtm", Assets: []string{"odm_dem/dtm.tif", "dtm.tif"}},
	{ID: "point_cloud", Assets: []string{"odm_georeferencing/odm_georeferenced_model.laz", "odm_georeferencing/odm_georeferenced_model.las", "odm_georeferencing/odm_georeferenced_model.ply", "georeferenced_model.laz", "georeferenced_model.las", "georeferenced_model.ply", "point_cloud.laz", "point_cloud.ply"}},
}

var taskAssetAliasCandidates = map[string][]string{
	"orthophoto": {"odm_orthophoto/odm_orthophoto.tif", "orthophoto.tif"},
	"dsm":        {"odm_dem/dsm.tif", "dsm.tif"},
	"dtm":        {"odm_dem/dtm.tif", "dtm.tif"},
	"point_cloud": {
		"odm_georeferencing/odm_georeferenced_model.laz",
		"odm_georeferencing/odm_georeferenced_model.las",
		"odm_georeferencing/odm_georeferenced_model.ply",
		"georeferenced_model.laz",
		"georeferenced_model.las",
		"georeferenced_model.ply",
		"point_cloud.laz",
		"point_cloud.ply",
	},
}

func taskAssetDownloadURL(uuid, asset string) string {
	return fmt.Sprintf("/task/%s/download/%s", uuid, asset)
}

func resolveAssetAliasCandidate(ctx context.Context, client *minio.Client, writeS3Path, requestedAsset string) (string, bool, error) {
	candidates, ok := taskAssetAliasCandidates[requestedAsset]
	if !ok {
		return "", false, nil
	}

	for _, candidate := range candidates {
		exists, err := s3.ObjectExistsInS3Path(ctx, client, writeS3Path, candidate)
		if err != nil {
			return "", false, err
		}
		if exists {
			return candidate, true, nil
		}
	}

	return "", true, nil
}

func clampTaskAdditionalLimit(limit int) int {
	if limit <= 0 {
		return taskAssetsDefaultAdditionalLimit
	}
	if limit > taskAssetsMaxAdditionalLimit {
		return taskAssetsMaxAdditionalLimit
	}
	return limit
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
		resp.Body.Version = version.Version // The ScaleODM version (normally the NodeODM version)
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

		req := input.Body

		// Log incoming task creation request
		log.Printf(
			"POST /task/new: name=%q readS3Path=%q writeS3Path=%q zipurl=%q skipPostProcessing=%t webhook_set=%t s3Region=%q s3Endpoint=%q dateCreated=%d processingMode=%q token_provided=%t setUUID_set=%t",
			req.Name,
			req.ReadS3Path,
			req.WriteS3Path,
			req.ZipURL,
			req.SkipPostProcessing,
			req.Webhook != "",
			req.S3Region,
			req.S3Endpoint,
			req.DateCreated,
			req.ProcessingMode,
			input.Token != "",
			input.SetUUID != "",
		)

		// Resolve processing mode + compose exclude list before doing any
		// expensive work. Reserved modes get 501 so clients can probe support.
		processingMode := req.ProcessingMode
		if processingMode == "" {
			processingMode = workflows.ProcessingModeStandard
		}
		if workflows.IsReservedProcessingMode(processingMode) {
			metricResult = "failure"
			metricReason = "processing_mode_not_implemented"
			log.Printf("POST /task/new: processingMode=%q is reserved but not yet implemented", processingMode)
			return nil, huma.NewError(501, fmt.Sprintf("processingMode %q is reserved for a future pipeline and is not yet implemented", processingMode))
		}
		if !workflows.IsImplementedProcessingMode(processingMode) {
			metricResult = "failure"
			metricReason = "invalid_processing_mode"
			log.Printf("POST /task/new: invalid processingMode=%q", processingMode)
			return nil, huma.NewError(400, fmt.Sprintf("invalid processingMode %q (supported: standard)", processingMode))
		}

		capacityType := req.CapacityType
		if capacityType == "" {
			capacityType = config.SCALEODM_WORKFLOW_CAPACITY_TYPE
		}
		if !workflows.IsValidCapacityType(capacityType) {
			metricResult = "failure"
			metricReason = "invalid_capacity_type"
			log.Printf("POST /task/new: invalid capacityType=%q", capacityType)
			return nil, huma.NewError(400, fmt.Sprintf("invalid capacityType %q (supported: spot, on-demand)", capacityType))
		}

		s3ScanDepth := 0
		if req.S3ScanDepth != nil {
			s3ScanDepth = *req.S3ScanDepth
		}
		s3ScanDepth, err := workflows.ValidateS3ScanDepth(s3ScanDepth)
		if err != nil {
			metricResult = "failure"
			metricReason = "invalid_s3_scan_depth"
			log.Printf("POST /task/new: invalid s3ScanDepth: %v", err)
			return nil, huma.NewError(400, err.Error())
		}

		var userExcludes []string
		if strings.TrimSpace(req.ExcludePaths) != "" {
			if err := json.Unmarshal([]byte(req.ExcludePaths), &userExcludes); err != nil {
				metricResult = "failure"
				metricReason = "invalid_exclude_paths"
				log.Printf("POST /task/new: invalid excludePaths JSON: %v", err)
				return nil, huma.NewError(400, "Invalid excludePaths JSON (expected an array of strings)", err)
			}
			for _, p := range userExcludes {
				if err := workflows.ValidateExcludePattern(p); err != nil {
					metricResult = "failure"
					metricReason = "invalid_exclude_pattern"
					log.Printf("POST /task/new: invalid exclude pattern %q: %v", p, err)
					return nil, huma.NewError(400, fmt.Sprintf("invalid exclude pattern %q: %s", p, err.Error()))
				}
			}
		}

		useDefaultExcludes := true
		if req.UseDefaultExcludes != nil {
			useDefaultExcludes = *req.UseDefaultExcludes
		}
		excludePatterns := workflows.ComposeExcludePatterns(useDefaultExcludes, userExcludes)

		// Parse options if provided
		var options []TaskOption
		var odmFlags []string
		if req.Options != "" {
			if err := json.Unmarshal([]byte(req.Options), &options); err != nil {
				metricResult = "failure"
				metricReason = "invalid_options"
				span.AddEvent("task.new.rejected", trace.WithAttributes(attribute.String("reason", metricReason)))
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
						odmFlags = append(odmFlags, fmt.Sprintf("%s=%v", flag, opt.Value))
					}
				}
			}
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
				metricReason = "invalid_zipurl"
				log.Printf("POST /task/new: invalid zipurl=%q (must be s3:// or http(s) zip URL)", req.ZipURL)
				return nil, huma.NewError(400, "zipurl must be an s3://... prefix or a http(s) zip URL")
			}

			if isS3Prefix {
				readPath = strings.TrimSuffix(req.ZipURL, "/") + "/"
				writePath = strings.TrimSuffix(req.ZipURL, "/") + "-output/"
			} else {
				// HTTP zip - not supported for S3 read/write workflow
				metricReason = "http_zip_not_supported"
				log.Printf("POST /task/new: HTTP zip URLs not supported zipurl=%q", req.ZipURL)
				return nil, huma.NewError(400, "HTTP zip URLs not supported. Use readS3Path for S3-based processing")
			}
		} else {
			metricReason = "missing_read_path"
			log.Printf("POST /task/new: missing required readS3Path or zipurl")
			return nil, huma.NewError(400, "readS3Path is required (or zipurl for legacy support)")
		}

		// Validate S3 paths
		if !strings.HasPrefix(readPath, "s3://") {
			metricReason = "invalid_read_path"
			log.Printf("POST /task/new: readPath must be s3:// path, got %q", readPath)
			return nil, huma.NewError(400, "readS3Path must be an s3:// path")
		}
		if !strings.HasPrefix(writePath, "s3://") {
			metricReason = "invalid_write_path"
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
			metricReason = "invalid_project_name"
			return nil, huma.NewError(400, err.Error())
		}
		for _, flag := range odmFlags {
			if err := validateShellSafe(flag, "options flag"); err != nil {
				metricReason = "invalid_option_flag"
				return nil, huma.NewError(400, err.Error())
			}
		}
		if err := validateShellSafe(readPath, "readS3Path"); err != nil {
			metricReason = "invalid_read_path"
			return nil, huma.NewError(400, err.Error())
		}
		if err := validateShellSafe(writePath, "writeS3Path"); err != nil {
			metricReason = "invalid_write_path"
			return nil, huma.NewError(400, err.Error())
		}

		// Determine S3 region & optional endpoint
		s3Region := req.S3Region
		s3Endpoint, err := normalizeOptionalS3Endpoint(req.S3Endpoint)
		if err != nil {
			metricResult = "failure"
			metricReason = "invalid_s3_endpoint"
			return nil, huma.NewError(400, "Invalid s3Endpoint", err)
		}
		if err := enforceEndpointAllowlist(s3Endpoint); err != nil {
			metricResult = "failure"
			metricReason = "invalid_s3_endpoint"
			return nil, huma.NewError(400, "Invalid s3Endpoint", err)
		}
		if s3Region == "" {
			if s3Endpoint != "" {
				s3Region = "garage"
			} else {
				s3Region = "us-east-1"
			}
		}
		log.Printf("POST /task/new: endpoint selection endpoint=%q region=%q allowlist_enforced=%t", s3Endpoint, s3Region, config.SCALEODM_ENFORCE_S3_ENDPOINT_ALLOWLIST)

		// Count images before workflow submission so resources can be sized.
		taskClient, clientErr := s3.GetS3ClientForEndpoint(config.AWS_S3_ENDPOINT)
		if s3Endpoint != "" {
			taskClient, clientErr = s3.GetS3ClientForEndpoint(s3Endpoint)
		}
		if clientErr != nil {
			metricResult = "failure"
			metricReason = "s3_client_init_failed"
			log.Printf("POST /task/new: failed to construct S3 client for image counting endpoint=%q: %v", s3Endpoint, clientErr)
			return nil, huma.NewError(500, "Failed to initialize S3 client", clientErr)
		}
		imageCount, imageTotalBytes, countErr := s3.CountImageStatsInS3PathWithExcludes(ctx, taskClient, readPath, excludePatterns)
		if countErr != nil {
			metricResult = "failure"
			metricReason = "image_count_failed"
			log.Printf("POST /task/new: failed to count images for readPath=%q endpoint=%q: %v", readPath, s3Endpoint, countErr)
			return nil, huma.NewError(400, "Unable to read imagery from readS3Path", countErr)
		}

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
		wfConfig.ImageCount = imageCount
		wfConfig.ImageTotalBytes = imageTotalBytes
		wfConfig.ProcessingMode = processingMode
		wfConfig.CapacityType = capacityType
		wfConfig.ExcludePaths = excludePatterns
		wfConfig.S3ScanDepth = s3ScanDepth

		// Submit workflow to Argo
		wf, err := a.workflowClient.CreateODMWorkflow(ctx, wfConfig)
		if err != nil {
			metricResult = "failure"
			metricReason = "argo_create_failed"
			log.Printf("workflow creation rejected reason=argo_create_failed project_id=%q error=%v", projectID, err)
			return nil, huma.NewError(500, "Failed to create workflow", err)
		}
		log.Printf("workflow creation accepted workflow=%q project_id=%q", wf.Name, projectID)

		log.Printf(
			"POST /task/new: created workflow name=%q projectID=%q readPath=%q writePath=%q odmFlags=%v s3Region=%q imageCount=%d imageTotalBytes=%d endpoint=%q",
			wf.Name,
			projectID,
			readPath,
			writePath,
			odmFlags,
			s3Region,
			imageCount,
			imageTotalBytes,
			s3Endpoint,
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
			metricResult = "failure"
			metricReason = "metadata_create_failed"
			log.Printf("workflow created but metadata update failed workflow=%q reason=metadata_create_failed error=%v", wf.Name, err)
			if rollbackErr := a.workflowClient.DeleteWorkflow(ctx, wf.Name); rollbackErr != nil && !isNotFound(rollbackErr) {
				log.Printf("POST /task/new: failed cleanup delete of unmanaged workflow %q after metadata create failure: %v", wf.Name, rollbackErr)
			} else {
				log.Printf("POST /task/new: compensated orphan workflow %q after metadata create failure", wf.Name)
			}
			return nil, huma.NewError(500, "Workflow created but failed to record metadata - retry the request", err)
		}

		metadataUpdates := map[string]interface{}{
			metadataImageCountKey:            imageCount,
			metadataImageTotalBytesKey:       imageTotalBytes,
			metadataWorkflowMissingFirstSeen: nil,
			metadataProcessingModeKey:        processingMode,
			metadataCapacityTypeKey:          capacityType,
			metadataExcludePathsKey:          userExcludes,
			metadataUseDefaultExcludesKey:    useDefaultExcludes,
			metadataS3ScanDepthKey:           s3ScanDepth,
		}
		if s3Endpoint != "" {
			metadataUpdates[metadataS3EndpointKey] = s3Endpoint
		}
		if metaErr := a.metadataStore.MergeJobMetadata(ctx, wf.Name, metadataUpdates); metaErr != nil {
			log.Printf("workflow created but metadata enrichment failed workflow=%q reason=metadata_enrichment_failed error=%v", wf.Name, metaErr)
		}

		resp := &TaskNewResponse{}
		resp.Body.UUID = wf.Name
		metricResult = "success"
		metricReason = "none"
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

		// Build task info from metadata, but prefer live workflow phase when
		// the Argo workflow still exists.
		statusCode := jobStatusToStatusCode(job.JobStatus)
		progress := jobStatusToProgress(job.JobStatus)
		errorMessage := ""
		if job.ErrorMessage != nil {
			errorMessage = *job.ErrorMessage
		}

		imagesCount := metadataImageCount(job.Metadata)

		if wf, wfErr := a.workflowClient.GetWorkflow(ctx, input.UUID); wfErr == nil {
			statusCode = workflowToStatusCode(wf.Status.Phase)
			progress = workflowToProgress(wf.Status.Phase)
			if (wf.Status.Phase == wfv1.WorkflowFailed || wf.Status.Phase == wfv1.WorkflowError) && wf.Status.Message != "" {
				errorMessage = wf.Status.Message
			}
			if len(job.Metadata) > 0 {
				if err := a.metadataStore.MergeJobMetadata(ctx, input.UUID, map[string]interface{}{metadataWorkflowMissingFirstSeen: nil}); err != nil {
					log.Printf("GET /task/%s/info: failed clearing missing-workflow marker: %v", input.UUID, err)
				}
			}

			// Reconcile infra/runtime pod failures (for example missing secrets,
			// image pull backoff, or container config errors) into failed status
			// without waiting for Argo to eventually mark the whole workflow phase.
			infraFailureReconciled := false
			if statusCode == StatusCodeRunning {
				failureMsg := detectWorkflowInfraFailure(wf)
				if failureMsg != "" {
					statusCode = StatusCodeFailed
					progress = 0
					errorMessage = failureMsg
					observability.RecordWorkflowReconciliation("running_to_failed", "infra_failure")
					log.Printf("reconciliation transition uuid=%s source=argo transition=running_to_failed reason=infra_failure", input.UUID)
					if err := a.metadataStore.UpdateJobStatus(ctx, input.UUID, "failed", &failureMsg); err != nil {
						log.Printf("GET /task/%s/info: failed to persist reconciled failure status: %v", input.UUID, err)
					}
					infraFailureReconciled = true
				}
			}

			// Sync DB status with live Argo phase so the UI (which reads from the
			// DB) reflects running/completed/failed without a separate reconciler.
			if !infraFailureReconciled {
				liveStatus := meta.MapArgoPhaseToJobStatus(string(wf.Status.Phase))
				dbStatus := strings.ToLower(strings.TrimSpace(job.JobStatus))
				if dbStatus != liveStatus && meta.IsForwardJobStatusTransition(dbStatus, liveStatus) {
					var errPtr *string
					if errorMessage != "" {
						errPtr = &errorMessage
					}
					if err := a.metadataStore.UpdateJobStatus(ctx, input.UUID, liveStatus, errPtr); err != nil {
						log.Printf("GET /task/%s/info: failed to sync status db=%q argo=%q: %v", input.UUID, dbStatus, liveStatus, err)
					}
				}
			}
		} else if isNotFound(wfErr) {
			jobStatus := strings.ToLower(job.JobStatus)
			if jobStatus == "queued" || jobStatus == "claimed" || jobStatus == "running" {
				metaMap := parseMetadataMap(job.Metadata)
				now := time.Now().UTC()
				firstSeen := now
				if rawFirstSeen, ok := metaMap[metadataWorkflowMissingFirstSeen].(string); ok && rawFirstSeen != "" {
					if parsed, err := time.Parse(time.RFC3339, rawFirstSeen); err == nil {
						firstSeen = parsed
					}
				} else {
					patchErr := a.metadataStore.MergeJobMetadata(ctx, input.UUID, map[string]interface{}{metadataWorkflowMissingFirstSeen: now.Format(time.RFC3339)})
					if patchErr != nil {
						log.Printf("GET /task/%s/info: failed to persist missing-workflow first-seen timestamp: %v", input.UUID, patchErr)
					}
				}
				grace := time.Duration(config.SCALEODM_WORKFLOW_MISSING_GRACE_SECONDS) * time.Second
				if grace <= 0 {
					grace = 5 * time.Minute
				}
				if now.Sub(firstSeen) < grace {
					log.Printf("GET /task/%s/info: workflow missing but within grace period status=%q first_seen=%s grace=%s", input.UUID, jobStatus, firstSeen.Format(time.RFC3339), grace)
				} else {
					msg := "Workflow missing in Argo beyond grace window; marking task as failed"
					observability.RecordWorkflowReconciliation("missing_workflow_to_failed", "missing_workflow_grace_expired")
					log.Printf("reconciliation transition uuid=%s source=metadata transition=missing_workflow_to_failed reason=missing_workflow_grace_expired", input.UUID)
					if err := a.metadataStore.UpdateJobStatus(ctx, input.UUID, "failed", &msg); err != nil {
						log.Printf("GET /task/%s/info: failed to reconcile missing workflow to failed status: %v", input.UUID, err)
					} else {
						_ = a.metadataStore.MergeJobMetadata(ctx, input.UUID, map[string]interface{}{metadataWorkflowMissingFirstSeen: nil})
						job.JobStatus = "failed"
						job.ErrorMessage = &msg
					}
					statusCode = StatusCodeFailed
					progress = 0
					errorMessage = msg
				}
			}
		} else {
			log.Printf("GET /task/%s/info: failed to fetch live workflow state: %v", input.UUID, wfErr)
		}

		status := TaskStatus{Code: statusCode}
		if errorMessage != "" {
			status.ErrorMessage = errorMessage
		}

		info := TaskInfo{
			UUID:        job.WorkflowName,
			Name:        job.ODMProjectID,
			DateCreated: job.CreatedAt.Unix(),
			Status:      status,
			ImagesCount: imagesCount,
			Progress:    progress,
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
				for i := 0; i < len(flags); i++ {
					flag := flags[i]
					if !strings.HasPrefix(flag, "--") {
						// Bare value from old pair-format storage: attach to previous option.
						if len(info.Options) > 0 {
							info.Options[len(info.Options)-1].Value = flag
						}
						continue
					}
					name := strings.TrimPrefix(flag, "--")
					var value interface{} = true
					if key, val, ok := strings.Cut(name, "="); ok {
						name = key
						value = val
					}
					info.Options = append(info.Options, TaskOption{Name: name, Value: value})
				}
			} else {
				log.Printf("GET /task/%s/info: failed to unmarshal stored ODM flags: %v", input.UUID, err)
			}
		}

		// Get console output if requested
		if input.WithOutput > 0 {
			var logBuilder strings.Builder
			if job.WriteS3Path != "" {
				if err := a.workflowClient.GetWorkflowLogsWithArchiveFallback(ctx, input.UUID, &logBuilder); err == nil {
					lines := strings.Split(logBuilder.String(), "\n")
					if input.WithOutput < len(lines) {
						info.Output = lines[input.WithOutput:]
					}
				} else {
					log.Printf("GET /task/%s/info: load logs: %v", input.UUID, err)
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

		var logBuilder strings.Builder
		if job.WriteS3Path != "" {
			err = a.workflowClient.GetWorkflowLogsWithArchiveFallback(ctx, input.UUID, &logBuilder)
			if err != nil {
				log.Printf("GET /task/%s/output: load logs: %v", input.UUID, err)
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

	// GET /task/{uuid}/assets - Discover task output assets
	huma.Register(a.api, huma.Operation{
		OperationID: "task-uuid-assets-get",
		Method:      http.MethodGet,
		Path:        "/task/{uuid}/assets",
		Summary:     "Lists primary and optional additional output assets",
		Tags:        []string{"task"},
	}, func(ctx context.Context, input *struct {
		UUID              string `path:"uuid" doc:"UUID of the task"`
		Token             string `query:"token" doc:"Authentication token (optional)"`
		IncludeAdditional bool   `query:"includeAdditional" default:"false" doc:"Include additional discovered files"`
		AdditionalLimit   int    `query:"additionalLimit" default:"100" doc:"Maximum number of additional files to return (clamped to 1000)"`
	}) (*TaskAssetsResponse, error) {
		log.Printf("GET /task/%s/assets: token_provided=%t includeAdditional=%t additionalLimit=%d", input.UUID, input.Token != "", input.IncludeAdditional, input.AdditionalLimit)

		job, err := a.metadataStore.GetJob(ctx, input.UUID)
		if err != nil {
			log.Printf("GET /task/%s/assets: failed to retrieve task metadata: %v", input.UUID, err)
			return nil, huma.NewError(500, "Failed to retrieve task metadata", err)
		}
		if job == nil {
			log.Printf("GET /task/%s/assets: task not found", input.UUID)
			return nil, huma.NewError(404, "Task not found")
		}
		if job.WriteS3Path == "" {
			log.Printf("GET /task/%s/assets: write S3 path not available", input.UUID)
			return nil, huma.NewError(400, "Write S3 path not available for this task")
		}

		s3Client, selectedEndpoint, clientErr := resolveTaskS3Client(job.Metadata)
		if clientErr != nil {
			log.Printf("GET /task/%s/assets: failed to initialize S3 client endpoint=%q: %v", input.UUID, selectedEndpoint, clientErr)
			return nil, huma.NewError(500, "Failed to initialize S3 client", clientErr)
		}

		response := &TaskAssetsResponse{}
		response.Body.Primary = make([]TaskAssetsPrimaryItem, 0, len(taskPrimaryAssetDefinitions))

		representedAssets := map[string]struct{}{}
		for _, definition := range taskPrimaryAssetDefinitions {
			item := TaskAssetsPrimaryItem{ID: definition.ID, Asset: definition.Assets[0], Exists: false}
			for _, candidate := range definition.Assets {
				exists, existsErr := s3.ObjectExistsInS3Path(ctx, s3Client, job.WriteS3Path, candidate)
				if existsErr != nil {
					log.Printf("GET /task/%s/assets: failed checking candidate asset=%q: %v", input.UUID, candidate, existsErr)
					return nil, huma.NewError(500, "Failed to query task assets", existsErr)
				}
				if exists {
					item.Asset = candidate
					item.Exists = true
					item.DownloadURL = taskAssetDownloadURL(input.UUID, candidate)
					representedAssets[candidate] = struct{}{}
					break
				}
			}
			response.Body.Primary = append(response.Body.Primary, item)
		}

		if input.IncludeAdditional {
			limit := clampTaskAdditionalLimit(input.AdditionalLimit)
			listedAssets, listErr := s3.ListFilesInS3PathWithLimit(ctx, s3Client, job.WriteS3Path, limit)
			if listErr != nil {
				log.Printf("GET /task/%s/assets: failed listing additional assets: %v", input.UUID, listErr)
				return nil, huma.NewError(500, "Failed to query task assets", listErr)
			}

			additional := make([]TaskAssetsAdditionalItem, 0, len(listedAssets))
			for _, asset := range listedAssets {
				if _, alreadyRepresented := representedAssets[asset]; alreadyRepresented {
					continue
				}
				additional = append(additional, TaskAssetsAdditionalItem{
					Asset:       asset,
					DownloadURL: taskAssetDownloadURL(input.UUID, asset),
				})
			}
			response.Body.Additional = additional
		}

		log.Printf("GET /task/%s/assets: returning primary=%d additional=%d includeAdditional=%t", input.UUID, len(response.Body.Primary), len(response.Body.Additional), input.IncludeAdditional)
		return response, nil
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
			return nil, huma.NewError(500, "Failed to retrieve task metadata", err)
		}
		if metadata == nil {
			return nil, huma.NewError(404, "Task not found")
		}

		// Parse new options if provided
		var odmFlags []string
		if input.Body.Options != "" {
			var options []TaskOption
			if err := json.Unmarshal([]byte(input.Body.Options), &options); err != nil {
				log.Printf("POST /task/restart: invalid options JSON for %q: %v", input.Body.UUID, err)
				return nil, huma.NewError(400, "Invalid options JSON", err)
			}
			for _, opt := range options {
				flag := fmt.Sprintf("--%s", opt.Name)
				if opt.Value != nil && opt.Value != false {
					if boolVal, ok := opt.Value.(bool); ok && boolVal {
						odmFlags = append(odmFlags, flag)
					} else {
						odmFlags = append(odmFlags, fmt.Sprintf("%s=%v", flag, opt.Value))
					}
				}
			}
		} else {
			if err := json.Unmarshal(metadata.ODMFlags, &odmFlags); err != nil {
				return nil, huma.NewError(500, "Failed to parse stored task options", err)
			}
		}
		for _, flag := range odmFlags {
			if err := validateShellSafe(flag, "options flag"); err != nil {
				return nil, huma.NewError(400, err.Error())
			}
		}

		s3Endpoint := normalizedEndpointFromMetadata(metadata.Metadata)
		if err := enforceEndpointAllowlist(s3Endpoint); err != nil {
			return nil, huma.NewError(400, "Invalid s3Endpoint", err)
		}
		log.Printf("POST /task/restart: endpoint selection endpoint=%q allowlist_enforced=%t", s3Endpoint, config.SCALEODM_ENFORCE_S3_ENDPOINT_ALLOWLIST)

		processingMode := metadataProcessingMode(metadata.Metadata)
		capacityType := metadataCapacityType(metadata.Metadata)
		userExcludes, _ := metadataExcludePaths(metadata.Metadata)
		useDefaultExcludes := metadataUseDefaultExcludes(metadata.Metadata)
		excludePatterns := workflows.ComposeExcludePatterns(useDefaultExcludes, userExcludes)
		s3ScanDepth, depthErr := workflows.ValidateS3ScanDepth(metadataS3ScanDepth(metadata.Metadata))
		if depthErr != nil {
			s3ScanDepth = workflows.DefaultS3ScanDepth
		}

		taskClient, taskClientErr := s3.GetS3ClientForEndpoint(config.AWS_S3_ENDPOINT)
		if s3Endpoint != "" {
			taskClient, taskClientErr = s3.GetS3ClientForEndpoint(s3Endpoint)
		}

		imageCount := metadataImageCount(metadata.Metadata)
		imageTotalBytes := metadataImageTotalBytes(metadata.Metadata)
		if imageCount == 0 || imageTotalBytes == 0 {
			if taskClientErr == nil {
				if counted, totalBytes, countErr := s3.CountImageStatsInS3PathWithExcludes(ctx, taskClient, metadata.ReadS3Path, excludePatterns); countErr == nil {
					if imageCount == 0 {
						imageCount = counted
					}
					if imageTotalBytes == 0 {
						imageTotalBytes = totalBytes
					}
				}
			}
		}

		wfConfig := workflows.NewDefaultODMConfig(
			metadata.ODMProjectID,
			metadata.ReadS3Path,
			metadata.WriteS3Path,
			odmFlags,
		)
		wfConfig.S3Region = metadata.S3Region
		wfConfig.S3Endpoint = s3Endpoint
		wfConfig.ImageCount = imageCount
		wfConfig.ImageTotalBytes = imageTotalBytes
		wfConfig.ProcessingMode = processingMode
		wfConfig.CapacityType = capacityType
		wfConfig.ExcludePaths = excludePatterns
		wfConfig.S3ScanDepth = s3ScanDepth

		wf, err := a.workflowClient.CreateODMWorkflow(ctx, wfConfig)
		if err != nil {
			log.Printf("POST /task/restart: failed to create new workflow for %q: %v", input.Body.UUID, err)
			return nil, huma.NewError(500, "Failed to restart task", err)
		}

		metadataPatch := map[string]interface{}{
			metadataImageCountKey:            imageCount,
			metadataImageTotalBytesKey:       imageTotalBytes,
			metadataWorkflowMissingFirstSeen: nil,
		}
		if s3Endpoint != "" {
			metadataPatch[metadataS3EndpointKey] = s3Endpoint
		}
		oldWorkflowName := input.Body.UUID
		if err := a.metadataStore.RestartJobMetadata(
			ctx,
			oldWorkflowName,
			wf.Name,
			metadata.ODMProjectID,
			metadata.ReadS3Path,
			metadata.WriteS3Path,
			odmFlags,
			metadata.S3Region,
			metadataPatch,
		); err != nil {
			log.Printf("POST /task/restart: failed to swap metadata for %q -> %q: %v", oldWorkflowName, wf.Name, err)
			if wf.Name != oldWorkflowName {
				if delErr := a.workflowClient.DeleteWorkflow(ctx, wf.Name); delErr != nil && !isNotFound(delErr) {
					log.Printf("POST /task/restart: failed cleanup delete of unmanaged workflow %q: %v", wf.Name, delErr)
				}
			}
			return nil, huma.NewError(500, "Failed to persist restarted task metadata", err)
		}

		if wf.Name != oldWorkflowName {
			if err := a.workflowClient.DeleteWorkflow(ctx, oldWorkflowName); err != nil && !isNotFound(err) {
				log.Printf("POST /task/restart: failed post-cutover cleanup delete of old workflow %q: %v", oldWorkflowName, err)
			}
		}

		log.Printf("POST /task/restart: task %q restarted as workflow %q imageCount=%d imageTotalBytes=%d endpoint=%q", oldWorkflowName, wf.Name, imageCount, imageTotalBytes, s3Endpoint)
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

		s3Client, selectedEndpoint, clientErr := resolveTaskS3Client(metadata.Metadata)
		if clientErr != nil {
			log.Printf("GET /task/%s/download/%s: failed to initialize S3 client endpoint=%q: %v", uuid, asset, selectedEndpoint, clientErr)
			http.Error(w, `{"error":"Failed to initialize S3 client"}`, http.StatusInternalServerError)
			return
		}

		requestedAsset := asset
		if resolvedAsset, isAlias, resolveErr := resolveAssetAliasCandidate(r.Context(), s3Client, metadata.WriteS3Path, requestedAsset); resolveErr != nil {
			log.Printf("GET /task/%s/download/%s: failed to resolve asset alias: %v", uuid, asset, resolveErr)
			http.Error(w, `{"error":"Failed to resolve asset alias"}`, http.StatusInternalServerError)
			return
		} else if isAlias {
			if resolvedAsset == "" {
				log.Printf("GET /task/%s/download/%s: alias requested but no asset found", uuid, asset)
				http.Error(w, fmt.Sprintf(`{"error":"File not found: %s"}`, asset), http.StatusNotFound)
				return
			}
			asset = resolvedAsset
		}

		assetExists, err := s3.ObjectExistsInS3Path(r.Context(), s3Client, metadata.WriteS3Path, asset)
		if err != nil {
			log.Printf("GET /task/%s/download/%s: failed to check asset existence: %v", uuid, requestedAsset, err)
			http.Error(w, `{"error":"Failed to query task asset"}`, http.StatusInternalServerError)
			return
		}
		if assetExists {
			presignedURL, err := s3.GeneratePresignedURL(r.Context(), s3Client, metadata.WriteS3Path, asset, 1*time.Hour)
			if err != nil {
				log.Printf("GET /task/%s/download/%s: failed to generate pre-signed URL: %v", uuid, requestedAsset, err)
				http.Error(w, fmt.Sprintf(`{"error":"File not found: %s"}`, requestedAsset), http.StatusNotFound)
				return
			}
			log.Printf("GET /task/%s/download/%s: redirecting to pre-signed URL for key=%q (expires in 1 hour)", uuid, requestedAsset, asset)
			http.Redirect(w, r, presignedURL, http.StatusFound)
			return
		}

		if requestedAsset == "all.zip" {
			var zipBuffer bytes.Buffer
			written, streamErr := s3.StreamS3PathAsZip(r.Context(), s3Client, metadata.WriteS3Path, &zipBuffer)
			if streamErr != nil {
				if streamErr == s3.ErrNoObjectsToZip {
					log.Printf("GET /task/%s/download/%s: no output objects found for synthetic all.zip", uuid, requestedAsset)
					http.Error(w, fmt.Sprintf(`{"error":"File not found: %s"}`, requestedAsset), http.StatusNotFound)
					return
				}
				log.Printf("GET /task/%s/download/%s: failed to stream synthetic all.zip: %v", uuid, requestedAsset, streamErr)
				http.Error(w, `{"error":"Failed to stream task output"}`, http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="all.zip"`)
			if _, writeErr := zipBuffer.WriteTo(w); writeErr != nil {
				log.Printf("GET /task/%s/download/%s: failed to write synthetic all.zip response: %v", uuid, requestedAsset, writeErr)
				return
			}
			log.Printf("GET /task/%s/download/%s: streamed synthetic all.zip with %d entries", uuid, requestedAsset, written)
			return
		}

		log.Printf("GET /task/%s/download/%s: asset not found", uuid, requestedAsset)
		http.Error(w, fmt.Sprintf(`{"error":"File not found: %s"}`, requestedAsset), http.StatusNotFound)
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
