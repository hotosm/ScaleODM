// Typed view of the job metadata JSON column, and the keys used to patch it.

package api

import (
	"encoding/json"
	"strings"

	"github.com/minio/minio-go/v7"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/workflows"
)

const (
	metadataS3EndpointKey         = "s3_endpoint"
	metadataImageCountKey         = "image_count"
	metadataImageTotalBytesKey    = "image_total_bytes"
	metadataProcessingModeKey     = "processing_mode"
	metadataExcludePathsKey       = "exclude_paths"
	metadataUseDefaultExcludesKey = "use_default_excludes"
	metadataS3ScanDepthKey        = "s3_scan_depth"
	metadataCapacityTypeKey       = "capacity_type"
	metadataBoundaryGeoJSONKey    = "boundary_geojson"
	metadataBoundaryS3PathKey     = "boundary_s3_path"
	metadataCamerasJSONKey        = "cameras_json"
	metadataCamerasS3PathKey      = "cameras_s3_path"
	metadataODMImageKey           = "odm_image"
)

type taskMetadata struct {
	S3Endpoint               string   `json:"s3_endpoint"`
	ImageCount               int      `json:"image_count"`
	ImageTotalBytes          int64    `json:"image_total_bytes"`
	WorkflowMissingFirstSeen string   `json:"workflow_missing_first_seen_at"`
	ProcessingMode           string   `json:"processing_mode"`
	CapacityType             string   `json:"capacity_type"`
	ExcludePaths             []string `json:"exclude_paths"`
	UseDefaultExcludes       *bool    `json:"use_default_excludes"`
	S3ScanDepth              *int     `json:"s3_scan_depth"`
	BoundaryGeoJSON          string   `json:"boundary_geojson"`
	BoundaryS3Path           string   `json:"boundary_s3_path"`
	CamerasJSON              string   `json:"cameras_json"`
	CamerasS3Path            string   `json:"cameras_s3_path"`
	ODMImage                 string   `json:"odm_image"`
}

// parseTaskMetadata ignores decode errors on purpose: json.Unmarshal still
// fills every well-typed field, and absent or mistyped ones read as defaults.
func parseTaskMetadata(raw []byte) taskMetadata {
	var m taskMetadata
	_ = json.Unmarshal(raw, &m)
	return m
}

func (m taskMetadata) processingMode() string {
	if m.ProcessingMode != "" {
		return m.ProcessingMode
	}
	return workflows.ProcessingModeStandard
}

func (m taskMetadata) capacityType() string {
	if workflows.IsValidCapacityType(m.CapacityType) {
		return m.CapacityType
	}
	return config.SCALEODM_WORKFLOW_CAPACITY_TYPE
}

func (m taskMetadata) s3ScanDepth() int {
	if m.S3ScanDepth == nil {
		return workflows.DefaultS3ScanDepth
	}
	return *m.S3ScanDepth
}

// Absent/legacy jobs default to true to preserve safer behaviour.
func (m taskMetadata) useDefaultExcludes() bool {
	return m.UseDefaultExcludes == nil || *m.UseDefaultExcludes
}

// s3Endpoint returns the normalized per-task endpoint, or "" for the server default.
func (m taskMetadata) s3Endpoint() string {
	normalized, err := normalizeOptionalS3Endpoint(m.S3Endpoint)
	if err != nil {
		return ""
	}
	return normalized
}

func (m taskMetadata) s3Client() (*minio.Client, error) {
	if endpoint := m.s3Endpoint(); endpoint != "" {
		return s3.GetS3ClientForEndpoint(endpoint)
	}
	return s3.GetS3Client(), nil
}

// workspaceFiles restores file sources for restart.
func (m taskMetadata) workspaceFiles() map[string]workflows.FileSource {
	files := map[string]workflows.FileSource{}
	for option, source := range map[string]workflows.FileSource{
		workflows.BoundaryOptionName: {Inline: m.BoundaryGeoJSON, S3Path: m.BoundaryS3Path},
		workflows.CamerasOptionName:  {Inline: m.CamerasJSON, S3Path: m.CamerasS3Path},
	} {
		if source.IsSet() {
			files[option] = source
		}
	}
	return files
}

// workspaceFileMetadata builds a metadata patch, clearing absent options.
func workspaceFileMetadata(files map[string]workflows.FileSource) map[string]any {
	boundary := files[workflows.BoundaryOptionName]
	cameras := files[workflows.CamerasOptionName]
	return map[string]any{
		metadataBoundaryGeoJSONKey: boundary.Inline,
		metadataBoundaryS3PathKey:  boundary.S3Path,
		metadataCamerasJSONKey:     cameras.Inline,
		metadataCamerasS3PathKey:   cameras.S3Path,
	}
}

func normalizeOptionalS3Endpoint(endpoint string) (string, error) {
	if strings.TrimSpace(endpoint) == "" {
		return "", nil
	}
	return s3.NormalizeEndpoint(endpoint)
}
