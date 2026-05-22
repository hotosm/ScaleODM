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

func profilePair(m, min float64) [2]float64 { return [2]float64{m, min} }

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
	assert.InDelta(t, 4, estimateMemoryGiB(40), 0.001)
	assert.InDelta(t, 16, estimateMemoryGiB(250), 0.001)
	assert.InDelta(t, 24, estimateMemoryGiB(375), 0.001)
	assert.InDelta(t, 256, estimateMemoryGiB(5000), 0.001)
	assert.InDelta(t, 256, estimateMemoryGiB(8000), 0.001)
}

func TestEstimateProcessResourcesFromImageCount_SetsMarginLimit(t *testing.T) {
	fallback := ContainerResources{}
	resources := estimateProcessResourcesFromImageCount(250, nil, fallback)
	assert.Equal(t, "16384Mi", resources.Requests.Memory)
	assert.Equal(t, "19661Mi", resources.Limits.Memory)
	assert.Equal(t, "2", resources.Requests.CPU)
	assert.Equal(t, "3", resources.Limits.CPU)
	assert.NotEmpty(t, resources.Requests.EphemeralStorage)
	assert.NotEmpty(t, resources.Limits.EphemeralStorage)
}

func TestEstimateProcessResourcesFromImageCount_CapsLargeJobCPUByRAMRatio(t *testing.T) {
	fallback := ContainerResources{}
	resources := estimateProcessResourcesFromImageCount(5000, nil, fallback)

	assert.Equal(t, "262144Mi", resources.Requests.Memory)
	assert.Equal(t, "32", resources.Requests.CPU)
	assert.Equal(t, "48", resources.Limits.CPU)
}

func TestFlagMemoryMultiplier(t *testing.T) {
	assert.Equal(t, 1.0, flagMemoryMultiplier(nil))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{}))
	assert.Equal(t, 1.0, flagMemoryMultiplier([]string{"--orthophoto-resolution=5"}))
	assert.Equal(t, 0.5, flagMemoryMultiplier([]string{"--fast-orthophoto"}))
	assert.Equal(t, 1.5, flagMemoryMultiplier([]string{"--dsm"}))
	assert.Equal(t, 1.5, flagMemoryMultiplier([]string{"--dtm"}))
	assert.Equal(t, 1.5, flagMemoryMultiplier([]string{"--dsm", "--dtm"}))
	// fast-orthophoto takes precedence even if dsm/dtm are also set
	assert.Equal(t, 0.5, flagMemoryMultiplier([]string{"--fast-orthophoto", "--dsm"}))
}

func TestEstimateProcessResourcesFromImageCount_AppliesFlagMultiplier(t *testing.T) {
	fallback := ContainerResources{}

	// --fast-orthophoto halves the request; limit = request * 1.2
	fast := estimateProcessResourcesFromImageCount(250, []string{"--fast-orthophoto"}, fallback)
	assert.Equal(t, "8192Mi", fast.Requests.Memory) // 16 GiB * 0.5
	assert.Equal(t, "9831Mi", fast.Limits.Memory)   // 8 GiB * 1.2

	// --dsm scales up by 1.5x
	dsm := estimateProcessResourcesFromImageCount(250, []string{"--dsm"}, fallback)
	assert.Equal(t, "24576Mi", dsm.Requests.Memory) // 16 GiB * 1.5
	assert.Equal(t, "29492Mi", dsm.Limits.Memory)   // 24 GiB * 1.2

	// small job with --dsm must not fall below memoryMinGiB (4 GiB)
	small := estimateProcessResourcesFromImageCount(7, []string{"--dsm"}, fallback)
	assert.Equal(t, "6144Mi", small.Requests.Memory) // 4 GiB * 1.5 = 6 GiB
	assert.Equal(t, "7373Mi", small.Limits.Memory)   // 6 GiB * 1.2
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
	require.Len(t, wf.Spec.Templates[0].Volumes, 2)
	assert.Equal(t, "tmp", wf.Spec.Templates[0].Volumes[0].Name)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit.String())
	assert.Equal(t, "workspace", wf.Spec.Templates[0].Volumes[1].Name)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[1].EmptyDir)
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
	require.Len(t, wf.Spec.Templates[0].Volumes, 1)
	assert.Equal(t, "tmp", wf.Spec.Templates[0].Volumes[0].Name)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit.String())

	claim := wf.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, "workspace", claim.Name)
	require.NotNil(t, claim.Spec.StorageClassName)
	assert.Equal(t, "gp3", *claim.Spec.StorageClassName)
	require.Len(t, claim.Spec.AccessModes, 1)
	assert.Equal(t, apiv1.ReadWriteOnce, claim.Spec.AccessModes[0])
	assert.Equal(t, "40Gi", claim.Spec.Resources.Requests.Storage().String())
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
	require.Len(t, wf.Spec.Templates[0].Volumes, 1)
	assert.Equal(t, "tmp", wf.Spec.Templates[0].Volumes[0].Name)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit.String())

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
	require.Len(t, wf.Spec.Templates[0].Volumes, 2)
	assert.Equal(t, "tmp", wf.Spec.Templates[0].Volumes[0].Name)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit)
	assert.Equal(t, "20Gi", wf.Spec.Templates[0].Volumes[0].EmptyDir.SizeLimit.String())
	assert.Equal(t, "workspace", wf.Spec.Templates[0].Volumes[1].Name)
	require.NotNil(t, wf.Spec.Templates[0].Volumes[1].EmptyDir)
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

	// nil flags → standard profile with multiplier=4, min=30; 10 GiB × 4 = 40 GiB
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.Workspace.Mode = "pvc"
	cfg.Workspace.Size = "30Gi"
	cfg.ImageTotalBytes = 10 * 1024 * 1024 * 1024

	applyDynamicWorkspaceSize(cfg)

	assert.Equal(t, "40Gi", cfg.Workspace.Size)
}

func TestFlagWorkspaceProfile(t *testing.T) {
	prevFastM := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER
	prevFastMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB
	prevStdM := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevStdMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevDemM := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER
	prevDemMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER = 3
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB = 30
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 6
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 50
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER = 10
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB = 90
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MULTIPLIER = prevFastM
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FAST_ORTHO_MIN_GIB = prevFastMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevStdM
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevStdMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MULTIPLIER = prevDemM
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_DSM_DTM_MIN_GIB = prevDemMin
	})

	// standard profile for nil and non-special flags
	assert.Equal(t, [2]float64{6, 50}, profilePair(flagWorkspaceProfile(nil)))
	assert.Equal(t, [2]float64{6, 50}, profilePair(flagWorkspaceProfile([]string{"--orthophoto-resolution=5"})))
	assert.Equal(t, [2]float64{6, 50}, profilePair(flagWorkspaceProfile([]string{"--pc-quality", "ultra"})))

	// --dsm/--dtm gets the larger DSM/DTM profile (surface rasters need ~2x
	// disk over the standard pipeline)
	assert.Equal(t, [2]float64{10, 90}, profilePair(flagWorkspaceProfile([]string{"--dsm"})))
	assert.Equal(t, [2]float64{10, 90}, profilePair(flagWorkspaceProfile([]string{"--dtm"})))
	assert.Equal(t, [2]float64{10, 90}, profilePair(flagWorkspaceProfile([]string{"--dsm", "--dtm"})))
	assert.Equal(t, [2]float64{10, 90}, profilePair(flagWorkspaceProfile([]string{"--orthophoto-resolution=5", "--dsm"})))

	// fast-orthophoto profile
	assert.Equal(t, [2]float64{3, 30}, profilePair(flagWorkspaceProfile([]string{"--fast-orthophoto"})))
	// fast-orthophoto wins over --dsm/--dtm because it skips the dense
	// reconstruction those flags depend on.
	assert.Equal(t, [2]float64{3, 30}, profilePair(flagWorkspaceProfile([]string{"--fast-orthophoto", "--dsm"})))
	assert.Equal(t, [2]float64{3, 30}, profilePair(flagWorkspaceProfile([]string{"--dtm", "--fast-orthophoto"})))
}

func TestEstimateWorkspaceGiB_PrefersBytesOverCountFallback(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	prevFallback := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = 20
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = prevFallback
	})

	gib := estimateWorkspaceGiB(2*1024*1024*1024, 10000, nil)

	assert.InDelta(t, 2.0, gib, 0.0001)
}

func TestEstimateWorkspaceGiB_UsesCountFallbackDefault20MB(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	prevFallback := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = 20
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_STANDARD_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = prevFallback
	})

	gib := estimateWorkspaceGiB(0, 100, nil)

	assert.InDelta(t, 1.953125, gib, 0.0001)
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
