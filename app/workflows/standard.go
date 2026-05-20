package workflows

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/observability"
	"github.com/hotosm/scaleodm/app/s3"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ResourceSpec defines CPU, memory, and ephemeral storage values.
type ResourceSpec struct {
	CPU              string
	Memory           string
	EphemeralStorage string
}

// ContainerResources defines request/limit resources for a workflow container.
type ContainerResources struct {
	Requests ResourceSpec
	Limits   ResourceSpec
}

// RetryConfig defines Argo retry behavior.
type RetryConfig struct {
	Limit              int32
	BackoffDuration    string
	BackoffFactor      string
	BackoffMaxDuration string
}

// WorkflowRuntimeGuardrails defines workflow-level runtime controls.
type WorkflowRuntimeGuardrails struct {
	ActiveDeadlineSeconds  int64
	TTLSuccessSeconds      int32
	TTLFailureSeconds      int32
	PodGCStrategy          string
	PodGCDeleteDelaySecond *int64
	Retry                  RetryConfig
}

type WorkspaceConfig struct {
	Mode         string
	Size         string
	StorageClass string
	AccessMode   string
}

// ODMPipelineConfig holds configuration for ODM pipeline workflow
type ODMPipelineConfig struct {
	ODMProjectID   string
	ReadS3Path     string   // S3 path where raw imagery is located (can contain zips)
	WriteS3Path    string   // S3 path where final ODM outputs will be written
	ODMFlags       []string // ODM command line flags
	S3Region       string
	S3Endpoint     string // Optional custom S3 endpoint for non-AWS providers
	ServiceAccount string
	RcloneImage    string
	ODMImage       string

	// ProcessingMode selects the pipeline shape; see processing_mode.go.
	// Empty string is treated as ProcessingModeStandard.
	ProcessingMode string
	// CapacityType controls node selection: "spot" (default) or "on-demand".
	// See processing_mode.go for constants.
	CapacityType string
	// ExcludePaths is the rclone-style filter pattern list used by the
	// download stage. Already composed (defaults + user) by the API layer.
	ExcludePaths []string
	// S3ScanDepth caps how deep rclone walks beneath ReadS3Path during the
	// download stage. Defaults to DefaultS3ScanDepth (1) - i.e. only files
	// directly under the given path. Values > 1 let callers point at a
	// higher-level project root and pick up imagery in nested task subdirs.
	S3ScanDepth int

	RuntimeGuardrails WorkflowRuntimeGuardrails
	Workspace         WorkspaceConfig
	DownloadResources ContainerResources
	ProcessResources  ContainerResources
	UploadResources   ContainerResources
	CleanupResources  ContainerResources

	ImageCount      int
	ImageTotalBytes int64
}

type interpolationPoint struct {
	images int
	ramGiB float64
}

var odmMemoryEstimationPoints = []interpolationPoint{
	{images: 40, ramGiB: 4},
	{images: 250, ramGiB: 16},
	{images: 500, ramGiB: 32},
	{images: 1500, ramGiB: 64},
	{images: 2500, ramGiB: 128},
	{images: 3500, ramGiB: 192},
	{images: 5000, ramGiB: 256},
}

// NewDefaultODMConfig returns default configuration
func NewDefaultODMConfig(odmProjectID, readS3Path, writeS3Path string, odmFlags []string) *ODMPipelineConfig {
	podGCDelaySeconds := int64(config.SCALEODM_WORKFLOW_POD_GC_DELETE_DELAY_SECONDS)
	return &ODMPipelineConfig{
		ODMProjectID:   odmProjectID,
		ReadS3Path:     readS3Path,
		WriteS3Path:    writeS3Path,
		ODMFlags:       odmFlags,
		ProcessingMode: ProcessingModeStandard,
		CapacityType:   config.SCALEODM_WORKFLOW_CAPACITY_TYPE,
		S3ScanDepth:    DefaultS3ScanDepth,
		S3Region:       "us-east-1",
		S3Endpoint:     "",
		ServiceAccount: "argo-odm",
		RcloneImage:    "docker.io/rclone/rclone:1.69",
		ODMImage:       config.SCALEODM_ODM_IMAGE,
		RuntimeGuardrails: WorkflowRuntimeGuardrails{
			ActiveDeadlineSeconds:  int64(config.SCALEODM_WORKFLOW_ACTIVE_DEADLINE_SECONDS),
			TTLSuccessSeconds:      int32(config.SCALEODM_WORKFLOW_TTL_SUCCESS_SECONDS),
			TTLFailureSeconds:      int32(config.SCALEODM_WORKFLOW_TTL_FAILURE_SECONDS),
			PodGCStrategy:          config.SCALEODM_WORKFLOW_POD_GC_STRATEGY,
			PodGCDeleteDelaySecond: &podGCDelaySeconds,
			Retry: RetryConfig{
				Limit:              int32(config.SCALEODM_WORKFLOW_RETRY_LIMIT),
				BackoffDuration:    config.SCALEODM_WORKFLOW_RETRY_BACKOFF_DURATION,
				BackoffFactor:      config.SCALEODM_WORKFLOW_RETRY_BACKOFF_FACTOR,
				BackoffMaxDuration: config.SCALEODM_WORKFLOW_RETRY_BACKOFF_MAX_DURATION,
			},
		},
		Workspace: WorkspaceConfig{
			Mode:         config.SCALEODM_WORKFLOW_WORKSPACE_MODE,
			Size:         config.SCALEODM_WORKFLOW_WORKSPACE_SIZE,
			StorageClass: config.SCALEODM_WORKFLOW_WORKSPACE_STORAGE_CLASS,
			AccessMode:   config.SCALEODM_WORKFLOW_WORKSPACE_ACCESS_MODE,
		},
		DownloadResources: ContainerResources{
			Requests: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_DOWNLOAD_REQUEST_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_DOWNLOAD_REQUEST_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_DOWNLOAD_REQUEST_EPHEMERAL_STORAGE,
			},
			Limits: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_DOWNLOAD_LIMIT_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_DOWNLOAD_LIMIT_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_DOWNLOAD_LIMIT_EPHEMERAL_STORAGE,
			},
		},
		ProcessResources: ContainerResources{
			Requests: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_PROCESS_REQUEST_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_PROCESS_REQUEST_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_PROCESS_REQUEST_EPHEMERAL_STORAGE,
			},
			Limits: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_PROCESS_LIMIT_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_PROCESS_LIMIT_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_PROCESS_LIMIT_EPHEMERAL_STORAGE,
			},
		},
		UploadResources: ContainerResources{
			Requests: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_UPLOAD_REQUEST_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_UPLOAD_REQUEST_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_UPLOAD_REQUEST_EPHEMERAL_STORAGE,
			},
			Limits: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_UPLOAD_LIMIT_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_UPLOAD_LIMIT_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_UPLOAD_LIMIT_EPHEMERAL_STORAGE,
			},
		},
		CleanupResources: ContainerResources{
			Requests: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_CLEANUP_REQUEST_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_CLEANUP_REQUEST_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_CLEANUP_REQUEST_EPHEMERAL_STORAGE,
			},
			Limits: ResourceSpec{
				CPU:              config.SCALEODM_WORKFLOW_RESOURCES_CLEANUP_LIMIT_CPU,
				Memory:           config.SCALEODM_WORKFLOW_RESOURCES_CLEANUP_LIMIT_MEMORY,
				EphemeralStorage: config.SCALEODM_WORKFLOW_RESOURCES_CLEANUP_LIMIT_EPHEMERAL_STORAGE,
			},
		},
	}
}

// CreateODMWorkflow creates and submits an ODM processing workflow
func (c *Client) CreateODMWorkflow(ctx context.Context, cfg *ODMPipelineConfig) (*wfv1.Workflow, error) {
	if cfg.S3Endpoint != "" {
		normalizedEndpoint, err := s3.NormalizeEndpoint(cfg.S3Endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid s3 endpoint: %w", err)
		}
		cfg.S3Endpoint = normalizedEndpoint
	}

	if cfg.ImageCount > 0 {
		cfg.ProcessResources = estimateProcessResourcesFromImageCount(cfg.ImageCount, cfg.ODMFlags, cfg.ProcessResources)
	}

	applyDynamicWorkspaceSize(cfg)

	wf := c.buildODMWorkflow(cfg)

	createStart := time.Now()
	created, err := c.wfClientset.ArgoprojV1alpha1().Workflows(c.namespace).Create(
		ctx,
		wf,
		metav1.CreateOptions{},
	)
	if err != nil {
		observability.RecordWorkflowCreate("failure", "argo_create_failed", time.Since(createStart))
		return nil, fmt.Errorf("failed to create workflow: %w", err)
	}
	observability.RecordWorkflowCreate("success", "none", time.Since(createStart))

	return created, nil
}

// flagMemoryMultiplier returns a scaling factor for the RAM estimate based on
// which ODM flags are active. --fast-orthophoto skips dense reconstruction
// (the most memory-intensive step), while --dsm/--dtm require it and then
// add surface-model generation on top.
func flagMemoryMultiplier(odmFlags []string) float64 {
	for _, f := range odmFlags {
		if f == "--fast-orthophoto" {
			return config.SCALEODM_PROCESS_FAST_ORTHO_MEMORY_MULTIPLIER
		}
	}
	for _, f := range odmFlags {
		if f == "--dsm" || f == "--dtm" {
			return config.SCALEODM_PROCESS_DSM_DTM_MEMORY_MULTIPLIER
		}
	}
	return 1.0
}

func estimateProcessResourcesFromImageCount(imageCount int, odmFlags []string, fallback ContainerResources) ContainerResources {
	baseRAMGiB := estimateMemoryGiB(imageCount)
	estimatedRAMGiB := clamp(baseRAMGiB*flagMemoryMultiplier(odmFlags), config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
	marginMultiplier := 1 + (config.SCALEODM_PROCESS_MEMORY_LIMIT_MARGIN_PERCENT / 100)
	if marginMultiplier < 1 {
		marginMultiplier = 1
	}

	memoryLimitGiB := estimatedRAMGiB * marginMultiplier
	cpuRequestCores := math.Max(1, estimatedRAMGiB*config.SCALEODM_PROCESS_CPU_PER_GIB)
	cpuLimitCores := math.Max(cpuRequestCores, cpuRequestCores*config.SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER)
	ephemeralRequestGiB := math.Max(10, estimatedRAMGiB*config.SCALEODM_PROCESS_EPHEMERAL_GIB_PER_GIB_RAM)
	ephemeralLimitGiB := math.Max(ephemeralRequestGiB, ephemeralRequestGiB*config.SCALEODM_PROCESS_EPHEMERAL_LIMIT_MULTIPLIER)

	estimated := ContainerResources{
		Requests: ResourceSpec{
			CPU:              formatCPU(cpuRequestCores),
			Memory:           formatGiBAsMi(estimatedRAMGiB),
			EphemeralStorage: formatGiBAsMi(ephemeralRequestGiB),
		},
		Limits: ResourceSpec{
			CPU:              formatCPU(cpuLimitCores),
			Memory:           formatGiBAsMi(memoryLimitGiB),
			EphemeralStorage: formatGiBAsMi(ephemeralLimitGiB),
		},
	}

	if estimated.Requests.CPU == "" || estimated.Requests.Memory == "" || estimated.Requests.EphemeralStorage == "" {
		return fallback
	}
	return estimated
}

func estimateMemoryGiB(imageCount int) float64 {
	if imageCount <= 0 {
		return clamp(config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
	}

	if imageCount <= odmMemoryEstimationPoints[0].images {
		return clamp(odmMemoryEstimationPoints[0].ramGiB, config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
	}
	last := odmMemoryEstimationPoints[len(odmMemoryEstimationPoints)-1]
	if imageCount >= last.images {
		return clamp(last.ramGiB, config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
	}

	for i := 1; i < len(odmMemoryEstimationPoints); i++ {
		left := odmMemoryEstimationPoints[i-1]
		right := odmMemoryEstimationPoints[i]
		if imageCount <= right.images {
			ratio := float64(imageCount-left.images) / float64(right.images-left.images)
			interpolated := left.ramGiB + ratio*(right.ramGiB-left.ramGiB)
			return clamp(interpolated, config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
		}
	}

	return clamp(last.ramGiB, config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
}

func clamp(v, minV, maxV float64) float64 {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func applyDynamicWorkspaceSize(cfg *ODMPipelineConfig) {
	if !config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED || !shouldUseWorkspacePVC(cfg.Workspace) {
		return
	}
	if estimatedSize, ok := estimateWorkspacePVCSize(cfg.ImageTotalBytes, cfg.ImageCount, cfg.ODMFlags); ok {
		cfg.Workspace.Size = estimatedSize
	}
}

func estimateWorkspacePVCSize(imageTotalBytes int64, imageCount int, odmFlags []string) (string, bool) {
	estimatedGiB := estimateWorkspaceGiB(imageTotalBytes, imageCount, odmFlags)
	if estimatedGiB <= 0 || math.IsNaN(estimatedGiB) || math.IsInf(estimatedGiB, 0) {
		return "", false
	}
	return fmt.Sprintf("%dGi", int64(math.Ceil(estimatedGiB))), true
}

// flagWorkspaceProfile returns the (multiplier, minGiB) pair for workspace sizing.
// --fast-orthophoto skips dense reconstruction and has a much smaller disk footprint;
// everything else (including DSM/DTM) uses the standard profile.
func flagWorkspaceProfile(odmFlags []string) (multiplier, minGiB float64) {
	for _, f := range odmFlags {
		if f == "--fast-orthophoto" {
			return config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER,
				config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB
		}
	}
	return config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER,
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
}

func estimateWorkspaceGiB(imageTotalBytes int64, imageCount int, odmFlags []string) float64 {
	multiplier, minGiB := flagWorkspaceProfile(odmFlags)
	maxGiB := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	if multiplier <= 0 || maxGiB <= 0 || maxGiB < minGiB {
		return 0
	}

	bytesEstimate := float64(imageTotalBytes)
	if bytesEstimate <= 0 && imageCount > 0 {
		fallbackMBPerImage := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
		if fallbackMBPerImage <= 0 {
			return 0
		}
		bytesEstimate = float64(imageCount) * fallbackMBPerImage * 1024 * 1024
	}
	if bytesEstimate <= 0 {
		return 0
	}

	gibEstimate := (bytesEstimate / (1024 * 1024 * 1024)) * multiplier
	gibEstimate = clamp(gibEstimate, minGiB, maxGiB)
	return gibEstimate
}

func formatCPU(cores float64) string {
	if cores <= 0 {
		return "1000m"
	}
	milli := int64(math.Ceil(cores * 1000))
	if milli%1000 == 0 {
		return fmt.Sprintf("%d", milli/1000)
	}
	return fmt.Sprintf("%dm", milli)
}

func formatGiBAsMi(gib float64) string {
	if gib <= 0 {
		gib = 1
	}
	mi := int64(math.Ceil(gib * 1024))
	return fmt.Sprintf("%dMi", mi)
}

func resourceList(spec ResourceSpec) apiv1.ResourceList {
	resources := apiv1.ResourceList{}
	if spec.CPU != "" {
		resources[apiv1.ResourceCPU] = resource.MustParse(spec.CPU)
	}
	if spec.Memory != "" {
		resources[apiv1.ResourceMemory] = resource.MustParse(spec.Memory)
	}
	if spec.EphemeralStorage != "" {
		resources[apiv1.ResourceEphemeralStorage] = resource.MustParse(spec.EphemeralStorage)
	}
	return resources
}

func containerRequirements(resources ContainerResources) apiv1.ResourceRequirements {
	return apiv1.ResourceRequirements{
		Requests: resourceList(resources.Requests),
		Limits:   resourceList(resources.Limits),
	}
}

func workflowPodSecurityContext() *apiv1.PodSecurityContext {
	runAsNonRoot := true
	fsGroup := int64(1000)
	return &apiv1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
		FSGroup:      &fsGroup,
		SeccompProfile: &apiv1.SeccompProfile{
			Type: apiv1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

func workflowContainerSecurityContext() *apiv1.SecurityContext {
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := true
	runAsNonRoot := true
	runAsUser := int64(1000)
	runAsGroup := int64(1000)
	return &apiv1.SecurityContext{
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
		RunAsNonRoot:             &runAsNonRoot,
		RunAsUser:                &runAsUser,
		RunAsGroup:               &runAsGroup,
		SeccompProfile: &apiv1.SeccompProfile{
			Type: apiv1.SeccompProfileTypeRuntimeDefault,
		},
		Capabilities: &apiv1.Capabilities{
			Drop: []apiv1.Capability{"ALL"},
		},
	}
}

// s3SecretEnvVars returns env vars that reference credentials from the unified
// runtime Kubernetes Secret via secretKeyRef. This keeps credentials out of the
// Argo Workflow spec and resolves them only at pod runtime.
func s3SecretEnvVars(cfg *ODMPipelineConfig) []apiv1.EnvVar {
	secretName := config.AWS_S3_SECRET_NAME
	region := cfg.S3Region
	if region == "" {
		region = "us-east-1"
	}

	envVars := []apiv1.EnvVar{
		{
			Name:  "TMPDIR",
			Value: "/tmp",
		},
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &apiv1.EnvVarSource{
				SecretKeyRef: &apiv1.SecretKeySelector{
					LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_ACCESS_KEY_ID",
				},
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &apiv1.EnvVarSource{
				SecretKeyRef: &apiv1.SecretKeySelector{
					LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_SECRET_ACCESS_KEY",
				},
			},
		},
		{
			Name:  "AWS_DEFAULT_REGION",
			Value: region,
		},
		{
			Name:  "AWS_REGION",
			Value: region,
		},
	}

	// If a custom S3 endpoint is specified (e.g., for MinIO), expose it as an env var
	if cfg.S3Endpoint != "" {
		envVars = append(envVars, apiv1.EnvVar{
			Name:  "AWS_S3_ENDPOINT",
			Value: cfg.S3Endpoint,
		})
	}

	return envVars
}

func toRetryStrategy(cfg RetryConfig) *wfv1.RetryStrategy {
	limit := intstr.FromInt32(cfg.Limit)
	factorInt := 2
	if strings.TrimSpace(cfg.BackoffFactor) != "" {
		parsedFactor := intstr.Parse(cfg.BackoffFactor)
		factorInt = (&parsedFactor).IntValue()
		if factorInt <= 0 {
			factorInt = 2
		}
	}
	factor := intstr.FromInt(factorInt)

	return &wfv1.RetryStrategy{
		Limit: &limit,
		Backoff: &wfv1.Backoff{
			Duration:    cfg.BackoffDuration,
			Factor:      &factor,
			MaxDuration: cfg.BackoffMaxDuration,
		},
	}
}

func toPodGC(strategy string, deleteDelay *int64) *wfv1.PodGC {
	podGC := &wfv1.PodGC{}
	switch strategy {
	case "OnPodCompletion":
		podGC.Strategy = wfv1.PodGCOnPodCompletion
	case "OnPodSuccess":
		podGC.Strategy = wfv1.PodGCOnPodSuccess
	case "OnWorkflowCompletion":
		podGC.Strategy = wfv1.PodGCOnWorkflowCompletion
	default:
		podGC.Strategy = wfv1.PodGCOnWorkflowSuccess
	}
	if deleteDelay != nil && *deleteDelay > 0 {
		podGC.DeleteDelayDuration = fmt.Sprintf("%ds", *deleteDelay)
	}
	return podGC
}

func parseWorkspaceAccessMode(mode string) apiv1.PersistentVolumeAccessMode {
	normalized := strings.TrimSpace(mode)
	switch normalized {
	case string(apiv1.ReadOnlyMany):
		return apiv1.ReadOnlyMany
	case string(apiv1.ReadWriteMany):
		return apiv1.ReadWriteMany
	case string(apiv1.ReadWriteOncePod):
		return apiv1.ReadWriteOncePod
	case string(apiv1.ReadWriteOnce):
		fallthrough
	default:
		return apiv1.ReadWriteOnce
	}
}

func shouldUseWorkspacePVC(workspace WorkspaceConfig) bool {
	mode := strings.ToLower(strings.TrimSpace(workspace.Mode))
	hasStorageClass := strings.TrimSpace(workspace.StorageClass) != ""
	switch mode {
	case "pvc":
		return true
	case "emptydir":
		return false
	case "auto", "":
		return hasStorageClass
	default:
		return hasStorageClass
	}
}

// buildODMWorkflow constructs the workflow specification
func (c *Client) buildODMWorkflow(cfg *ODMPipelineConfig) *wfv1.Workflow {
	awsEnv := s3SecretEnvVars(cfg)

	// Generate unique job ID for this workflow instance
	jobID := "{{workflow.name}}"

	// Download container - downloads from readS3Path and extracts zips
	// Uses include filters to only download image files and archives
	// Logs are written to shared workspace for later collection
	downloadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:            "download",
			Image:           cfg.RcloneImage,
			Command:         []string{"/bin/sh", "-c"},
			Args:            []string{s3.GenerateDownloadScript(jobID, cfg.ReadS3Path, cfg.ExcludePaths, cfg.S3ScanDepth) + " 2>&1 | tee /workspace/{{workflow.name}}/.download.log"},
			Env:             awsEnv,
			Resources:       containerRequirements(cfg.DownloadResources),
			SecurityContext: workflowContainerSecurityContext(),
		},
	}

	// ODM processing container
	// Logs are written to shared workspace for later collection
	odmFlagsStr := strings.Join(cfg.ODMFlags, " ")
	odmContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:            "process",
			Image:           cfg.ODMImage,
			Command:         []string{"/bin/bash", "-c"},
			Resources:       containerRequirements(cfg.ProcessResources),
			SecurityContext: workflowContainerSecurityContext(),
			Env: []apiv1.EnvVar{
				{
					Name:  "TMPDIR",
					Value: "/tmp",
				},
			},
			Args: []string{
				fmt.Sprintf(`
set -e
set -o pipefail
JOB_ID="{{workflow.name}}"
LOG_FILE="/workspace/$JOB_ID/.process.log"
echo "Running ODM processing..." | tee -a "$LOG_FILE"
echo "Processing job: $JOB_ID" | tee -a "$LOG_FILE"
echo "ODM Project ID: %s" | tee -a "$LOG_FILE"
odm_args="%s --project-path /workspace $JOB_ID"
echo "Executing: python3 run.py $odm_args" | tee -a "$LOG_FILE"
python3 run.py $odm_args 2>&1 | tee -a "$LOG_FILE"
echo "ODM processing complete" | tee -a "$LOG_FILE"
				`, cfg.ODMProjectID, odmFlagsStr),
			},
		},
		Dependencies: []string{"download"},
	}

	// Upload container - uploads results to writeS3Path
	// Logs are written to shared workspace for later collection
	uploadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:            "upload",
			Image:           cfg.RcloneImage,
			Command:         []string{"/bin/sh", "-c"},
			Args:            []string{s3.GenerateUploadScript(cfg.WriteS3Path) + " 2>&1 | tee /workspace/{{workflow.name}}/.upload.log"},
			Env:             awsEnv,
			Resources:       containerRequirements(cfg.UploadResources),
			SecurityContext: workflowContainerSecurityContext(),
		},
		Dependencies: []string{"process"},
	}

	// Cleanup template runs as workflow onExit so it executes after terminal outcome
	// (succeeded/failed/error), preserving log archival on failure paths.
	cleanupTemplate := wfv1.Template{
		Name: "cleanup",
		Container: &apiv1.Container{
			Name:            "cleanup",
			Image:           cfg.RcloneImage,
			Command:         []string{"/bin/sh", "-c"},
			Args:            []string{s3.GenerateLogUploadScript(cfg.WriteS3Path)},
			Resources:       containerRequirements(cfg.CleanupResources),
			SecurityContext: workflowContainerSecurityContext(),
			Env: append(awsEnv,
				apiv1.EnvVar{
					Name:  "ARGO_NAMESPACE",
					Value: c.namespace,
				},
			),
			VolumeMounts: []apiv1.VolumeMount{
				{
					Name:      "workspace",
					MountPath: "/workspace",
				},
				{
					Name:      "tmp",
					MountPath: "/tmp",
				},
			},
		},
	}

	activeDeadline := cfg.RuntimeGuardrails.ActiveDeadlineSeconds
	if activeDeadline <= 0 {
		activeDeadline = 21600
	}

	ttlSuccess := cfg.RuntimeGuardrails.TTLSuccessSeconds
	if ttlSuccess <= 0 {
		ttlSuccess = 86400
	}
	ttlFailure := cfg.RuntimeGuardrails.TTLFailureSeconds
	if ttlFailure <= 0 {
		ttlFailure = 604800
	}

	mainTemplate := wfv1.Template{
		Name:          "main",
		RetryStrategy: toRetryStrategy(cfg.RuntimeGuardrails.Retry),
		ContainerSet: &wfv1.ContainerSetTemplate{
			VolumeMounts: []apiv1.VolumeMount{
				{
					Name:      "workspace",
					MountPath: "/workspace",
				},
				{
					Name:      "tmp",
					MountPath: "/tmp",
				},
			},
			Containers: []wfv1.ContainerNode{
				downloadContainer,
				odmContainer,
				uploadContainer,
			},
		},
	}

	workspaceSize := strings.TrimSpace(cfg.Workspace.Size)
	if workspaceSize == "" {
		workspaceSize = "30Gi"
	}

	workspaceStorageClass := strings.TrimSpace(cfg.Workspace.StorageClass)
	workspaceAccessMode := parseWorkspaceAccessMode(cfg.Workspace.AccessMode)
	useWorkspacePVC := shouldUseWorkspacePVC(cfg.Workspace)

	tmpVolumeSizeLimit := resource.MustParse("20Gi")
	tmpVolume := apiv1.Volume{
		Name: "tmp",
		VolumeSource: apiv1.VolumeSource{
			EmptyDir: &apiv1.EmptyDirVolumeSource{
				SizeLimit: &tmpVolumeSizeLimit,
			},
		},
	}
	mainTemplate.Volumes = []apiv1.Volume{tmpVolume}
	cleanupTemplate.Volumes = []apiv1.Volume{tmpVolume}

	if !useWorkspacePVC {
		emptyDirWorkspace := apiv1.Volume{
			Name: "workspace",
			VolumeSource: apiv1.VolumeSource{
				EmptyDir: &apiv1.EmptyDirVolumeSource{},
			},
		}
		mainTemplate.Volumes = append(mainTemplate.Volumes, emptyDirWorkspace)
		cleanupTemplate.Volumes = append(cleanupTemplate.Volumes, emptyDirWorkspace)
	}

	capacityType := cfg.CapacityType
	if !IsValidCapacityType(capacityType) {
		capacityType = CapacityTypeSpot
	}

	tolerations := []apiv1.Toleration{}
	if capacityType == CapacityTypeSpot {
		tolerations = append(tolerations, apiv1.Toleration{
			Key:      "spot",
			Operator: apiv1.TolerationOpEqual,
			Value:    "true",
			Effect:   apiv1.TaintEffectPreferNoSchedule,
		})
	}

	// Create workflow
	wf := &wfv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "odm-pipeline-",
			Namespace:    c.namespace,
		},
		Spec: wfv1.WorkflowSpec{
			Entrypoint:            "main",
			OnExit:                "cleanup",
			ServiceAccountName:    cfg.ServiceAccount,
			PodSpecPatch:          `{"securityContext":{"fsGroup":1000,"seccompProfile":{"type":"RuntimeDefault"}}}`,
			ActiveDeadlineSeconds: &activeDeadline,
			TTLStrategy: &wfv1.TTLStrategy{
				SecondsAfterSuccess: &ttlSuccess,
				SecondsAfterFailure: &ttlFailure,
			},
			PodGC:     toPodGC(cfg.RuntimeGuardrails.PodGCStrategy, cfg.RuntimeGuardrails.PodGCDeleteDelaySecond),
			Templates: []wfv1.Template{mainTemplate, cleanupTemplate},
			NodeSelector: map[string]string{
				"node-type":                  "cpu",
				"karpenter.sh/capacity-type": capacityType,
			},
			Tolerations: tolerations,
		},
	}

	if useWorkspacePVC {
		workspaceClaim := apiv1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "workspace",
			},
			Spec: apiv1.PersistentVolumeClaimSpec{
				AccessModes: []apiv1.PersistentVolumeAccessMode{workspaceAccessMode},
				Resources: apiv1.VolumeResourceRequirements{
					Requests: apiv1.ResourceList{
						apiv1.ResourceStorage: resource.MustParse(workspaceSize),
					},
				},
			},
		}
		if workspaceStorageClass != "" {
			workspaceClaim.Spec.StorageClassName = &workspaceStorageClass
		}
		wf.Spec.VolumeClaimTemplates = []apiv1.PersistentVolumeClaim{workspaceClaim}
	}

	return wf
}
