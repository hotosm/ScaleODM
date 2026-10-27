package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/workflows"
)

func TestParseTaskNewRequest_Rejections(t *testing.T) {
	depth := func(d int) *int { return &d }
	valid := TaskNewRequest{ReadS3Path: "s3://bucket/images"}
	tests := []struct {
		name       string
		mutate     func(*TaskNewRequest)
		wantReason string
		wantStatus int
	}{
		{"reserved mode", func(r *TaskNewRequest) { r.ProcessingMode = "thermal" }, "processing_mode_not_implemented", 501},
		{"unknown mode", func(r *TaskNewRequest) { r.ProcessingMode = "nope" }, "invalid_processing_mode", 400},
		{"bad capacity", func(r *TaskNewRequest) { r.CapacityType = "cheap" }, "invalid_capacity_type", 400},
		{"image not allowlisted", func(r *TaskNewRequest) { r.OdmImage = "evil/image" }, "invalid_odm_image", 400},
		{"scan depth too deep", func(r *TaskNewRequest) { r.S3ScanDepth = depth(11) }, "invalid_s3_scan_depth", 400},
		{"exclude paths not JSON", func(r *TaskNewRequest) { r.ExcludePaths = "nope" }, "invalid_exclude_paths", 400},
		{"unsafe exclude pattern", func(r *TaskNewRequest) { r.ExcludePaths = `["../etc"]` }, "invalid_exclude_pattern", 400},
		{"options not JSON", func(r *TaskNewRequest) { r.Options = "nope" }, "invalid_options", 400},
		{"http zipurl", func(r *TaskNewRequest) { r.ReadS3Path = ""; r.ZipURL = "https://x/a.zip" }, "http_zip_not_supported", 400},
		{"other zipurl", func(r *TaskNewRequest) { r.ReadS3Path = ""; r.ZipURL = "ftp://x/a.zip" }, "invalid_zipurl", 400},
		{"no input", func(r *TaskNewRequest) { r.ReadS3Path = "" }, "missing_read_path", 400},
		{"read path not s3", func(r *TaskNewRequest) { r.ReadS3Path = "https://x/" }, "invalid_read_path", 400},
		{"write path not s3", func(r *TaskNewRequest) { r.WriteS3Path = "https://x/" }, "invalid_write_path", 400},
		{"unsafe name", func(r *TaskNewRequest) { r.Name = "a;b" }, "invalid_project_name", 400},
		{"unsafe option flag", func(r *TaskNewRequest) { r.Options = `[{"name":"x","value":"a b"}]` }, "invalid_option_flag", 400},
		{"bad endpoint", func(r *TaskNewRequest) { r.S3Endpoint = "http://" }, "invalid_s3_endpoint", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			tt.mutate(&req)
			_, rej := parseTaskNewRequest(req)
			require.NotNil(t, rej)
			assert.Equal(t, tt.wantReason, rej.reason)
			assert.Equal(t, tt.wantStatus, rej.status)
		})
	}
}

func TestParseTaskNewRequest_Defaults(t *testing.T) {
	spec, rej := parseTaskNewRequest(TaskNewRequest{ReadS3Path: "s3://bucket/images"})
	require.Nil(t, rej)
	assert.Equal(t, "odm-project", spec.projectID)
	assert.Equal(t, "s3://bucket/images/", spec.readPath)
	assert.Equal(t, "s3://bucket/images/output/", spec.writePath)
	assert.Equal(t, workflows.ProcessingModeStandard, spec.processingMode)
	assert.Equal(t, config.SCALEODM_WORKFLOW_CAPACITY_TYPE, spec.capacityType)
	assert.Equal(t, config.SCALEODM_ODM_IMAGE, spec.odmImage)
	assert.Equal(t, workflows.DefaultS3ScanDepth, spec.s3ScanDepth)
	assert.True(t, spec.useDefaultExcludes)
	assert.Empty(t, spec.files)
	assert.Equal(t, config.AWS_S3_ENDPOINT, spec.workflowS3Endpoint())
}

func TestParseTaskNewRequest_LegacyS3ZipURL(t *testing.T) {
	spec, rej := parseTaskNewRequest(TaskNewRequest{ZipURL: "s3://bucket/project/"})
	require.Nil(t, rej)
	assert.Equal(t, "s3://bucket/project/", spec.readPath)
	assert.Equal(t, "s3://bucket/project-output/", spec.writePath)
}

func TestTaskSpecWorkflowConfig(t *testing.T) {
	spec, rej := parseTaskNewRequest(TaskNewRequest{
		Name:       "proj",
		ReadS3Path: "s3://bucket/images/",
		S3Endpoint: "http://minio:9000",
		Options:    `[{"name":"dsm","value":true},{"name":"cameras","value":"s3://bucket/cameras.json"}]`,
	})
	require.Nil(t, rej)

	cfg := spec.workflowConfig(12, 3400)
	assert.Equal(t, "proj", cfg.ODMProjectID)
	assert.Equal(t, []string{"--dsm"}, cfg.ODMFlags)
	assert.Equal(t, "http://minio:9000", cfg.S3Endpoint)
	assert.Equal(t, 12, cfg.ImageCount)
	assert.Equal(t, int64(3400), cfg.ImageTotalBytes)
	assert.Equal(t, "s3://bucket/cameras.json", cfg.Cameras.S3Path)
	assert.Equal(t, spec.excludePatterns(), cfg.ExcludePaths)
}
