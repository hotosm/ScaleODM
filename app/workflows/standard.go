package workflows

import (
	"context"
	"fmt"
	"log"
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
	// Policy maps to Argo's retryStrategy.retryPolicy. Empty defaults to
	// OnTransientError. See chart values.yaml for the trade-offs.
	Policy string
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

// Peak RAM fit: ~0.042*images + 16 GiB (from testing). The ~16 GiB SIFT/OpenSFM
// baseline dominates small jobs. MEMORY_LIMIT_MARGIN_PERCENT pads the limit.
// Demand steepens past 5k images, where OpenMVS per-view scene state starts to
// dominate; see docs/system-requirements.md.
var odmMemoryEstimationPoints = []interpolationPoint{
	{images: 40, ramGiB: 18},
	{images: 200, ramGiB: 25},
	{images: 500, ramGiB: 37},
	{images: 1000, ramGiB: 58},
	{images: 1500, ramGiB: 79},
	{images: 2500, ramGiB: 121},
	{images: 3500, ramGiB: 163},
	{images: 5000, ramGiB: 227},
	{images: 12000, ramGiB: 800},
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
				Policy:             config.SCALEODM_WORKFLOW_RETRY_POLICY,
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

	applyOnDemandUpgrade(cfg)
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

// flagMemoryMultiplier adjusts RAM estimates for high/low-cost ODM modes.
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
	// peakRAMGiB is the brief peak working set (RAM + swap), not the resident
	// set. It sets the memory limit and the CPU/ephemeral sizing.
	peakRAMGiB := clamp(estimateMemoryGiB(imageCount)*flagMemoryMultiplier(odmFlags), config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)

	// requestGiB is the real RAM to schedule; swap absorbs the peak. See
	// SCALEODM_PROCESS_SWAP_RATIO.
	requestGiB := peakRAMGiB
	if config.SCALEODM_PROCESS_SWAP_RATIO > 0 {
		requestGiB = peakRAMGiB / (1 + config.SCALEODM_PROCESS_SWAP_RATIO)
	}
	if requestGiB < config.SCALEODM_PROCESS_MEMORY_REQUEST_MIN_GIB {
		requestGiB = config.SCALEODM_PROCESS_MEMORY_REQUEST_MIN_GIB
	}
	if requestGiB > peakRAMGiB {
		requestGiB = peakRAMGiB
	}

	marginMultiplier := 1 + (config.SCALEODM_PROCESS_MEMORY_LIMIT_MARGIN_PERCENT / 100)
	if marginMultiplier < 1 {
		marginMultiplier = 1
	}

	// memoryLimitGiB is memory.max: it caps resident (non-swapped) memory. With
	// swap on it targets below node RAM so reclaim pushes the peak into swap
	// (above node RAM the cgroup gets no pressure and kubelet's swap-blind
	// eviction kills the pod), and stays above the request so the pod is
	// Burstable (Guaranteed gets no swap). Not guaranteed below node RAM when
	// the request lands near a node-size boundary. See decisions/0003-swap.md.
	memoryLimitGiB := peakRAMGiB * marginMultiplier
	if config.SCALEODM_PROCESS_SWAP_RATIO > 0 {
		memoryLimitGiB = requestGiB * marginMultiplier
		if memoryLimitGiB <= requestGiB {
			memoryLimitGiB = requestGiB * 1.05
		}
	}

	// CPU scales off the RAM request so it never forces a bigger instance than
	// RAM does; ODM uses every node core regardless of the request.
	cpuRequestCores := math.Max(1, requestGiB*config.SCALEODM_PROCESS_CPU_PER_GIB)
	if config.SCALEODM_PROCESS_CPU_MAX_CORES > 0 && cpuRequestCores > config.SCALEODM_PROCESS_CPU_MAX_CORES {
		cpuRequestCores = config.SCALEODM_PROCESS_CPU_MAX_CORES
	}
	// A multiplier of 0 omits the CPU limit, letting ODM burst to the whole node.
	cpuLimit := ""
	if config.SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER > 0 {
		cpuLimitCores := math.Max(cpuRequestCores, cpuRequestCores*config.SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER)
		cpuLimit = formatCPU(cpuLimitCores)
	}

	// Ephemeral (ODM scratch) scales off the peak, the true working-set size.
	ephemeralRequestGiB := math.Max(10, peakRAMGiB*config.SCALEODM_PROCESS_EPHEMERAL_GIB_PER_GIB_RAM)
	ephemeralLimitGiB := math.Max(ephemeralRequestGiB, ephemeralRequestGiB*config.SCALEODM_PROCESS_EPHEMERAL_LIMIT_MULTIPLIER)

	estimated := ContainerResources{
		Requests: ResourceSpec{
			CPU:              formatCPU(cpuRequestCores),
			Memory:           formatGiBAsMi(requestGiB),
			EphemeralStorage: formatGiBAsMi(ephemeralRequestGiB),
		},
		Limits: ResourceSpec{
			CPU:              cpuLimit,
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
		// Beyond the table, extrapolate on the last segment's slope instead of
		// clamping. MEMORY_MAX_GIB still bounds it to what the cluster can schedule.
		prev := odmMemoryEstimationPoints[len(odmMemoryEstimationPoints)-2]
		slope := (last.ramGiB - prev.ramGiB) / float64(last.images-prev.images)
		extrapolated := last.ramGiB + slope*float64(imageCount-last.images)
		return clamp(extrapolated, config.SCALEODM_PROCESS_MEMORY_MIN_GIB, config.SCALEODM_PROCESS_MEMORY_MAX_GIB)
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

// buildNodeScheduling returns the node selector and tolerations for workflow
// pods. The base NODE_SELECTOR/TOLERATIONS apply in every mode; "karpenter"
// mode also adds capacity-type routing and the spot toleration. "generic" mode
// adds nothing Karpenter-specific.
func buildNodeScheduling(capacityType string) (map[string]string, []apiv1.Toleration) {
	nodeSelector := parseNodeSelector(config.SCALEODM_WORKFLOW_NODE_SELECTOR)
	tolerations := parseTolerations(config.SCALEODM_WORKFLOW_TOLERATIONS)

	if strings.EqualFold(strings.TrimSpace(config.SCALEODM_WORKFLOW_SCHEDULING_MODE), "generic") {
		return nodeSelector, tolerations
	}

	if !IsValidCapacityType(capacityType) {
		capacityType = CapacityTypeSpot
	}
	nodeSelector["karpenter.sh/capacity-type"] = capacityType
	if capacityType == CapacityTypeSpot {
		tolerations = append(tolerations, apiv1.Toleration{
			Key:      "spot",
			Operator: apiv1.TolerationOpEqual,
			Value:    "true",
			Effect:   apiv1.TaintEffectPreferNoSchedule,
		})
	}
	return nodeSelector, tolerations
}

// parseNodeSelector parses a comma-separated "key=value" list into a label map.
// Blank or malformed entries are skipped.
func parseNodeSelector(raw string) map[string]string {
	selector := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			continue
		}
		selector[key] = strings.TrimSpace(value)
	}
	return selector
}

// parseTolerations parses a ';'-separated list of "key=value:Effect" entries.
// Omitting "=value" uses the Exists operator; omitting ":Effect" matches all.
func parseTolerations(raw string) []apiv1.Toleration {
	tolerations := []apiv1.Toleration{}
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		keyval := item
		effect := ""
		if idx := strings.LastIndex(item, ":"); idx != -1 {
			keyval = item[:idx]
			effect = strings.TrimSpace(item[idx+1:])
		}

		key, value, hasValue := strings.Cut(keyval, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}

		tol := apiv1.Toleration{Key: key}
		if hasValue && value != "" {
			tol.Operator = apiv1.TolerationOpEqual
			tol.Value = value
		} else {
			tol.Operator = apiv1.TolerationOpExists
		}
		if effect != "" {
			tol.Effect = apiv1.TaintEffect(effect)
		}
		tolerations = append(tolerations, tol)
	}
	return tolerations
}

// burstHeadroomGiB is ceil(limit - request) in GiB, a rough guide. 0 when unset
// or request == limit (Guaranteed, no swap). With swap on the limit is a resident
// cap below node RAM, so this is just the RAM margin, not the full swap span; the
// real per-pod allowance is container_swap_limit_bytes. See docs/swap.md.
func burstHeadroomGiB(r ContainerResources) int64 {
	if r.Requests.Memory == "" || r.Limits.Memory == "" {
		return 0
	}
	req, err1 := resource.ParseQuantity(r.Requests.Memory)
	lim, err2 := resource.ParseQuantity(r.Limits.Memory)
	if err1 != nil || err2 != nil {
		return 0
	}
	diffBytes := lim.Value() - req.Value()
	if diffBytes <= 0 {
		return 0
	}
	const giB = 1024 * 1024 * 1024
	return int64(math.Ceil(float64(diffBytes) / giB))
}

// applyOnDemandUpgrade pins large spot jobs to on-demand capacity, since a spot
// eviction partway through a multi-hour run would waste the whole job.
func applyOnDemandUpgrade(cfg *ODMPipelineConfig) {
	// Capacity type is ignored in generic mode, so skip the phantom upgrade.
	if strings.EqualFold(strings.TrimSpace(config.SCALEODM_WORKFLOW_SCHEDULING_MODE), "generic") {
		return
	}
	threshold := config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD
	if threshold > 0 && cfg.ImageCount >= threshold && cfg.CapacityType == CapacityTypeSpot {
		cfg.CapacityType = CapacityTypeOnDemand
		log.Printf("capacity upgraded spot->on-demand project=%s images=%d threshold=%d",
			cfg.ODMProjectID, cfg.ImageCount, threshold)
	}
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

// flagWorkspaceProfile returns the bytes multiplier, GiB-per-image floor,
// and min size for the ODM profile. Precedence matches flagMemoryMultiplier.
func flagWorkspaceProfile(odmFlags []string) (multiplier, gibPerImage, minGiB float64) {
	for _, f := range odmFlags {
		if f == "--fast-orthophoto" {
			return config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER,
				config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_GIB_PER_IMAGE,
				config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB
		}
	}
	for _, f := range odmFlags {
		if f == "--dsm" || f == "--dtm" {
			return config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER,
				config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_GIB_PER_IMAGE,
				config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB
		}
	}
	return config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER,
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE,
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
}

func estimateWorkspaceGiB(imageTotalBytes int64, imageCount int, odmFlags []string) float64 {
	multiplier, gibPerImage, minGiB := flagWorkspaceProfile(odmFlags)
	maxGiB := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	if multiplier <= 0 || maxGiB <= 0 || maxGiB < minGiB {
		return 0
	}

	bytesEstimate := float64(imageTotalBytes)
	if bytesEstimate <= 0 && imageCount > 0 {
		fallbackMBPerImage := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
		if fallbackMBPerImage > 0 {
			bytesEstimate = float64(imageCount) * fallbackMBPerImage * 1024 * 1024
		}
	}

	bytesBasedGiB := (bytesEstimate / (1024 * 1024 * 1024)) * multiplier

	// Intermediates scale with image count, not raw bytes (~66 MB/image in
	// standard mode, from testing). Without this floor, small-image datasets get
	// under-provisioned.
	countBasedGiB := 0.0
	if gibPerImage > 0 && imageCount > 0 {
		countBasedGiB = float64(imageCount) * gibPerImage
	}

	gibEstimate := math.Max(bytesBasedGiB, countBasedGiB)
	if gibEstimate <= 0 {
		return 0
	}

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
		Limit:       &limit,
		RetryPolicy: parseRetryPolicy(cfg.Policy),
		Backoff: &wfv1.Backoff{
			Duration:    cfg.BackoffDuration,
			Factor:      &factor,
			MaxDuration: cfg.BackoffMaxDuration,
		},
	}
}

// parseRetryPolicy maps a string to Argo's RetryPolicy.
// Unknown or empty values fall back to OnTransientError.
func parseRetryPolicy(policy string) wfv1.RetryPolicy {
	switch strings.TrimSpace(policy) {
	case string(wfv1.RetryPolicyAlways):
		return wfv1.RetryPolicyAlways
	case string(wfv1.RetryPolicyOnFailure):
		return wfv1.RetryPolicyOnFailure
	case string(wfv1.RetryPolicyOnError):
		return wfv1.RetryPolicyOnError
	case string(wfv1.RetryPolicyOnTransientError), "":
		return wfv1.RetryPolicyOnTransientError
	default:
		return wfv1.RetryPolicyOnTransientError
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

	// Download input files. Argo captures stdout when log archival is enabled.
	downloadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "download",
			Image:   cfg.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{fmt.Sprintf(`set -e
set -o pipefail
echo "=== download attempt {{retries}} @ $(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ) ==="
%s`, s3.GenerateDownloadScript(jobID, cfg.ReadS3Path, cfg.ExcludePaths, cfg.S3ScanDepth))},
			Env:             awsEnv,
			Resources:       containerRequirements(cfg.DownloadResources),
			SecurityContext: workflowContainerSecurityContext(),
		},
	}

	// Run ODM with unbuffered Python so partial logs are flushed promptly.
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
echo "=== process attempt {{retries}} @ $(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ) ==="
echo "Running ODM processing..."
echo "Processing job: $JOB_ID"
echo "ODM Project ID: %s"
odm_args="%s --project-path /workspace $JOB_ID"
echo "Executing: python3 -u run.py $odm_args"
python3 -u run.py $odm_args
echo "ODM processing complete"
				`, cfg.ODMProjectID, odmFlagsStr),
			},
		},
		Dependencies: []string{"download"},
	}

	// Upload results to writeS3Path.
	uploadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "upload",
			Image:   cfg.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{fmt.Sprintf(`set -e
set -o pipefail
echo "=== upload attempt {{retries}} @ $(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ) ==="
%s`, s3.GenerateUploadScript(cfg.WriteS3Path))},
			Env:             awsEnv,
			Resources:       containerRequirements(cfg.UploadResources),
			SecurityContext: workflowContainerSecurityContext(),
		},
		Dependencies: []string{"process"},
	}

	// onExit prints a small workspace snapshot before the PVC is removed.
	cleanupEnv := []apiv1.EnvVar{
		{
			Name:  "WORKFLOW_STATUS",
			Value: "{{workflow.status}}",
		},
		{
			Name:  "WORKFLOW_FAILURES",
			Value: "{{workflow.failures}}",
		},
		{
			Name:  "WORKFLOW_DURATION",
			Value: "{{workflow.duration}}",
		},
		{
			Name:  "WORKFLOW_NAME",
			Value: "{{workflow.name}}",
		},
		{
			Name:  "WORKFLOW_UID",
			Value: "{{workflow.uid}}",
		},
		{
			Name:  "WORKFLOW_CREATION_TIMESTAMP",
			Value: "{{workflow.creationTimestamp}}",
		},
	}

	cleanupTemplate := wfv1.Template{
		Name: "cleanup",
		Container: &apiv1.Container{
			Name:            "cleanup",
			Image:           cfg.RcloneImage,
			Command:         []string{"/bin/sh", "-c"},
			Args:            []string{s3.GenerateWorkspaceSnapshotScript()},
			Resources:       containerRequirements(cfg.CleanupResources),
			SecurityContext: workflowContainerSecurityContext(),
			Env:             cleanupEnv,
			VolumeMounts: []apiv1.VolumeMount{
				{
					Name:      "workspace",
					MountPath: "/workspace",
				},
			},
		},
	}

	activeDeadline := cfg.RuntimeGuardrails.ActiveDeadlineSeconds
	if activeDeadline <= 0 {
		activeDeadline = 172800
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
	// Cleanup container only reads the workspace - no /tmp scratch needed
	// since it doesn't run rclone or any tool that writes outside the mount.
	cleanupTemplate.Volumes = nil

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

	nodeSelector, tolerations := buildNodeScheduling(cfg.CapacityType)

	// Advertise limit - request as a rough guide (not the real LimitedSwap grant,
	// which is proportional); see docs/swap.md.
	annotations := map[string]string{}
	if headroom := burstHeadroomGiB(cfg.ProcessResources); headroom > 0 {
		annotations["scaleodm.hotosm.org/burst-headroom-gib"] = fmt.Sprintf("%d", headroom)
	}
	if len(annotations) == 0 {
		annotations = nil
	}

	wf := &wfv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "odm-pipeline-",
			Namespace:    c.namespace,
			Annotations:  annotations,
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
			PodGC:        toPodGC(cfg.RuntimeGuardrails.PodGCStrategy, cfg.RuntimeGuardrails.PodGCDeleteDelaySecond),
			Templates:    []wfv1.Template{mainTemplate, cleanupTemplate},
			NodeSelector: nodeSelector,
			Tolerations:  tolerations,
		},
	}

	// Protect long jobs from voluntary Karpenter disruption (consolidation,
	// drift). Spot interruption and node failure can still evict; retries cover it.
	if config.SCALEODM_WORKFLOW_DO_NOT_DISRUPT {
		wf.Spec.PodMetadata = &wfv1.Metadata{
			Annotations: map[string]string{"karpenter.sh/do-not-disrupt": "true"},
		}
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
