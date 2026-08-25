package workflows

import (
	"strings"
	"testing"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/hotosm/scaleodm/app/config"
	apiv1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func profileTriple(m, gibPerImage, min float64) [3]float64 {
	return [3]float64{m, gibPerImage, min}
}

func TestNewDefaultODMConfig(t *testing.T) {
	projectID := "test-project"
	readPath := "s3://bucket/images/"
	writePath := "s3://bucket/output/"
	flags := []string{"--fast-orthophoto", "--dsm"}

	config := NewDefaultODMConfig(projectID, readPath, writePath, flags)

	assert.Equal(t, projectID, config.ODMProjectID)
	assert.Equal(t, readPath, config.ReadS3Path)
	assert.Equal(t, writePath, config.WriteS3Path)
	assert.Equal(t, flags, config.ODMFlags)
	assert.Equal(t, "us-east-1", config.S3Region)
	assert.Equal(t, "argo-odm", config.ServiceAccount)
	assert.True(t, strings.HasPrefix(config.RcloneImage, "docker.io/rclone/rclone:1"), "rclone image should stay on major version 1")
}

func TestBuildODMWorkflow(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{
		namespace: "test-namespace",
	}

	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	assert.Equal(t, "test-namespace", wf.Namespace)
	assert.Equal(t, "main", wf.Spec.Entrypoint)
	assert.Equal(t, "argo-odm", wf.Spec.ServiceAccountName)
	assert.NotEmpty(t, wf.Spec.Templates)
}

func TestBuildODMWorkflow_CleanupRunsOnTerminalUploadStates(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotEmpty(t, wf.Spec.Templates)
	assert.Equal(t, "cleanup", wf.Spec.OnExit)

	mainTemplate := wf.Spec.Templates[0]
	require.NotNil(t, mainTemplate.ContainerSet)
	for _, container := range mainTemplate.ContainerSet.Containers {
		assert.NotEqual(t, "cleanup", container.Name)
	}

	var cleanupTemplate *wfv1.Template
	for i := range wf.Spec.Templates {
		if wf.Spec.Templates[i].Name == "cleanup" {
			cleanupTemplate = &wf.Spec.Templates[i]
			break
		}
	}
	require.NotNil(t, cleanupTemplate)
	require.NotNil(t, cleanupTemplate.Container)
	assert.Equal(t, "cleanup", cleanupTemplate.Container.Name)
}

// Stage containers no longer write log files - stdout is captured by Argo's
// archive. Guard against tee/log-file machinery sneaking back in.
func TestBuildODMWorkflow_StageContainersDoNotWriteLogFiles(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotEmpty(t, wf.Spec.Templates)
	require.NotNil(t, wf.Spec.Templates[0].ContainerSet)

	for _, stage := range []string{"download", "process", "upload"} {
		t.Run(stage, func(t *testing.T) {
			var container *wfv1.ContainerNode
			for i := range wf.Spec.Templates[0].ContainerSet.Containers {
				if wf.Spec.Templates[0].ContainerSet.Containers[i].Name == stage {
					container = &wf.Spec.Templates[0].ContainerSet.Containers[i]
					break
				}
			}
			require.NotNil(t, container)
			require.Len(t, container.Args, 1)
			script := container.Args[0]

			assert.NotContains(t, script, "tee -a", "no tee chain; Argo archives stdout")
			assert.NotContains(t, script, ".download.log", "stage log files removed")
			assert.NotContains(t, script, ".process.log", "stage log files removed")
			assert.NotContains(t, script, ".upload.log", "stage log files removed")
			// Per-retry attempt marker stays on stdout for diagnostic clarity.
			assert.Contains(t, script, "{{retries}}")
		})
	}
}

func TestBuildODMWorkflow_ProcessContainerUsesUnbufferedPython(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf.Spec.Templates[0].ContainerSet)

	var process *wfv1.ContainerNode
	for i := range wf.Spec.Templates[0].ContainerSet.Containers {
		if wf.Spec.Templates[0].ContainerSet.Containers[i].Name == "process" {
			process = &wf.Spec.Templates[0].ContainerSet.Containers[i]
			break
		}
	}
	require.NotNil(t, process)
	require.Len(t, process.Args, 1)

	// python3 -u keeps stdout line-buffered so partial logs survive
	// SIGKILL/OOM and reach Argo's log archive.
	assert.Contains(t, process.Args[0], "python3 -u run.py")
}

// Cleanup pod is now a stdout-only diagnostic dump - it must not carry AWS
// credentials, must not mount /tmp (no rclone), and must forward only the
// {{workflow.*}} env vars its snapshot script uses.
func TestBuildODMWorkflow_CleanupMinimalAndForwardsStatus(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	var cleanup *wfv1.Template
	for i := range wf.Spec.Templates {
		if wf.Spec.Templates[i].Name == "cleanup" {
			cleanup = &wf.Spec.Templates[i]
			break
		}
	}
	require.NotNil(t, cleanup)
	require.NotNil(t, cleanup.Container)

	// Cleanup script writes only to stdout - no rclone, no AWS creds needed.
	for _, env := range cleanup.Container.Env {
		assert.NotContains(t, env.Name, "AWS_", "cleanup must not carry AWS creds (no rclone uploads)")
		assert.NotEqual(t, "TMPDIR", env.Name, "no TMPDIR; cleanup doesn't need /tmp scratch")
	}

	// No /tmp mount either.
	for _, vm := range cleanup.Container.VolumeMounts {
		assert.NotEqual(t, "tmp", vm.Name, "cleanup must not mount /tmp")
	}
	// Only the workspace mount is needed for the snapshot.
	require.Len(t, cleanup.Container.VolumeMounts, 1)
	assert.Equal(t, "workspace", cleanup.Container.VolumeMounts[0].Name)

	// Argo globals forwarded for the snapshot script.
	expected := map[string]string{
		"WORKFLOW_STATUS":             "{{workflow.status}}",
		"WORKFLOW_FAILURES":           "{{workflow.failures}}",
		"WORKFLOW_DURATION":           "{{workflow.duration}}",
		"WORKFLOW_NAME":               "{{workflow.name}}",
		"WORKFLOW_UID":                "{{workflow.uid}}",
		"WORKFLOW_CREATION_TIMESTAMP": "{{workflow.creationTimestamp}}",
	}
	found := map[string]string{}
	for _, env := range cleanup.Container.Env {
		if _, want := expected[env.Name]; want {
			found[env.Name] = env.Value
		}
	}
	for k, v := range expected {
		assert.Equal(t, v, found[k], "cleanup env %s should forward Argo global", k)
	}
}

func TestToRetryStrategy_RetryPolicy(t *testing.T) {
	cases := []struct {
		name   string
		policy string
		want   wfv1.RetryPolicy
	}{
		{name: "default empty -> OnTransientError", policy: "", want: wfv1.RetryPolicyOnTransientError},
		{name: "explicit OnTransientError", policy: "OnTransientError", want: wfv1.RetryPolicyOnTransientError},
		{name: "Always", policy: "Always", want: wfv1.RetryPolicyAlways},
		{name: "OnFailure", policy: "OnFailure", want: wfv1.RetryPolicyOnFailure},
		{name: "OnError", policy: "OnError", want: wfv1.RetryPolicyOnError},
		{name: "garbage falls back to OnTransientError", policy: "Sometimes", want: wfv1.RetryPolicyOnTransientError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := toRetryStrategy(RetryConfig{Limit: 1, Policy: tc.policy})
			require.NotNil(t, rs)
			assert.Equal(t, tc.want, rs.RetryPolicy)
		})
	}
}

func TestBuildODMWorkflow_UsesSecretKeyRef(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{
		namespace: "test-namespace",
	}

	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	mainTemplate := wf.Spec.Templates[0]
	require.NotNil(t, mainTemplate.ContainerSet)

	// Check that containers use secretKeyRef for AWS credentials
	containers := mainTemplate.ContainerSet.Containers
	require.Greater(t, len(containers), 0)

	downloadContainer := containers[0]
	hasAccessKey := false
	hasSecretKey := false
	for _, env := range downloadContainer.Env {
		if env.Name == "AWS_ACCESS_KEY_ID" {
			hasAccessKey = true
			require.NotNil(t, env.ValueFrom, "AWS_ACCESS_KEY_ID should use ValueFrom")
			require.NotNil(t, env.ValueFrom.SecretKeyRef, "AWS_ACCESS_KEY_ID should use secretKeyRef")
			assert.Equal(t, "AWS_ACCESS_KEY_ID", env.ValueFrom.SecretKeyRef.Key)
		}
		if env.Name == "AWS_SECRET_ACCESS_KEY" {
			hasSecretKey = true
			require.NotNil(t, env.ValueFrom, "AWS_SECRET_ACCESS_KEY should use ValueFrom")
			require.NotNil(t, env.ValueFrom.SecretKeyRef, "AWS_SECRET_ACCESS_KEY should use secretKeyRef")
			assert.Equal(t, "AWS_SECRET_ACCESS_KEY", env.ValueFrom.SecretKeyRef.Key)
		}
	}
	assert.True(t, hasAccessKey, "AWS_ACCESS_KEY_ID should be present")
	assert.True(t, hasSecretKey, "AWS_SECRET_ACCESS_KEY should be present")
}

func TestEstimateMemoryGiB_InterpolatesFromTable(t *testing.T) {
	// Below the first point, return the first point's value.
	assert.InDelta(t, 18, estimateMemoryGiB(40), 0.001)
	// Interpolation between (200, 25) and (500, 37): ratio=50/300, ram=27.
	assert.InDelta(t, 27, estimateMemoryGiB(250), 0.001)
	// Interpolation between (500, 37) and (1000, 58): ratio=200/500=0.4, ram=37+0.4*21=45.4.
	assert.InDelta(t, 45.4, estimateMemoryGiB(700), 0.001)
	// Table value returned as-is when under the cap.
	assert.InDelta(t, 227, estimateMemoryGiB(5000), 0.001)
	// Interpolation on the steep (5000, 227) -> (12000, 800) segment
	// (573/7000 = 0.08186/image), clamped by the 256 GiB default max:
	// 8000 -> 227 + 3000*0.08186 = ~473, clamped to 256.
	assert.InDelta(t, 256, estimateMemoryGiB(8000), 0.001)
}

func TestEstimateMemoryGiB_ExtrapolatesBeyondTable(t *testing.T) {
	prev := config.SCALEODM_PROCESS_MEMORY_MAX_GIB
	config.SCALEODM_PROCESS_MEMORY_MAX_GIB = 2048
	t.Cleanup(func() { config.SCALEODM_PROCESS_MEMORY_MAX_GIB = prev })

	// With the cap raised, 13000 images extrapolate to
	// 800 + (13000-12000)*0.08186 = ~882 GiB peak.
	assert.InDelta(t, 882, estimateMemoryGiB(13000), 1.0)
}

// withSwapRatio sets the swap ratio (default is 0/off) for tests that exercise
// the request/limit split, restoring it afterward.
func withSwapRatio(t *testing.T, v float64) {
	prev := config.SCALEODM_PROCESS_SWAP_RATIO
	config.SCALEODM_PROCESS_SWAP_RATIO = v
	t.Cleanup(func() { config.SCALEODM_PROCESS_SWAP_RATIO = prev })
}

func TestEstimateProcessResourcesFromImageCount_SplitsRequestAndLimit(t *testing.T) {
	withSwapRatio(t, 2.0)
	fallback := ContainerResources{}
	// 250 images interpolates to a 27 GiB peak. With swap ratio 2.0 the RAM
	// request is peak/3 = 9 GiB. With swap on, the limit is a resident ceiling
	// (request * 1.2 = 10.8 GiB, below node RAM), so the cgroup forces the peak's
	// overflow into swap. CPU/ephemeral still scale off the peak, not the request.
	resources := estimateProcessResourcesFromImageCount(250, nil, fallback)
	assert.Equal(t, "9216Mi", resources.Requests.Memory) // 9 GiB * 1024 (27 / 3)
	assert.Equal(t, "11060Mi", resources.Limits.Memory)  // ceil(9 * 1.2 * 1024)
	// CPU is requested off the 9 GiB RAM request: 9 * 0.125 = 1.125 cores.
	assert.Equal(t, "1125m", resources.Requests.CPU)
	assert.Equal(t, "1688m", resources.Limits.CPU) // ceil(1.125 * 1.5 * 1000)
	assert.NotEmpty(t, resources.Requests.EphemeralStorage)
	assert.NotEmpty(t, resources.Limits.EphemeralStorage)
}

func TestEstimateProcessResourcesFromImageCount_SwapLimitTracksRequestNotPeak(t *testing.T) {
	fallback := ContainerResources{}

	// Same 27 GiB peak (250 images), two swap modes.
	withSwapRatio(t, 0)
	off := estimateProcessResourcesFromImageCount(250, nil, fallback)
	assert.Equal(t, "27648Mi", off.Requests.Memory) // no swap: request == peak
	assert.Equal(t, "33178Mi", off.Limits.Memory)   // limit == peak * 1.2

	withSwapRatio(t, 1.0)
	on := estimateProcessResourcesFromImageCount(250, nil, fallback)
	// Swap on: request = peak/2, and the limit is a resident ceiling based on the
	// request (below node RAM), strictly above it for Burstable QoS -- NOT the
	// peak-based limit, which would sit above node RAM and defeat swap by letting
	// the resident set fill the node into swap-blind node-pressure eviction.
	assert.Equal(t, "13824Mi", on.Requests.Memory)
	assert.Equal(t, "16589Mi", on.Limits.Memory) // request * 1.2, well below off's peak-based limit
}

func TestEstimateProcessResourcesFromImageCount_ScalesCPUOffRAMRequest(t *testing.T) {
	withSwapRatio(t, 2.0)
	fallback := ContainerResources{}
	// 5000 images: 227 GiB peak, RAM request = 75.667 GiB. CPU scales off the
	// request (not the peak): 75.667 * 0.125 = 9.458 cores.
	resources := estimateProcessResourcesFromImageCount(5000, nil, fallback)

	assert.Equal(t, "77483Mi", resources.Requests.Memory) // ceil(227/3 * 1024)
	assert.Equal(t, "9459m", resources.Requests.CPU)      // ceil(75.667 * 0.125 * 1000)
	assert.Equal(t, "14188m", resources.Limits.CPU)       // ceil(9.458 * 1.5 * 1000)
}

func TestEstimateProcessResourcesFromImageCount_CapsCPUCores(t *testing.T) {
	withSwapRatio(t, 2.0)
	prev := config.SCALEODM_PROCESS_CPU_MAX_CORES
	config.SCALEODM_PROCESS_CPU_MAX_CORES = 5
	t.Cleanup(func() { config.SCALEODM_PROCESS_CPU_MAX_CORES = prev })

	// 5000 images -> 75.667 GiB request -> 9.458 cores uncapped, capped to 5.
	resources := estimateProcessResourcesFromImageCount(5000, nil, ContainerResources{})
	assert.Equal(t, "5", resources.Requests.CPU)
	assert.Equal(t, "7500m", resources.Limits.CPU) // 5 * 1.5
}

func TestEstimateProcessResourcesFromImageCount_OmitsCPULimitWhenMultiplierZero(t *testing.T) {
	prev := config.SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER
	config.SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER = 0
	t.Cleanup(func() { config.SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER = prev })

	// With no multiplier the CPU limit is omitted so ODM bursts to the whole node.
	resources := estimateProcessResourcesFromImageCount(2500, nil, ContainerResources{})
	assert.NotEmpty(t, resources.Requests.CPU)
	assert.Empty(t, resources.Limits.CPU)
}

func TestBuildODMWorkflow_StampsDoNotDisrupt(t *testing.T) {
	prev := config.SCALEODM_WORKFLOW_DO_NOT_DISRUPT
	config.SCALEODM_WORKFLOW_DO_NOT_DISRUPT = true
	t.Cleanup(func() { config.SCALEODM_WORKFLOW_DO_NOT_DISRUPT = prev })

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf.Spec.PodMetadata)
	assert.Equal(t, "true", wf.Spec.PodMetadata.Annotations["karpenter.sh/do-not-disrupt"])
}

func TestApplyOnDemandUpgrade(t *testing.T) {
	prevT := config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD
	prevM := config.SCALEODM_WORKFLOW_SCHEDULING_MODE
	config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD = 5000
	config.SCALEODM_WORKFLOW_SCHEDULING_MODE = "karpenter"
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD = prevT
		config.SCALEODM_WORKFLOW_SCHEDULING_MODE = prevM
	})

	// Large spot job is upgraded to on-demand.
	big := &ODMPipelineConfig{ImageCount: 5000, CapacityType: CapacityTypeSpot}
	applyOnDemandUpgrade(big)
	assert.Equal(t, CapacityTypeOnDemand, big.CapacityType)

	// Small spot job is left on spot.
	small := &ODMPipelineConfig{ImageCount: 4999, CapacityType: CapacityTypeSpot}
	applyOnDemandUpgrade(small)
	assert.Equal(t, CapacityTypeSpot, small.CapacityType)

	// An explicit on-demand choice is unaffected.
	od := &ODMPipelineConfig{ImageCount: 100, CapacityType: CapacityTypeOnDemand}
	applyOnDemandUpgrade(od)
	assert.Equal(t, CapacityTypeOnDemand, od.CapacityType)
}

func TestApplyOnDemandUpgrade_GenericModeNoUpgrade(t *testing.T) {
	prevT := config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD
	prevM := config.SCALEODM_WORKFLOW_SCHEDULING_MODE
	config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD = 5000
	config.SCALEODM_WORKFLOW_SCHEDULING_MODE = "generic"
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD = prevT
		config.SCALEODM_WORKFLOW_SCHEDULING_MODE = prevM
	})

	// In generic mode capacity type is ignored, so a large job is not mutated.
	big := &ODMPipelineConfig{ImageCount: 20000, CapacityType: CapacityTypeSpot}
	applyOnDemandUpgrade(big)
	assert.Equal(t, CapacityTypeSpot, big.CapacityType)
}

func TestFlagMemoryMultiplier(t *testing.T) {
	assert.Equal(t, 1.0, flagMemoryMultiplier(nil))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--orthophoto-resolution=5"}))
	assert.Equal(t, 0.5, flagMemoryMultiplier([]string{"--fast-orthophoto"}))
	// Fast orthophoto takes precedence over other flags.
	assert.Equal(t, 0.5, flagMemoryMultiplier([]string{"--fast-orthophoto", "--dsm"}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--dsm"}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--dtm"}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--dsm", "--dtm"}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--dsm", "--boundary=/b.geojson"}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--dsm", "--auto-boundary"}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--boundary=/b.geojson"}))
}

func TestEstimateProcessResourcesFromImageCount_AppliesFlagMultiplier(t *testing.T) {
	withSwapRatio(t, 2.0)
	fallback := ContainerResources{}

	// 250 images base = 27 GiB peak; --fast-orthophoto halves it to a 13.5 GiB
	// peak. RAM request = peak/3 = 4.5 GiB; with swap on the limit is the resident
	// ceiling request * 1.2, not peak * 1.2.
	fast := estimateProcessResourcesFromImageCount(250, []string{"--fast-orthophoto"}, fallback)
	assert.Equal(t, "4608Mi", fast.Requests.Memory) // 4.5 GiB * 1024 (13.5 / 3)
	assert.Equal(t, "5530Mi", fast.Limits.Memory)   // ceil(4.5 * 1.2 * 1024)

	// Other flags use standard sizing.
	dsm := estimateProcessResourcesFromImageCount(250, []string{"--dsm"}, fallback)
	plain := estimateProcessResourcesFromImageCount(250, nil, fallback)
	assert.Equal(t, plain, dsm)
	assert.Equal(t, "9216Mi", dsm.Requests.Memory) // 9 GiB * 1024 (27 / 3)
	assert.Equal(t, "11060Mi", dsm.Limits.Memory)  // ceil(9 * 1.2 * 1024)

	// Small jobs use the first table point: 18 GiB peak and a 6 GiB request.
	small := estimateProcessResourcesFromImageCount(7, []string{"--dsm"}, fallback)
	assert.Equal(t, "6144Mi", small.Requests.Memory) // 6 GiB * 1024 (18 / 3)
	assert.Equal(t, "7373Mi", small.Limits.Memory)   // ceil(6 * 1.2 * 1024)
}

func TestBuildODMWorkflow_AppliesGuardrailsAndResources(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	cfg.ImageCount = 500
	cfg.ProcessResources = estimateProcessResourcesFromImageCount(cfg.ImageCount, cfg.ODMFlags, cfg.ProcessResources)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf.Spec.ActiveDeadlineSeconds)
	assert.Greater(t, *wf.Spec.ActiveDeadlineSeconds, int64(0))
	require.NotNil(t, wf.Spec.TTLStrategy)
	require.NotNil(t, wf.Spec.TTLStrategy.SecondsAfterSuccess)
	require.NotNil(t, wf.Spec.TTLStrategy.SecondsAfterFailure)
	require.NotNil(t, wf.Spec.PodGC)
	require.NotNil(t, wf.Spec.Templates[0].RetryStrategy)
	assert.NotContains(t, wf.Spec.PodSpecPatch, `"runAsNonRoot"`)
	assert.Contains(t, wf.Spec.PodSpecPatch, `"seccompProfile":{"type":"RuntimeDefault"}`)

	containers := wf.Spec.Templates[0].ContainerSet.Containers
	require.Len(t, containers, 3)
	for _, container := range containers {
		assert.NotEmpty(t, container.Resources.Requests)
		assert.NotEmpty(t, container.Resources.Limits)
		require.NotNil(t, container.SecurityContext)
		require.NotNil(t, container.SecurityContext.RunAsNonRoot)
		assert.True(t, *container.SecurityContext.RunAsNonRoot)
		require.NotNil(t, container.SecurityContext.RunAsUser)
		assert.Equal(t, int64(1000), *container.SecurityContext.RunAsUser)
		require.NotNil(t, container.SecurityContext.RunAsGroup)
		assert.Equal(t, int64(1000), *container.SecurityContext.RunAsGroup)
		require.NotNil(t, container.SecurityContext.AllowPrivilegeEscalation)
		assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
		require.NotNil(t, container.SecurityContext.ReadOnlyRootFilesystem)
		assert.True(t, *container.SecurityContext.ReadOnlyRootFilesystem)
		require.NotNil(t, container.SecurityContext.Capabilities)
		assert.Equal(t, []apiv1.Capability{"ALL"}, container.SecurityContext.Capabilities.Drop)
		require.NotNil(t, container.SecurityContext.SeccompProfile)
		assert.Equal(t, apiv1.SeccompProfileTypeRuntimeDefault, container.SecurityContext.SeccompProfile.Type)
	}

	var cleanupTemplate *wfv1.Template
	for i := range wf.Spec.Templates {
		if wf.Spec.Templates[i].Name == "cleanup" {
			cleanupTemplate = &wf.Spec.Templates[i]
			break
		}
	}
	require.NotNil(t, cleanupTemplate)
	require.NotNil(t, cleanupTemplate.Container)
	require.NotNil(t, cleanupTemplate.Container.SecurityContext)
	assert.Equal(t, int64(1000), *cleanupTemplate.Container.SecurityContext.RunAsUser)
	assert.Equal(t, int64(1000), *cleanupTemplate.Container.SecurityContext.RunAsGroup)
}

func TestBuildODMWorkflow_UsesEmptyDirWorkspaceInEmptyDirMode(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	cfg.Workspace.Mode = "emptyDir"
	cfg.Workspace.StorageClass = "gp3"

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	require.Empty(t, wf.Spec.VolumeClaimTemplates)
	require.Len(t, wf.Spec.Templates, 2)
	tmpVol := volumeByName(t, wf.Spec.Templates[0].Volumes, "tmp")
	require.NotNil(t, tmpVol.EmptyDir)
	require.NotNil(t, tmpVol.EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", tmpVol.EmptyDir.SizeLimit.String())
	require.NotNil(t, volumeByName(t, wf.Spec.Templates[0].Volumes, "workspace").EmptyDir)
}

func TestBuildODMWorkflow_UsesPVCWorkspaceInPVCMode(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	cfg.Workspace.Mode = "pvc"
	cfg.Workspace.Size = "40Gi"
	cfg.Workspace.StorageClass = "gp3"
	cfg.Workspace.AccessMode = "ReadWriteOnce"

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	require.Len(t, wf.Spec.VolumeClaimTemplates, 1)
	assert.NotContains(t, volumeNames(wf.Spec.Templates[0].Volumes), "workspace",
		"workspace comes from the volumeClaimTemplate in PVC mode")
	tmpVol := volumeByName(t, wf.Spec.Templates[0].Volumes, "tmp")
	require.NotNil(t, tmpVol.EmptyDir)
	require.NotNil(t, tmpVol.EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", tmpVol.EmptyDir.SizeLimit.String())

	claim := wf.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, "workspace", claim.Name)
	require.NotNil(t, claim.Spec.StorageClassName)
	assert.Equal(t, "gp3", *claim.Spec.StorageClassName)
	require.Len(t, claim.Spec.AccessModes, 1)
	assert.Equal(t, apiv1.ReadWriteOnce, claim.Spec.AccessModes[0])
	assert.Equal(t, "40Gi", claim.Spec.Resources.Requests.Storage().String())

	// Scratch PVCs are collected after every workflow.
	require.NotNil(t, wf.Spec.VolumeClaimGC)
	assert.Equal(t, wfv1.VolumeClaimGCOnCompletion, wf.Spec.VolumeClaimGC.Strategy)
}

func TestBuildODMWorkflow_UsesPVCWorkspaceInAutoModeWhenStorageClassSet(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	cfg.Workspace.Mode = "auto"
	cfg.Workspace.Size = "55Gi"
	cfg.Workspace.StorageClass = "ceph-rbd"
	cfg.Workspace.AccessMode = "ReadWriteMany"

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	require.Len(t, wf.Spec.VolumeClaimTemplates, 1)
	assert.NotContains(t, volumeNames(wf.Spec.Templates[0].Volumes), "workspace",
		"workspace comes from the volumeClaimTemplate in PVC mode")
	tmpVol := volumeByName(t, wf.Spec.Templates[0].Volumes, "tmp")
	require.NotNil(t, tmpVol.EmptyDir)
	require.NotNil(t, tmpVol.EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", tmpVol.EmptyDir.SizeLimit.String())

	claim := wf.Spec.VolumeClaimTemplates[0]
	require.NotNil(t, claim.Spec.StorageClassName)
	assert.Equal(t, "ceph-rbd", *claim.Spec.StorageClassName)
	require.Len(t, claim.Spec.AccessModes, 1)
	assert.Equal(t, apiv1.ReadWriteMany, claim.Spec.AccessModes[0])
	assert.Equal(t, "55Gi", claim.Spec.Resources.Requests.Storage().String())
}

func TestBuildODMWorkflow_UsesEmptyDirWorkspaceInAutoModeWithoutStorageClass(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	cfg.Workspace.Mode = "auto"
	cfg.Workspace.StorageClass = ""

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	require.Empty(t, wf.Spec.VolumeClaimTemplates)
	assert.Nil(t, wf.Spec.VolumeClaimGC, "no PVC to collect in emptyDir mode")
	tmpVol := volumeByName(t, wf.Spec.Templates[0].Volumes, "tmp")
	require.NotNil(t, tmpVol.EmptyDir)
	require.NotNil(t, tmpVol.EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", tmpVol.EmptyDir.SizeLimit.String())
	require.NotNil(t, volumeByName(t, wf.Spec.Templates[0].Volumes, "workspace").EmptyDir)
}

func TestApplyDynamicWorkspaceSize_DisabledKeepsStaticSize(t *testing.T) {
	prevEnabled := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = false
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = prevEnabled
	})

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.Workspace.Mode = "pvc"
	cfg.Workspace.Size = "30Gi"
	cfg.Workspace.StorageClass = "gp3"
	cfg.ImageTotalBytes = 120 * 1024 * 1024 * 1024
	cfg.ImageCount = 500

	applyDynamicWorkspaceSize(cfg)

	assert.Equal(t, "30Gi", cfg.Workspace.Size)
}

func TestApplyDynamicWorkspaceSize_EnabledPVCComputesSize(t *testing.T) {
	prevEnabled := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = true
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 4
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 30
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = prevEnabled
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
	})

	// nil flags -> standard profile with multiplier=4, min=30; 10 GiB x 4 = 40 GiB
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.Workspace.Mode = "pvc"
	cfg.Workspace.Size = "30Gi"
	cfg.ImageTotalBytes = 10 * 1024 * 1024 * 1024

	applyDynamicWorkspaceSize(cfg)

	assert.Equal(t, "40Gi", cfg.Workspace.Size)
}

func TestFlagWorkspaceProfile(t *testing.T) {
	prevFastM := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER
	prevFastG := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_GIB_PER_IMAGE
	prevFastMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB
	prevStdM := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevStdG := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE
	prevStdMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevDemM := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER
	prevDemG := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_GIB_PER_IMAGE
	prevDemMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER = 3
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_GIB_PER_IMAGE = 0.05
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB = 30
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 6
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = 0.10
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 50
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER = 10
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_GIB_PER_IMAGE = 0.20
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB = 90
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER = prevFastM
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_GIB_PER_IMAGE = prevFastG
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB = prevFastMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevStdM
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = prevStdG
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevStdMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER = prevDemM
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_GIB_PER_IMAGE = prevDemG
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB = prevDemMin
	})

	// standard profile for nil and non-special flags
	assert.Equal(t, [3]float64{6, 0.10, 50}, profileTriple(flagWorkspaceProfile(nil)))
	assert.Equal(t, [3]float64{6, 0.10, 50}, profileTriple(flagWorkspaceProfile([]string{"--orthophoto-resolution=5"})))
	assert.Equal(t, [3]float64{6, 0.10, 50}, profileTriple(flagWorkspaceProfile([]string{"--pc-quality", "ultra"})))

	// --dsm/--dtm gets the larger DSM/DTM profile (surface rasters need ~2x
	// disk over the standard pipeline)
	assert.Equal(t, [3]float64{10, 0.20, 90}, profileTriple(flagWorkspaceProfile([]string{"--dsm"})))
	assert.Equal(t, [3]float64{10, 0.20, 90}, profileTriple(flagWorkspaceProfile([]string{"--dtm"})))
	assert.Equal(t, [3]float64{10, 0.20, 90}, profileTriple(flagWorkspaceProfile([]string{"--dsm", "--dtm"})))
	assert.Equal(t, [3]float64{10, 0.20, 90}, profileTriple(flagWorkspaceProfile([]string{"--orthophoto-resolution=5", "--dsm"})))

	// fast-orthophoto profile
	assert.Equal(t, [3]float64{3, 0.05, 30}, profileTriple(flagWorkspaceProfile([]string{"--fast-orthophoto"})))
	// fast-orthophoto wins over --dsm/--dtm because it skips the dense
	// reconstruction those flags depend on.
	assert.Equal(t, [3]float64{3, 0.05, 30}, profileTriple(flagWorkspaceProfile([]string{"--fast-orthophoto", "--dsm"})))
	assert.Equal(t, [3]float64{3, 0.05, 30}, profileTriple(flagWorkspaceProfile([]string{"--dtm", "--fast-orthophoto"})))
}

func TestEstimateWorkspaceGiB_PrefersBytesOverCountFallback(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevGibPerImage := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	prevFallback := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = 0
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = 20
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = prevGibPerImage
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = prevFallback
	})

	gib := estimateWorkspaceGiB(2*1024*1024*1024, 10000, nil)

	assert.InDelta(t, 2.0, gib, 0.0001)
}

func TestEstimateWorkspaceGiB_UsesCountFallbackDefault20MB(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevGibPerImage := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	prevFallback := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = 0
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = 20
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = prevGibPerImage
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = prevFallback
	})

	gib := estimateWorkspaceGiB(0, 100, nil)

	assert.InDelta(t, 1.953125, gib, 0.0001)
}

// TestEstimateWorkspaceGiB_CountFloorBeatsSmallBytes covers the case the
// count-based floor was added for: small image bytes would size the workspace
// well below the actual intermediate-artifact growth measured from tests.
func TestEstimateWorkspaceGiB_CountFloorBeatsSmallBytes(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevGibPerImage := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 8
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = 0.10
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_GIB_PER_IMAGE = prevGibPerImage
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
	})

	const gib = int64(1024 * 1024 * 1024)

	// 1000 small images totalling 5 GiB: bytes-based = 5 * 8 = 40 GiB,
	// count-based = 1000 * 0.10 = 100 GiB; max() picks 100.
	smallImages := estimateWorkspaceGiB(5*gib, 1000, nil)
	assert.InDelta(t, 100.0, smallImages, 0.001)

	// 1000 large images totalling 25 GiB: bytes-based = 25 * 8 = 200 GiB,
	// count-based = 100 GiB; max() picks 200, so the floor is inert here.
	largeImages := estimateWorkspaceGiB(25*gib, 1000, nil)
	assert.InDelta(t, 200.0, largeImages, 0.001)
}

func TestEstimateWorkspacePVCSize_ClampsToMinAndMax(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 30
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 100
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
	})

	minSize, ok := estimateWorkspacePVCSize(1*1024*1024*1024, 0, nil)
	require.True(t, ok)
	assert.Equal(t, "30Gi", minSize)

	maxSize, ok := estimateWorkspacePVCSize(500*1024*1024*1024, 0, nil)
	require.True(t, ok)
	assert.Equal(t, "100Gi", maxSize)
}

func TestBuildODMWorkflow_SpotNodeSelector(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.CapacityType = CapacityTypeSpot

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	assert.Equal(t, "spot", wf.Spec.NodeSelector["karpenter.sh/capacity-type"])
	assert.Equal(t, "cpu", wf.Spec.NodeSelector["node-type"])
	require.Len(t, wf.Spec.Tolerations, 1)
	assert.Equal(t, "spot", wf.Spec.Tolerations[0].Key)
	assert.Equal(t, apiv1.TaintEffectPreferNoSchedule, wf.Spec.Tolerations[0].Effect)
}

func TestBuildODMWorkflow_OnDemandNodeSelector(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.CapacityType = CapacityTypeOnDemand

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	assert.Equal(t, "on-demand", wf.Spec.NodeSelector["karpenter.sh/capacity-type"])
	assert.Equal(t, "cpu", wf.Spec.NodeSelector["node-type"])
	assert.Empty(t, wf.Spec.Tolerations)
}

func TestBuildODMWorkflow_InvalidCapacityTypeFallsBackToSpot(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.CapacityType = "invalid"

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	assert.Equal(t, "spot", wf.Spec.NodeSelector["karpenter.sh/capacity-type"])
}

func TestBuildNodeScheduling_GenericModeOmitsKarpenter(t *testing.T) {
	prevMode := config.SCALEODM_WORKFLOW_SCHEDULING_MODE
	prevSelector := config.SCALEODM_WORKFLOW_NODE_SELECTOR
	prevTolerations := config.SCALEODM_WORKFLOW_TOLERATIONS
	config.SCALEODM_WORKFLOW_SCHEDULING_MODE = "generic"
	config.SCALEODM_WORKFLOW_NODE_SELECTOR = "pool=swap"
	config.SCALEODM_WORKFLOW_TOLERATIONS = "swap=true:NoSchedule"
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_SCHEDULING_MODE = prevMode
		config.SCALEODM_WORKFLOW_NODE_SELECTOR = prevSelector
		config.SCALEODM_WORKFLOW_TOLERATIONS = prevTolerations
	})

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.CapacityType = CapacityTypeSpot // must be ignored in generic mode

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	// No Karpenter capacity-type label, no spot toleration.
	_, hasCapacity := wf.Spec.NodeSelector["karpenter.sh/capacity-type"]
	assert.False(t, hasCapacity)
	assert.Equal(t, "swap", wf.Spec.NodeSelector["pool"])
	require.Len(t, wf.Spec.Tolerations, 1)
	assert.Equal(t, "swap", wf.Spec.Tolerations[0].Key)
	assert.Equal(t, "true", wf.Spec.Tolerations[0].Value)
	assert.Equal(t, apiv1.TaintEffectNoSchedule, wf.Spec.Tolerations[0].Effect)
}

func TestBuildNodeScheduling_EmptySelectorPlacesAnywhere(t *testing.T) {
	prevMode := config.SCALEODM_WORKFLOW_SCHEDULING_MODE
	prevSelector := config.SCALEODM_WORKFLOW_NODE_SELECTOR
	config.SCALEODM_WORKFLOW_SCHEDULING_MODE = "generic"
	config.SCALEODM_WORKFLOW_NODE_SELECTOR = ""
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_SCHEDULING_MODE = prevMode
		config.SCALEODM_WORKFLOW_NODE_SELECTOR = prevSelector
	})

	selector, tolerations := buildNodeScheduling(CapacityTypeSpot)
	assert.Empty(t, selector)
	assert.Empty(t, tolerations)
}

func TestParseTolerations(t *testing.T) {
	tolerations := parseTolerations("swap=true:NoSchedule; dedicated:NoExecute; ;bad")
	require.Len(t, tolerations, 3)

	assert.Equal(t, "swap", tolerations[0].Key)
	assert.Equal(t, apiv1.TolerationOpEqual, tolerations[0].Operator)
	assert.Equal(t, "true", tolerations[0].Value)
	assert.Equal(t, apiv1.TaintEffectNoSchedule, tolerations[0].Effect)

	// No "=value" -> Exists operator.
	assert.Equal(t, "dedicated", tolerations[1].Key)
	assert.Equal(t, apiv1.TolerationOpExists, tolerations[1].Operator)
	assert.Equal(t, apiv1.TaintEffectNoExecute, tolerations[1].Effect)

	// "bad" has no effect separator -> Exists, no effect.
	assert.Equal(t, "bad", tolerations[2].Key)
	assert.Equal(t, apiv1.TolerationOpExists, tolerations[2].Operator)
	assert.Empty(t, string(tolerations[2].Effect))
}

func TestBurstHeadroomGiB(t *testing.T) {
	// Burstable: limit 32Gi - request 8Gi = 24Gi headroom.
	assert.Equal(t, int64(24), burstHeadroomGiB(ContainerResources{
		Requests: ResourceSpec{Memory: "8Gi"},
		Limits:   ResourceSpec{Memory: "32Gi"},
	}))
	// Guaranteed (request == limit) -> no headroom / no swap.
	assert.Equal(t, int64(0), burstHeadroomGiB(ContainerResources{
		Requests: ResourceSpec{Memory: "8Gi"},
		Limits:   ResourceSpec{Memory: "8Gi"},
	}))
	// Unset -> 0.
	assert.Equal(t, int64(0), burstHeadroomGiB(ContainerResources{}))
}

func TestBuildODMWorkflow_AnnotatesBurstHeadroom(t *testing.T) {
	withSwapRatio(t, 2.0)
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.ImageCount = 2500
	cfg.ProcessResources = estimateProcessResourcesFromImageCount(cfg.ImageCount, cfg.ODMFlags, cfg.ProcessResources)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	require.NotNil(t, wf.Annotations)
	assert.NotEmpty(t, wf.Annotations["scaleodm.hotosm.org/burst-headroom-gib"])
}

func TestApplyDynamicWorkspaceSize_EmptyDirUnaffected(t *testing.T) {
	prevEnabled := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = true
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = prevEnabled
	})

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.Workspace.Mode = "emptyDir"
	cfg.Workspace.Size = "30Gi"
	cfg.ImageTotalBytes = 80 * 1024 * 1024 * 1024

	applyDynamicWorkspaceSize(cfg)

	assert.Equal(t, "30Gi", cfg.Workspace.Size)
}

func TestBuildODMWorkflow_ProcessContainerHasWritableHome(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf.Spec.Templates[0].ContainerSet)

	var process *wfv1.ContainerNode
	for i := range wf.Spec.Templates[0].ContainerSet.Containers {
		if wf.Spec.Templates[0].ContainerSet.Containers[i].Name == "process" {
			process = &wf.Spec.Templates[0].ContainerSet.Containers[i]
			break
		}
	}
	require.NotNil(t, process)

	env := map[string]string{}
	for _, e := range process.Env {
		env[e.Name] = e.Value
	}

	for _, name := range []string{
		"TMPDIR", "HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
		"XDG_DATA_HOME", "MPLCONFIGDIR",
	} {
		value, ok := env[name]
		require.True(t, ok, "%s must be set on the process container", name)
		assert.True(t, strings.HasPrefix(value, "/tmp"),
			"%s=%s must live on the writable /tmp emptyDir", name, value)
	}

	// Some tools expect $HOME to exist rather than creating it.
	require.Len(t, process.Args, 1)
	assert.Contains(t, process.Args[0], `mkdir -p "$HOME"`)
	assert.Contains(t, process.Args[0], `"$MPLCONFIGDIR"`)
}

func TestBuildODMWorkflow_ProcessContainerHasWritableModelCache(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--dtm"},
	)

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)

	main := wf.Spec.Templates[0]
	require.NotNil(t, main.ContainerSet)

	var volume *apiv1.Volume
	for i := range main.Volumes {
		if main.Volumes[i].Name == "odm-model-cache" {
			volume = &main.Volumes[i]
			break
		}
	}
	require.NotNil(t, volume, "model cache volume must be declared on the template")
	require.NotNil(t, volume.EmptyDir, "model cache must be an emptyDir, not the read-only root")
	assert.NotNil(t, volume.EmptyDir.SizeLimit, "emptyDir needs a sizeLimit or it can fill the node")

	for _, ctr := range main.ContainerSet.Containers {
		var mounted bool
		for _, m := range ctr.VolumeMounts {
			if m.Name == "odm-model-cache" {
				mounted = true
				assert.Equal(t, odmModelCachePath, m.MountPath)
			}
		}
		assert.Equal(t, ctr.Name == "process", mounted,
			"model cache mount on %q: got %v", ctr.Name, mounted)
	}

	for _, tmpl := range wf.Spec.Templates[1:] {
		for _, v := range tmpl.Volumes {
			assert.NotEqual(t, "odm-model-cache", v.Name,
				"template %q does not need the model cache", tmpl.Name)
		}
	}
}

func volumeByName(t *testing.T, volumes []apiv1.Volume, name string) apiv1.Volume {
	t.Helper()
	for _, v := range volumes {
		if v.Name == name {
			return v
		}
	}
	require.Failf(t, "volume not found", "no volume %q in %v", name, volumeNames(volumes))
	return apiv1.Volume{}
}

func volumeNames(volumes []apiv1.Volume) []string {
	names := make([]string, 0, len(volumes))
	for _, v := range volumes {
		names = append(names, v.Name)
	}
	return names
}
