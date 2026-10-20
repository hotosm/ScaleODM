package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hotosm/scaleodm/app/workflows"
)

func TestOdmFlagsFromOptions_PullsOutWorkspaceFiles(t *testing.T) {
	flags, files, err := odmFlagsFromOptions([]TaskOption{
		{Name: "fast-orthophoto", Value: true},
		{Name: "boundary", Value: "s3://bucket/aoi.geojson"},
		{Name: "cameras", Value: "s3://bucket/calibration/cameras.json"},
		{Name: "orthophoto-resolution", Value: 5},
	})
	require.NoError(t, err)

	// File flags are added later with workspace paths.
	assert.Equal(t, []string{"--fast-orthophoto", "--orthophoto-resolution=5"}, flags)
	assert.Equal(t, "s3://bucket/aoi.geojson", files[workflows.BoundaryOptionName].S3Path)
	assert.Equal(t, "s3://bucket/calibration/cameras.json", files[workflows.CamerasOptionName].S3Path)
}

func TestOdmFlagsFromOptions_AcceptsNestedJSONObjects(t *testing.T) {
	_, files, err := odmFlagsFromOptions([]TaskOption{
		{Name: "cameras", Value: map[string]any{
			"dji fc8482 4032 3024 brown 0.6666": map[string]any{
				"projection_type": "brown",
				"width":           4032.0,
				"height":          3024.0,
				"focal_x":         0.74018822,
				"focal_y":         0.74018822,
			},
		}},
		{Name: "boundary", Value: map[string]any{
			"type":        "Polygon",
			"coordinates": []any{[]any{[]any{115.45, -8.35}, []any{115.48, -8.35}, []any{115.48, -8.39}, []any{115.45, -8.35}}},
		}},
	})
	require.NoError(t, err)

	assert.Contains(t, files[workflows.CamerasOptionName].Inline, `"focal_x":0.74018822`)
	assert.NotContains(t, files[workflows.CamerasOptionName].Inline, "\n")
	assert.Contains(t, files[workflows.BoundaryOptionName].Inline, `"Polygon"`)
}

func TestOdmFlagsFromOptions_PlainPathsStayFlags(t *testing.T) {
	flags, files, err := odmFlagsFromOptions([]TaskOption{
		{Name: "cameras", Value: "/workspace/cameras.json"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"--cameras=/workspace/cameras.json"}, flags)
	assert.Empty(t, files)
}

func TestOdmFlagsFromOptions_RejectsBadCameras(t *testing.T) {
	_, _, err := odmFlagsFromOptions([]TaskOption{
		{Name: "cameras", Value: `{"cam":{"projection_type":"brown","width":4032,"height":3024,"focal_x":0.74,"focal_y":0.74}}`},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "space-separated")
}

func TestWorkspaceFileMetadataRoundTrip(t *testing.T) {
	files := map[string]workflows.FileSource{
		workflows.BoundaryOptionName: {S3Path: "s3://bucket/aoi.geojson"},
		workflows.CamerasOptionName:  {Inline: `{"cam":{"projection_type":"brown"}}`},
	}

	restored := metadataWorkspaceFiles(mustJSON(t, workspaceFileMetadata(files)))

	assert.Equal(t, files, restored)
}

func TestMetadataWorkspaceFilesReadsLegacyBoundaryKeys(t *testing.T) {
	restored := metadataWorkspaceFiles([]byte(`{"boundary_s3_path":"s3://bucket/aoi.geojson"}`))

	assert.Equal(t, "s3://bucket/aoi.geojson", restored[workflows.BoundaryOptionName].S3Path)
	assert.NotContains(t, restored, workflows.CamerasOptionName)
}

func TestWorkspaceFileMetadataClearsAbsentOptions(t *testing.T) {
	patch := workspaceFileMetadata(map[string]workflows.FileSource{})

	assert.Equal(t, "", patch[metadataBoundaryS3PathKey])
	assert.Equal(t, "", patch[metadataCamerasJSONKey])
	assert.Empty(t, metadataWorkspaceFiles(mustJSON(t, patch)))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	require.NoError(t, err)
	return encoded
}
