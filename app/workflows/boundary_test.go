package workflows

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBoundaryValueS3Path(t *testing.T) {
	source, passthrough, err := ParseBoundaryValue("s3://dronetm-prod/projects/abc/aoi.geojson")
	require.NoError(t, err)
	assert.Equal(t, "s3://dronetm-prod/projects/abc/aoi.geojson", source.S3Path)
	assert.Empty(t, source.GeoJSON)
	assert.Empty(t, passthrough)
	assert.True(t, source.IsSet())
}

func TestParseBoundaryValueRejectsBadS3Paths(t *testing.T) {
	for name, value := range map[string]string{
		"bucket only":  "s3://dronetm-prod",
		"prefix":       "s3://dronetm-prod/projects/",
		"traversal":    "s3://dronetm-prod/../etc/passwd",
		"empty bucket": "s3:///projects/aoi.geojson",
		"shell chars":  "s3://dronetm-prod/projects/$(id).geojson",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseBoundaryValue(value)
			assert.Error(t, err)
		})
	}
}

func TestParseBoundaryValueCompactsGeoJSON(t *testing.T) {
	pretty := `{
      "type": "FeatureCollection",
      "features": [
        {"type": "Feature", "properties": {}, "geometry": {
          "type": "Polygon",
          "coordinates": [[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.39],[115.45,-8.35]]]
        }}
      ]
    }`

	source, passthrough, err := ParseBoundaryValue(pretty)
	require.NoError(t, err)
	assert.Empty(t, passthrough)
	assert.Empty(t, source.S3Path)

	// One-line JSON cannot escape the quoted heredoc.
	assert.NotContains(t, source.GeoJSON, "\n")
	assert.Contains(t, source.GeoJSON, `"FeatureCollection"`)
}

func TestParseBoundaryValueGeoJSONCannotEscapeHeredoc(t *testing.T) {
	// Newlines in string values must remain escaped.
	hostile := "{\"type\":\"Polygon\",\"name\":\"x\\nSCALEODM_BOUNDARY_EOF\\nrm -rf /\",\"coordinates\":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}"

	source, _, err := ParseBoundaryValue(hostile)
	require.NoError(t, err)
	assert.NotContains(t, source.GeoJSON, "\n")
}

func TestParseBoundaryValueAcceptsSinglePolygonForms(t *testing.T) {
	// A bare geometry is what ODM's own --auto-boundary hands to load_boundary.
	for name, value := range map[string]string{
		"bare geometry": `{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}`,
		"feature":       `{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}}`,
		"collection":    `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}}]}`,
		"3d ring":       `{"type":"Polygon","coordinates":[[[115.45,-8.35,10],[115.48,-8.35,10],[115.48,-8.39,10],[115.45,-8.35,10]]]}`,
	} {
		t.Run(name, func(t *testing.T) {
			source, _, err := ParseBoundaryValue(value)
			require.NoError(t, err)
			assert.True(t, source.IsSet())
		})
	}
}

func TestParseBoundaryValueRejectsWhatODMRejects(t *testing.T) {
	// These are valid GeoJSON forms that ODM does not accept as boundaries.
	for name, value := range map[string]string{
		"multipolygon":        `{"type":"MultiPolygon","coordinates":[]}`,
		"point":               `{"type":"Point","coordinates":[115.4,-8.3]}`,
		"non-polygon feature": `{"type":"Feature","geometry":{"type":"LineString","coordinates":[]}}`,
		"feature no geometry": `{"type":"Feature","properties":{}}`,
		"two features": `{"type":"FeatureCollection","features":[
			{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}},
			{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}}]}`,
		"empty collection": `{"type":"FeatureCollection","features":[]}`,
		// load_boundary raises on all of these once it reaches the rings.
		"no rings":         `{"type":"Polygon","coordinates":[]}`,
		"empty ring":       `{"type":"Polygon","coordinates":[[]]}`,
		"bare ordinate":    `{"type":"Polygon","coordinates":[[[115.45]]]}`,
		"string ordinates": `{"type":"Polygon","coordinates":[[["115.45","-8.35"]]]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseBoundaryValue(value)
			assert.Error(t, err)
		})
	}
}

func TestParseBoundaryValueRejectsMalformedGeoJSON(t *testing.T) {
	_, _, err := ParseBoundaryValue(`{"coordinates":[]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type")

	_, _, err = ParseBoundaryValue(`{not json`)
	assert.Error(t, err)
}

func TestParseBoundaryValuePassesThroughPlainPath(t *testing.T) {
	// Plain paths keep the NodeODM behavior.
	source, passthrough, err := ParseBoundaryValue("/workspace/boundary.geojson")
	require.NoError(t, err)
	assert.False(t, source.IsSet())
	assert.Equal(t, "/workspace/boundary.geojson", passthrough)
}

func TestParseBoundaryValueRejectsOversizeGeoJSON(t *testing.T) {
	huge := `{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]],"pad":"` + strings.Repeat("x", maxBoundaryGeoJSONBytes) + `"}`
	_, _, err := ParseBoundaryValue(huge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestBoundaryFilePathSitsOutsideImagesDir(t *testing.T) {
	// The image cleanup would delete a boundary stored under images.
	path := BoundaryFilePath("odm-pipeline-abc12")
	assert.Equal(t, "/workspace/odm-pipeline-abc12/boundary.geojson", path)
	assert.NotContains(t, path, "/images/")
}

func TestApplyMaxConcurrencyFromCPULimit(t *testing.T) {
	cfg := &ODMPipelineConfig{ODMFlags: []string{"--dsm"}}
	cfg.ProcessResources.Limits.CPU = "7500m"
	applyMaxConcurrencyFromCPULimit(cfg)
	assert.Contains(t, cfg.ODMFlags, "--max-concurrency=7")
}

func TestApplyMaxConcurrencyFromCPULimitRespectsCaller(t *testing.T) {
	cfg := &ODMPipelineConfig{ODMFlags: []string{"--max-concurrency=8"}}
	cfg.ProcessResources.Limits.CPU = "48"
	applyMaxConcurrencyFromCPULimit(cfg)
	assert.Equal(t, []string{"--max-concurrency=8"}, cfg.ODMFlags)
}

func TestApplyMaxConcurrencyFromCPULimitIgnoresRequest(t *testing.T) {
	// CPU requests should not limit ODM concurrency.
	cfg := &ODMPipelineConfig{}
	cfg.ProcessResources.Requests.CPU = "75"
	applyMaxConcurrencyFromCPULimit(cfg)
	assert.Empty(t, cfg.ODMFlags)
}

func TestApplyMaxConcurrencyFromCPULimitFloorsAtOneWorker(t *testing.T) {
	// A fractional CPU limit still allows one worker.
	for _, cpu := range []string{"500m", "0.5"} {
		cfg := &ODMPipelineConfig{}
		cfg.ProcessResources.Limits.CPU = cpu
		applyMaxConcurrencyFromCPULimit(cfg)
		assert.Equal(t, []string{"--max-concurrency=1"}, cfg.ODMFlags, "cpu=%q", cpu)
	}
}

func TestApplyMaxConcurrencyFromCPULimitSkipsUnusableCPU(t *testing.T) {
	for _, cpu := range []string{"", "not-a-number", "0"} {
		cfg := &ODMPipelineConfig{}
		cfg.ProcessResources.Limits.CPU = cpu
		applyMaxConcurrencyFromCPULimit(cfg)
		assert.Empty(t, cfg.ODMFlags, "cpu=%q", cpu)
	}
}
