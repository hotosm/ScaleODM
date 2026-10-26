package workflows

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// fetch_input_file itself is tested in scripts_test.go.
func TestDownloadArgs_S3BoundaryPassedToFetch(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", nil)
	cfg.Boundary = FileSource{S3Path: "s3://bucket/aoi.geojson"}

	script := containerArgs(t, cfg, "download")[0]
	assert.Contains(t, script, "BOUNDARY_DEST='/workspace/{{workflow.name}}/boundary.geojson'\n")
	assert.Contains(t, script, "BOUNDARY_S3_PATH='s3://bucket/aoi.geojson'\n")
	assert.Contains(t, script, `fetch_input_file boundary "${BOUNDARY_DEST:-}" "${BOUNDARY_S3_PATH:-}" "${BOUNDARY_INLINE:-}"`)
}

// Download aborts on an unusable boundary, so the process flag can stay static.
func TestProcessArgs_BoundaryFlagIsStatic(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--fast-orthophoto"})
	cfg.Boundary = FileSource{S3Path: "s3://bucket/aoi.geojson"}

	assert.Equal(t, []string{"--fast-orthophoto", "--boundary=/workspace/{{workflow.name}}/boundary.geojson"},
		processFlagArgs(t, cfg))
}

func TestProcessArgs_NoBoundaryNoFlag(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--fast-orthophoto"})

	assert.NotContains(t, containerArgs(t, cfg, "download")[0], "BOUNDARY_DEST=", "no boundary requested, so nothing to fetch")
	assert.Equal(t, []string{"--fast-orthophoto"}, processFlagArgs(t, cfg))
}
