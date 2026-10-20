package workflows

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func containerArgs(t *testing.T, cfg *ODMPipelineConfig, name string) string {
	t.Helper()

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)
	require.NotNil(t, wf)

	for _, c := range wf.Spec.Templates[0].ContainerSet.Containers {
		if c.Name == name {
			require.Len(t, c.Args, 1)
			return c.Args[0]
		}
	}
	t.Fatalf("no %q container in the workflow", name)
	return ""
}

func camerasConfig(source FileSource) *ODMPipelineConfig {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--dsm"})
	cfg.Cameras = source
	return cfg
}

func TestDownloadArgs_S3CamerasFailureIsFatal(t *testing.T) {
	args := containerArgs(t, camerasConfig(FileSource{S3Path: "s3://bucket/calibration/cameras.json"}), "download")
	fetch := args[strings.Index(args, "Fetching ODM cameras"):]

	assert.Equal(t, 3, strings.Count(fetch, "exit 1"))
	assert.Contains(t, fetch, "ERROR: cameras not found or unreadable at s3://bucket/calibration/cameras.json")
	assert.Contains(t, fetch, "ERROR: cameras path s3://bucket/calibration/cameras.json is a prefix, not a single object")
	assert.Contains(t, fetch, "ERROR: cameras at s3://bucket/calibration/cameras.json is empty")

	assert.Contains(t, fetch, `CAMERAS_TMP="$CAMERAS_DEST.part"`)
	assert.Contains(t, fetch, `mv "$CAMERAS_TMP" "$CAMERAS_DEST"`)
	assert.NotContains(t, fetch, "BOUNDARY_TMP")
}

func TestDownloadArgs_InlineCamerasWrittenToWorkspace(t *testing.T) {
	source, _, err := ParseCamerasValue(testCameraModel)
	require.NoError(t, err)

	args := containerArgs(t, camerasConfig(source), "download")

	assert.Contains(t, args, "Writing inline ODM cameras")
	assert.Contains(t, args, "<<'SCALEODM_CAMERAS_EOF'")
	assert.Contains(t, args, `"focal_x":0.74018822`)
	assert.Contains(t, args, "/workspace/{{workflow.name}}/cameras-input.json")
}

// Download failure prevents processing with a missing calibration.
func TestProcessArgs_CamerasFlagIsStatic(t *testing.T) {
	args := containerArgs(t, camerasConfig(FileSource{S3Path: "s3://bucket/calibration/cameras.json"}), "process")

	assert.Contains(t, args, `odm_args="--dsm --cameras=/workspace/{{workflow.name}}/cameras-input.json --project-path /workspace $JOB_ID"`)
}

// OpenSfM controls whether intrinsics are refined.
func TestProcessArgs_CamerasNeverForcesFixedCameraParams(t *testing.T) {
	args := containerArgs(t, camerasConfig(FileSource{S3Path: "s3://bucket/calibration/cameras.json"}), "process")

	assert.NotContains(t, args, "--use-fixed-camera-params")
}

func TestProcessArgs_NoCamerasNoFlag(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--dsm"})

	assert.NotContains(t, containerArgs(t, cfg, "download"), "ODM cameras")
	assert.NotContains(t, containerArgs(t, cfg, "process"), "--cameras")
}

func TestWorkflow_BoundaryAndCamerasTogether(t *testing.T) {
	cameras, _, err := ParseCamerasValue(testCameraModel)
	require.NoError(t, err)

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--fast-orthophoto"})
	cfg.Boundary = FileSource{S3Path: "s3://bucket/aoi.geojson"}
	cfg.Cameras = cameras

	download := containerArgs(t, cfg, "download")
	assert.Contains(t, download, "Fetching ODM boundary")
	assert.Contains(t, download, "Writing inline ODM cameras")

	assert.Contains(t, containerArgs(t, cfg, "process"),
		`odm_args="--fast-orthophoto --boundary=/workspace/{{workflow.name}}/boundary.geojson `+
			`--cameras=/workspace/{{workflow.name}}/cameras-input.json --project-path /workspace $JOB_ID"`)
}

func TestWorkflow_DoesNotMutateCallerFlags(t *testing.T) {
	flags := []string{"--fast-orthophoto"}
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", flags)
	cfg.Boundary = FileSource{S3Path: "s3://bucket/aoi.geojson"}
	cfg.Cameras = FileSource{S3Path: "s3://bucket/cameras.json"}

	client := &Client{namespace: "test-namespace"}
	client.buildODMWorkflow(cfg)

	assert.Equal(t, []string{"--fast-orthophoto"}, flags)
	assert.Equal(t, []string{"--fast-orthophoto"}, cfg.ODMFlags)
}
