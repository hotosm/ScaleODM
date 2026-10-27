// Request and response bodies for the NodeODM-compatible API.

package api

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

	// NodeODM's 'outputs' param (override all.zip contents) is intentionally
	// not implemented, to keep things simple.

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
	// S3 region. Huma fills the default whenever it is omitted or empty.
	S3Region string `json:"s3Region,omitempty" form:"s3Region" default:"us-east-1" doc:"S3 region (default: us-east-1)"`

	// Optional override for creation timestamp. If omitted, the server uses the current
	// time when the job is created.
	DateCreated int64 `json:"dateCreated,omitempty" form:"dateCreated" doc:"Override creation timestamp (optional; defaults to current time when omitted)"`

	// ProcessingMode selects the pipeline shape. Pick the mode that matches
	// the post-processing needs; the depth/scope of the input scan is
	// configured separately via S3ScanDepth.
	//
	// Supported today:
	//   - "standard" (default): the regular ODM pipeline (download -> process
	//     -> upload). Imagery under readS3Path is gathered into a single ODM
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

	OdmImage string `json:"odmImage,omitempty" form:"odmImage" doc:"Alternative ODM image for this task (must be allowlisted; defaults to the configured image)."`

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

// SuccessResponse returns 200 + JSON; a bodyless Huma response is a 204, which pyodm rejects.
type SuccessResponse struct {
	Body Response
}

func successResponse() *SuccessResponse {
	return &SuccessResponse{Body: Response{Success: true}}
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
