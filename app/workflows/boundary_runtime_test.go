package workflows

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func downloadBoundaryScript(t *testing.T) string {
	t.Helper()

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", nil)
	cfg.Boundary = BoundarySource{S3Path: "s3://bucket/aoi.geojson"}

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)
	require.NotNil(t, wf)

	args := wf.Spec.Templates[0].ContainerSet.Containers[0].Args[0]
	require.Contains(t, args, "Fetching ODM boundary")
	return args[strings.Index(args, "Fetching ODM boundary"):]
}

// Failing to fetch a requested boundary must not degrade into a silent full-area run.
func TestDownloadArgs_S3BoundaryFailureIsFatal(t *testing.T) {
	fetch := downloadBoundaryScript(t)

	assert.Equal(t, 3, strings.Count(fetch, "exit 1"))
	assert.NotContains(t, fetch, "continuing without a boundary")

	// Distinct per-branch messages: the archived log is the only place the reason appears.
	assert.Contains(t, fetch, "ERROR: boundary not found or unreadable at s3://bucket/aoi.geojson")
	assert.Contains(t, fetch, "ERROR: boundary path s3://bucket/aoi.geojson is a prefix, not a single object")
	assert.Contains(t, fetch, "ERROR: boundary at s3://bucket/aoi.geojson is empty")
}

// A prefix fetch writes a directory that satisfies -s and survives rm -f.
func TestDownloadArgs_S3BoundaryFetchIsAtomicAndTypeChecked(t *testing.T) {
	fetch := downloadBoundaryScript(t)

	assert.Contains(t, fetch, `BOUNDARY_TMP="$BOUNDARY_DEST.part"`)
	assert.Contains(t, fetch, `rclone copyto --error-on-no-transfer "$BOUNDARY_REMOTE" "$BOUNDARY_TMP"`)
	assert.Contains(t, fetch, `mv "$BOUNDARY_TMP" "$BOUNDARY_DEST"`)
	assert.NotContains(t, fetch, `rclone copyto --error-on-no-transfer "$BOUNDARY_REMOTE" "$BOUNDARY_DEST"`)

	assert.Contains(t, fetch, `rm -rf "$BOUNDARY_DEST" "$BOUNDARY_TMP"`)
	assert.True(t, strings.Index(fetch, `rm -rf "$BOUNDARY_DEST" "$BOUNDARY_TMP"`) < strings.Index(fetch, "rclone copyto"),
		"the reset must happen before the fetch")

	assert.NotContains(t, fetch, `rm -f "`)
	assert.Contains(t, fetch, `[ ! -f "$BOUNDARY_TMP" ]`)
}

// Download aborts on an unusable boundary, so the process flag can stay static.
func TestProcessArgs_BoundaryFlagIsStatic(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--fast-orthophoto"})
	cfg.Boundary = BoundarySource{S3Path: "s3://bucket/aoi.geojson"}

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)
	require.NotNil(t, wf)

	var args string
	for _, c := range wf.Spec.Templates[0].ContainerSet.Containers {
		if c.Name == "process" {
			require.Len(t, c.Args, 1)
			args = c.Args[0]
		}
	}
	require.NotEmpty(t, args)

	assert.Contains(t, args, `odm_args="--fast-orthophoto --boundary=/workspace/{{workflow.name}}/boundary.geojson --project-path /workspace $JOB_ID"`)
}

func TestProcessArgs_NoBoundaryNoFlag(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--fast-orthophoto"})

	client := &Client{namespace: "test-namespace"}
	wf := client.buildODMWorkflow(cfg)
	require.NotNil(t, wf)

	for _, c := range wf.Spec.Templates[0].ContainerSet.Containers {
		if c.Name == "download" {
			assert.NotContains(t, c.Args[0], "Fetching ODM boundary", "no boundary requested, so nothing to fetch")
		}
		if c.Name == "process" {
			assert.NotContains(t, c.Args[0], "--boundary")
		}
	}
}
