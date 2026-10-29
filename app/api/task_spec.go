// Validation of task requests into a taskSpec, before any S3 or Argo calls.

package api

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/minio/minio-go/v7"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/workflows"
)

// taskSpec is everything needed to submit a task's workflow.
type taskSpec struct {
	projectID          string
	readPath           string
	writePath          string
	odmFlags           []string
	files              map[string]workflows.FileSource
	s3Region           string
	s3Endpoint         string // normalized per-task endpoint, "" for the server default
	processingMode     string
	capacityType       string
	odmImage           string
	userExcludes       []string
	useDefaultExcludes bool
	s3ScanDepth        int
}

// workflowS3Endpoint is used by both the API preflight and the workflow pods, so they agree.
func (s taskSpec) workflowS3Endpoint() string {
	return cmp.Or(s.s3Endpoint, config.AWS_S3_ENDPOINT)
}

func (s taskSpec) excludePatterns() []string {
	return workflows.ComposeExcludePatterns(s.useDefaultExcludes, s.userExcludes)
}

// S3 credentials reach workflow pods via Kubernetes Secret references
// (secretKeyRef), so there is no per-request credential handling.
func (s taskSpec) workflowConfig(imageCount int, imageTotalBytes int64) *workflows.ODMPipelineConfig {
	cfg := workflows.NewDefaultODMConfig(s.projectID, s.readPath, s.writePath, s.odmFlags)
	cfg.S3Region = s.s3Region
	cfg.S3Endpoint = s.workflowS3Endpoint()
	cfg.ImageCount = imageCount
	cfg.ImageTotalBytes = imageTotalBytes
	cfg.ProcessingMode = s.processingMode
	cfg.CapacityType = s.capacityType
	cfg.ODMImage = s.odmImage
	cfg.ExcludePaths = s.excludePatterns()
	cfg.S3ScanDepth = s.s3ScanDepth
	cfg.Boundary = s.files[workflows.BoundaryOptionName]
	cfg.Cameras = s.files[workflows.CamerasOptionName]
	return cfg
}

// newTaskMetadata is the metadata patch stored after /task/new creates a job.
func (s taskSpec) newTaskMetadata(imageCount int, imageTotalBytes int64, webhook string) map[string]any {
	patch := map[string]any{
		metadataImageCountKey:                    imageCount,
		metadataImageTotalBytesKey:               imageTotalBytes,
		meta.MetadataWorkflowMissingFirstSeenKey: nil,
		metadataProcessingModeKey:                s.processingMode,
		metadataCapacityTypeKey:                  s.capacityType,
		metadataExcludePathsKey:                  s.userExcludes,
		metadataUseDefaultExcludesKey:            s.useDefaultExcludes,
		metadataS3ScanDepthKey:                   s.s3ScanDepth,
	}
	if s.s3Endpoint != "" {
		patch[metadataS3EndpointKey] = s.s3Endpoint
	}
	// Only overrides are stored, so restarts follow later default image upgrades.
	if s.odmImage != config.SCALEODM_ODM_IMAGE {
		patch[metadataODMImageKey] = s.odmImage
	}
	// Lets the reconciler POST a terminal-status notification.
	if strings.TrimSpace(webhook) != "" {
		patch[meta.MetadataWebhookKey] = webhook
	}
	return patch
}

// rejection is a client-facing error plus a stable reason code for logs.
type rejection struct {
	reason string
	status int
	msg    string
	errs   []error
}

func reject(reason string, status int, msg string, errs ...error) *rejection {
	return &rejection{reason: reason, status: status, msg: msg, errs: errs}
}

func (r *rejection) humaError() error {
	return huma.NewError(r.status, r.msg, r.errs...)
}

func parseTaskNewRequest(req TaskNewRequest) (taskSpec, *rejection) {
	spec := taskSpec{
		projectID:          cmp.Or(req.Name, "odm-project"),
		s3Region:           req.S3Region,
		processingMode:     cmp.Or(req.ProcessingMode, workflows.ProcessingModeStandard),
		capacityType:       cmp.Or(req.CapacityType, config.SCALEODM_WORKFLOW_CAPACITY_TYPE),
		useDefaultExcludes: req.UseDefaultExcludes == nil || *req.UseDefaultExcludes,
	}

	// Reserved modes get 501 so clients can probe support.
	if workflows.IsReservedProcessingMode(spec.processingMode) {
		return taskSpec{}, reject("processing_mode_not_implemented", 501, fmt.Sprintf("processingMode %q is reserved for a future pipeline and is not yet implemented", spec.processingMode))
	}
	if !workflows.IsImplementedProcessingMode(spec.processingMode) {
		return taskSpec{}, reject("invalid_processing_mode", 400, fmt.Sprintf("invalid processingMode %q (supported: standard)", spec.processingMode))
	}
	if !workflows.IsValidCapacityType(spec.capacityType) {
		return taskSpec{}, reject("invalid_capacity_type", 400, fmt.Sprintf("invalid capacityType %q (supported: spot, on-demand)", spec.capacityType))
	}

	var err error
	if spec.odmImage, err = resolveODMImage(req.OdmImage); err != nil {
		return taskSpec{}, reject("invalid_odm_image", 400, err.Error())
	}
	requestedDepth := 0
	if req.S3ScanDepth != nil {
		requestedDepth = *req.S3ScanDepth
	}
	if spec.s3ScanDepth, err = workflows.ValidateS3ScanDepth(requestedDepth); err != nil {
		return taskSpec{}, reject("invalid_s3_scan_depth", 400, err.Error())
	}

	var rej *rejection
	if spec.userExcludes, rej = parseExcludePaths(req.ExcludePaths); rej != nil {
		return taskSpec{}, rej
	}
	if spec.odmFlags, spec.files, rej = parseOptions(req.Options); rej != nil {
		return taskSpec{}, rej
	}
	if spec.readPath, spec.writePath, rej = taskPaths(req); rej != nil {
		return taskSpec{}, rej
	}

	// Validate all values that will be embedded in shell scripts
	if err := validateShellSafe(spec.projectID, "name"); err != nil {
		return taskSpec{}, reject("invalid_project_name", 400, err.Error())
	}
	if err := validateShellSafeFlags(spec.odmFlags); err != nil {
		return taskSpec{}, reject("invalid_option_flag", 400, err.Error())
	}
	if err := validateShellSafe(spec.readPath, "readS3Path"); err != nil {
		return taskSpec{}, reject("invalid_read_path", 400, err.Error())
	}
	if err := validateShellSafe(spec.writePath, "writeS3Path"); err != nil {
		return taskSpec{}, reject("invalid_write_path", 400, err.Error())
	}

	if spec.s3Endpoint, err = normalizeOptionalS3Endpoint(req.S3Endpoint); err != nil {
		return taskSpec{}, reject("invalid_s3_endpoint", 400, "Invalid s3Endpoint", err)
	}
	if err := enforceEndpointAllowlist(spec.s3Endpoint); err != nil {
		return taskSpec{}, reject("invalid_s3_endpoint", 400, "Invalid s3Endpoint", err)
	}
	return spec, nil
}

// restartSpec rebuilds a stored task's spec, applying new options if given.
func restartSpec(job *meta.JobMetadata, metadata taskMetadata, newOptions string) (taskSpec, *rejection) {
	spec := taskSpec{
		projectID:          job.ODMProjectID,
		readPath:           job.ReadS3Path,
		writePath:          job.WriteS3Path,
		s3Region:           job.S3Region,
		s3Endpoint:         metadata.s3Endpoint(),
		processingMode:     metadata.processingMode(),
		capacityType:       metadata.capacityType(),
		userExcludes:       metadata.ExcludePaths,
		useDefaultExcludes: metadata.useDefaultExcludes(),
	}

	var rej *rejection
	if newOptions != "" {
		if spec.odmFlags, spec.files, rej = parseOptions(newOptions); rej != nil {
			return taskSpec{}, rej
		}
	} else {
		if err := json.Unmarshal(job.ODMFlags, &spec.odmFlags); err != nil {
			return taskSpec{}, reject("invalid_stored_options", 500, "Failed to parse stored task options", err)
		}
		spec.files = metadata.workspaceFiles()
	}
	if err := validateShellSafeFlags(spec.odmFlags); err != nil {
		return taskSpec{}, reject("invalid_option_flag", 400, err.Error())
	}
	if err := enforceEndpointAllowlist(spec.s3Endpoint); err != nil {
		return taskSpec{}, reject("invalid_s3_endpoint", 400, "Invalid s3Endpoint", err)
	}

	// Re-check the stored image: the allowlist may have changed since submit.
	var err error
	if spec.odmImage, err = resolveODMImage(metadata.ODMImage); err != nil {
		return taskSpec{}, reject("invalid_odm_image", 400, err.Error())
	}
	if spec.s3ScanDepth, err = workflows.ValidateS3ScanDepth(metadata.s3ScanDepth()); err != nil {
		spec.s3ScanDepth = workflows.DefaultS3ScanDepth
	}
	return spec, nil
}

func parseExcludePaths(raw string) ([]string, *rejection) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var patterns []string
	if err := json.Unmarshal([]byte(raw), &patterns); err != nil {
		return nil, reject("invalid_exclude_paths", 400, "Invalid excludePaths JSON (expected an array of strings)", err)
	}
	for _, p := range patterns {
		if err := workflows.ValidateExcludePattern(p); err != nil {
			return nil, reject("invalid_exclude_pattern", 400, fmt.Sprintf("invalid exclude pattern %q: %s", p, err.Error()))
		}
	}
	return patterns, nil
}

// parseOptions decodes the NodeODM options JSON into ODM flags and workspace files.
func parseOptions(raw string) ([]string, map[string]workflows.FileSource, *rejection) {
	if raw == "" {
		return nil, map[string]workflows.FileSource{}, nil
	}
	var options []TaskOption
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil, nil, reject("invalid_options", 400, "Invalid options JSON", err)
	}
	odmFlags, files, err := odmFlagsFromOptions(options)
	if err != nil {
		return nil, nil, reject("invalid_options", 400, err.Error())
	}
	return odmFlags, files, nil
}

// taskPaths resolves read/write prefixes from readS3Path or the legacy zipurl.
func taskPaths(req TaskNewRequest) (readPath, writePath string, rej *rejection) {
	switch {
	case req.ReadS3Path != "":
		readPath = withTrailingSlash(req.ReadS3Path)
		writePath = withTrailingSlash(cmp.Or(req.WriteS3Path, strings.TrimSuffix(req.ReadS3Path, "/")+"/output"))
	case strings.HasPrefix(req.ZipURL, "s3://"):
		readPath = withTrailingSlash(req.ZipURL)
		writePath = strings.TrimSuffix(req.ZipURL, "/") + "-output/"
	case strings.HasPrefix(req.ZipURL, "http://") || strings.HasPrefix(req.ZipURL, "https://"):
		return "", "", reject("http_zip_not_supported", 400, "HTTP zip URLs not supported. Use readS3Path for S3-based processing")
	case req.ZipURL != "":
		return "", "", reject("invalid_zipurl", 400, "zipurl must be an s3://... prefix or a http(s) zip URL")
	default:
		return "", "", reject("missing_read_path", 400, "readS3Path is required (or zipurl for legacy support)")
	}
	if !strings.HasPrefix(readPath, "s3://") {
		return "", "", reject("invalid_read_path", 400, "readS3Path must be an s3:// path")
	}
	if !strings.HasPrefix(writePath, "s3://") {
		return "", "", reject("invalid_write_path", 400, "writeS3Path must be an s3:// path")
	}
	return readPath, writePath, nil
}

// validateShellSafe checks that a string is safe to embed in a shell script.
func validateShellSafe(value, fieldName string) error {
	if !workflows.ShellSafePattern.MatchString(value) {
		return fmt.Errorf("%s contains invalid characters: only alphanumerics, hyphens, underscores, dots, slashes, colons, equals, and @ are allowed", fieldName)
	}
	return nil
}

func validateShellSafeFlags(odmFlags []string) error {
	for _, flag := range odmFlags {
		if err := validateShellSafe(flag, "options flag"); err != nil {
			return err
		}
	}
	return nil
}

func withTrailingSlash(path string) string {
	return strings.TrimSuffix(path, "/") + "/"
}

// Options that accept workspace documents.
var workspaceFileOptions = map[string]func(string) (workflows.FileSource, string, error){
	workflows.BoundaryOptionName: workflows.ParseBoundaryValue,
	workflows.CamerasOptionName:  workflows.ParseCamerasValue,
}

// odmFlagsFromOptions separates ODM flags from workspace files.
func odmFlagsFromOptions(options []TaskOption) ([]string, map[string]workflows.FileSource, error) {
	var odmFlags []string
	files := map[string]workflows.FileSource{}

	for _, opt := range options {
		if opt.Value == nil {
			continue
		}
		// Skip options explicitly set to false.
		if boolVal, ok := opt.Value.(bool); ok && !boolVal {
			continue
		}

		if parse, ok := workspaceFileOptions[opt.Name]; ok {
			source, passthrough, err := parse(documentOptionString(opt.Value))
			if err != nil {
				return nil, nil, err
			}
			if source.IsSet() {
				files[opt.Name] = source
				continue
			}
			odmFlags = append(odmFlags, fmt.Sprintf("--%s=%s", opt.Name, passthrough))
			continue
		}

		flag := fmt.Sprintf("--%s", opt.Name)
		if boolVal, ok := opt.Value.(bool); ok && boolVal {
			odmFlags = append(odmFlags, flag)
		} else {
			odmFlags = append(odmFlags, fmt.Sprintf("%s=%v", flag, opt.Value))
		}
	}

	return odmFlags, files, nil
}

// documentOptionString preserves nested JSON objects as text.
func documentOptionString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any, []any:
		if encoded, err := json.Marshal(v); err == nil {
			return string(encoded)
		}
	}
	return fmt.Sprintf("%v", value)
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

// Drops any tag or digest and qualifies bare names with docker.io.
func odmImageRepo(image string) string {
	image = strings.TrimSpace(image)
	if at := strings.Index(image, "@"); at != -1 {
		image = image[:at]
	}
	// Only a colon after the final slash is a tag; an earlier one is a registry port.
	lastSlash := strings.LastIndex(image, "/")
	if colon := strings.Index(image[lastSlash+1:], ":"); colon != -1 {
		image = image[:lastSlash+1+colon]
	}
	host, _, hasHost := strings.Cut(image, "/")
	if !hasHost || (!strings.ContainsAny(host, ".:") && host != "localhost") {
		image = "docker.io/" + image
	}
	return image
}

func resolveODMImage(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == config.SCALEODM_ODM_IMAGE {
		return config.SCALEODM_ODM_IMAGE, nil
	}
	repo := odmImageRepo(requested)
	for _, allowed := range strings.Split(config.SCALEODM_ALLOWED_ODM_IMAGES, ",") {
		if strings.TrimSpace(allowed) != "" && odmImageRepo(allowed) == repo {
			return requested, nil
		}
	}
	return "", fmt.Errorf("odmImage %q is not in SCALEODM_ALLOWED_ODM_IMAGES", requested)
}

func sentenceCase(name string) string {
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func hasS3WorkspaceFile(files map[string]workflows.FileSource) bool {
	for _, source := range files {
		if source.S3Path != "" {
			return true
		}
	}
	return false
}

// preflightWorkspaceFiles rejects invalid S3 documents before workflow submission.
// ODM can silently ignore invalid camera calibration.
func preflightWorkspaceFiles(ctx context.Context, client *minio.Client, files map[string]workflows.FileSource) (string, error) {
	for _, option := range []string{workflows.BoundaryOptionName, workflows.CamerasOptionName} {
		source, ok := files[option]
		if !ok || source.S3Path == "" {
			continue
		}
		data, err := s3.ReadObjectAtURL(ctx, client, source.S3Path, workflows.MaxRemoteDocumentBytes)
		switch {
		case err != nil:
			return option, fmt.Errorf("Unable to read %s from %s: %w", option, source.S3Path, err)
		case len(data) == 0:
			return option, fmt.Errorf("%s object is empty: %s", sentenceCase(option), source.S3Path)
		}
		if err := workflows.ValidateDocument(option, data); err != nil {
			return option, fmt.Errorf("Invalid %s at %s: %w", option, source.S3Path, err)
		}
	}
	return "", nil
}
