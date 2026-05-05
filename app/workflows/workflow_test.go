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
	assert.NotEmpty(t, resources.Requests.CPU)
	assert.NotEmpty(t, resources.Limits.CPU)
	assert.NotEmpty(t, resources.Requests.EphemeralStorage)
	assert.NotEmpty(t, resources.Limits.EphemeralStorage)
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
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = true
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = 4
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = 30
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_ENABLED = prevEnabled
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
	})

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/images/", "s3://bucket/output/", nil)
	cfg.Workspace.Mode = "pvc"
	cfg.Workspace.Size = "30Gi"
	cfg.ImageTotalBytes = 10 * 1024 * 1024 * 1024

	applyDynamicWorkspaceSize(cfg)

	assert.Equal(t, "40Gi", cfg.Workspace.Size)
}

func TestEstimateWorkspaceGiB_PrefersBytesOverCountFallback(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	prevFallback := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = 20
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = prevFallback
	})

	gib := estimateWorkspaceGiB(2*1024*1024*1024, 10000)

	assert.InDelta(t, 2.0, gib, 0.0001)
}

func TestEstimateWorkspaceGiB_UsesCountFallbackDefault20MB(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	prevFallback := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 1024
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = 20
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_FALLBACK_MB_PER_IMAGE = prevFallback
	})

	gib := estimateWorkspaceGiB(0, 100)

	assert.InDelta(t, 1.953125, gib, 0.0001)
}

func TestEstimateWorkspacePVCSize_ClampsToMinAndMax(t *testing.T) {
	prevMultiplier := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER
	prevMin := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB
	prevMax := config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = 1
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = 30
	config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = 100
	t.Cleanup(func() {
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MULTIPLIER = prevMultiplier
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MIN_GIB = prevMin
		config.SCALEODM_WORKFLOW_WORKSPACE_DYNAMIC_SIZE_MAX_GIB = prevMax
	})

	minSize, ok := estimateWorkspacePVCSize(1*1024*1024*1024, 0)
	require.True(t, ok)
	assert.Equal(t, "30Gi", minSize)

	maxSize, ok := estimateWorkspacePVCSize(500*1024*1024*1024, 0)
	require.True(t, ok)
	assert.Equal(t, "100Gi", maxSize)
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
