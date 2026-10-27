package workflows

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func containerArgs(t *testing.T, cfg *ODMPipelineConfig, name string) []string {
	t.Helper()

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)
	require.NotNil(t, wf)

	for _, c := range wf.Spec.Templates[0].ContainerSet.Containers {
		if c.Name == name {
			require.NotEmpty(t, c.Args)
			return c.Args
		}
	}
	t.Fatalf("no %q container in the workflow", name)
	return nil
}

// processFlagArgs returns the ODM flags passed after `bash -c SCRIPT process`.
func processFlagArgs(t *testing.T, cfg *ODMPipelineConfig) []string {
	t.Helper()
	args := containerArgs(t, cfg, "process")
	require.GreaterOrEqual(t, len(args), 2)
	require.Equal(t, "process", args[1])
	return args[2:]
}

func camerasConfig(source FileSource) *ODMPipelineConfig {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--dsm"})
	cfg.Cameras = source
	return cfg
}

func TestDownloadArgs_S3CamerasPassedToFetch(t *testing.T) {
	script := containerArgs(t, camerasConfig(FileSource{S3Path: "s3://bucket/calibration/cameras.json"}), "download")[0]

	assert.Contains(t, script, "CAMERAS_DEST='/workspace/{{workflow.name}}/cameras-input.json'\n")
	assert.Contains(t, script, "CAMERAS_S3_PATH='s3://bucket/calibration/cameras.json'\n")
	assert.Contains(t, script, `fetch_input_file cameras "${CAMERAS_DEST:-}" "${CAMERAS_S3_PATH:-}" "${CAMERAS_INLINE:-}"`)
	assert.NotContains(t, script, "BOUNDARY_DEST=")
}

func TestDownloadArgs_InlineCamerasPassedToFetch(t *testing.T) {
	source, _, err := ParseCamerasValue(testCameraModel)
	require.NoError(t, err)

	script := containerArgs(t, camerasConfig(source), "download")[0]

	assert.Contains(t, script, "CAMERAS_DEST='/workspace/{{workflow.name}}/cameras-input.json'\n")
	assert.Contains(t, script, "CAMERAS_INLINE="+shellQuote(source.Inline)+"\n")
	assert.Contains(t, script, `"focal_x":0.74018822`)
}

// Download failure prevents processing with a missing calibration.
func TestProcessArgs_CamerasFlagIsStatic(t *testing.T) {
	assert.Equal(t, []string{"--dsm", "--cameras=/workspace/{{workflow.name}}/cameras-input.json"},
		processFlagArgs(t, camerasConfig(FileSource{S3Path: "s3://bucket/calibration/cameras.json"})))
}

// OpenSfM controls whether intrinsics are refined.
func TestProcessArgs_CamerasNeverForcesFixedCameraParams(t *testing.T) {
	assert.NotContains(t, processFlagArgs(t, camerasConfig(FileSource{S3Path: "s3://bucket/calibration/cameras.json"})),
		"--use-fixed-camera-params")
}

func TestProcessArgs_NoCamerasNoFlag(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--dsm"})

	assert.NotContains(t, containerArgs(t, cfg, "download")[0], "CAMERAS_DEST=")
	assert.Equal(t, []string{"--dsm"}, processFlagArgs(t, cfg))
}

func TestWorkflow_BoundaryAndCamerasTogether(t *testing.T) {
	cameras, _, err := ParseCamerasValue(testCameraModel)
	require.NoError(t, err)

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--fast-orthophoto"})
	cfg.Boundary = FileSource{S3Path: "s3://bucket/aoi.geojson"}
	cfg.Cameras = cameras

	download := containerArgs(t, cfg, "download")[0]
	assert.Contains(t, download, "BOUNDARY_S3_PATH='s3://bucket/aoi.geojson'\n")
	assert.Contains(t, download, "CAMERAS_INLINE=")

	assert.Equal(t, []string{
		"--fast-orthophoto",
		"--boundary=/workspace/{{workflow.name}}/boundary.geojson",
		"--cameras=/workspace/{{workflow.name}}/cameras-input.json",
	}, processFlagArgs(t, cfg))
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
