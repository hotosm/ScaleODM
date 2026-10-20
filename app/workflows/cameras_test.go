package workflows

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCameraModel = `{"dji fc8482 4032 3024 brown 0.6666":{"projection_type":"brown","width":4032,"height":3024,` +
	`"focal_x":0.74018822,"focal_y":0.74018822,"c_x":-0.00160016,"c_y":-0.00236154,` +
	`"k1":0.09751959,"k2":-0.15519216,"p1":0.00032039,"p2":0.00003902,"k3":0.09725945}}`

func TestParseCamerasValueS3Path(t *testing.T) {
	source, passthrough, err := ParseCamerasValue("s3://dronetm-prod/calibration/cameras.json")
	require.NoError(t, err)
	assert.Equal(t, "s3://dronetm-prod/calibration/cameras.json", source.S3Path)
	assert.Empty(t, source.Inline)
	assert.Empty(t, passthrough)
	assert.True(t, source.IsSet())
}

func TestParseCamerasValueRejectsBadS3Paths(t *testing.T) {
	for name, value := range map[string]string{
		"bucket only": "s3://dronetm-prod",
		"prefix":      "s3://dronetm-prod/calibration/",
		"traversal":   "s3://dronetm-prod/../etc/passwd",
		"shell chars": "s3://dronetm-prod/$(id).json",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseCamerasValue(value)
			assert.Error(t, err)
		})
	}
}

func TestParseCamerasValueCompactsInlineJSON(t *testing.T) {
	pretty := `{
	  "dji fc8482 4032 3024 brown 0.6666": {
	    "projection_type": "brown",
	    "width": 4032,
	    "height": 3024,
	    "focal_x": 0.74018822,
	    "focal_y": 0.74018822,
	    "c_x": -0.00160016,
	    "c_y": -0.00236154,
	    "k1": 0.09751959,
	    "k2": -0.15519216,
	    "p1": 0.00032039,
	    "p2": 0.00003902,
	    "k3": 0.09725945
	  }
	}`

	source, passthrough, err := ParseCamerasValue(pretty)
	require.NoError(t, err)
	assert.Empty(t, passthrough)
	assert.Empty(t, source.S3Path)

	// One-line JSON cannot terminate the quoted heredoc.
	assert.NotContains(t, source.Inline, "\n")
	assert.Contains(t, source.Inline, `"dji fc8482 4032 3024 brown 0.6666"`)
}

func TestParseCamerasValueAcceptsV2PrefixedKey(t *testing.T) {
	source, _, err := ParseCamerasValue(strings.Replace(testCameraModel, `{"dji`, `{"v2 dji`, 1))
	require.NoError(t, err)
	assert.Contains(t, source.Inline, `"v2 dji fc8482 4032 3024 brown 0.6666"`)
}

// ODM silently ignores calibration with shorter IDs.
func TestParseCamerasValueRejectsShortCameraID(t *testing.T) {
	for name, key := range map[string]string{
		"single word": "cam",
		"five parts":  "dji fc8482 4032 3024 brown",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseCamerasValue(`{"` + key + `":{"projection_type":"brown","width":4032,"height":3024,"focal_x":0.74,"focal_y":0.74}}`)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "space-separated")
		})
	}
}

// Panoramas do not require focal fields.
func TestParseCamerasValueAcceptsPanorama(t *testing.T) {
	source, _, err := ParseCamerasValue(`{"insta360 x3 5760 2880 spherical 0":{"projection_type":"spherical","width":5760,"height":2880}}`)
	require.NoError(t, err)
	assert.True(t, source.IsSet())
}

// OpenSfM requires both focal fields when either is present.
func TestParseCamerasValueRejectsUnpairedFocalXY(t *testing.T) {
	for name, fields := range map[string]string{
		"focal_x only": `"focal_x":0.74`,
		"focal_y only": `"focal_y":0.74`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseCamerasValue(`{"dji fc8482 4032 3024 brown 0.6666":{"projection_type":"brown","width":4032,"height":3024,` + fields + `}}`)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "focal_x and focal_y together")
		})
	}
}

func TestParseCamerasValueRejectsGeoJSON(t *testing.T) {
	_, _, err := ParseCamerasValue(`{"type":"Polygon","coordinates":[[[115.45,-8.35],[115.48,-8.35],[115.48,-8.39],[115.45,-8.35]]]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GeoJSON")
}

func TestParseCamerasValueRejectsMalformedModels(t *testing.T) {
	for name, value := range map[string]string{
		"no entries":       `{}`,
		"entry not object": `{"dji fc8482 4032 3024 brown 0.6666":"brown"}`,
		"no projection":    `{"a b c d e f":{"width":4032,"height":3024}}`,
		"blank projection": `{"a b c d e f":{"projection_type":"  ","width":4032,"height":3024}}`,
		"no width":         `{"a b c d e f":{"projection_type":"brown","height":3024}}`,
		"string width":     `{"a b c d e f":{"projection_type":"brown","width":"4032","height":3024}}`,
		"zero height":      `{"a b c d e f":{"projection_type":"brown","width":4032,"height":0}}`,
		"fractional width": `{"a b c d e f":{"projection_type":"brown","width":4032.5,"height":3024}}`,
		"not json":         `{not json`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseCamerasValue(value)
			assert.Error(t, err)
		})
	}
}

// Perspective and fisheye models use focal instead of focal_x/focal_y.
func TestParseCamerasValueAcceptsNonBrownProjections(t *testing.T) {
	source, _, err := ParseCamerasValue(`{"dji fc350 4000 3000 fisheye 0.5":{"projection_type":"fisheye","width":4000,"height":3000,"focal":0.5,"k1":0.0,"k2":0.0}}`)
	require.NoError(t, err)
	assert.True(t, source.IsSet())
}

func TestParseCamerasValueAcceptsMultipleCameras(t *testing.T) {
	two := `{"dji fc8482 4032 3024 brown 0.6666":{"projection_type":"brown","width":4032,"height":3024,"focal_x":0.74,"focal_y":0.74},` +
		`"dji fc8482 4032 3024 brown 1.3333":{"projection_type":"brown","width":4032,"height":3024,"focal_x":1.48,"focal_y":1.48}}`
	source, _, err := ParseCamerasValue(two)
	require.NoError(t, err)
	assert.Contains(t, source.Inline, "1.3333")
}

func TestParseCamerasValuePassesThroughPlainPath(t *testing.T) {
	source, passthrough, err := ParseCamerasValue("/workspace/cameras.json")
	require.NoError(t, err)
	assert.False(t, source.IsSet())
	assert.Equal(t, "/workspace/cameras.json", passthrough)
}

func TestParseCamerasValueRejectsOversizeJSON(t *testing.T) {
	huge := strings.TrimSuffix(testCameraModel, "}") + `,"pad":"` + strings.Repeat("x", maxCamerasJSONBytes) + `"}`
	_, _, err := ParseCamerasValue(huge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestParseCamerasValueRejectsEmpty(t *testing.T) {
	_, _, err := ParseCamerasValue("   ")
	assert.Error(t, err)
}

func TestCamerasFilePathDoesNotCollideWithODMOutput(t *testing.T) {
	path := CamerasFilePath("odm-pipeline-abc12")
	assert.Equal(t, "/workspace/odm-pipeline-abc12/cameras-input.json", path)
	assert.NotContains(t, path, "/images/")
	assert.False(t, strings.HasSuffix(path, "/cameras.json"))
}

func TestValidateDocumentSkipsInlineSizeCap(t *testing.T) {
	padded := strings.Replace(testCameraModel, `"projection_type":"brown",`,
		`"projection_type":"brown","note":"`+strings.Repeat("x", maxCamerasJSONBytes)+`",`, 1)

	_, _, inlineErr := ParseCamerasValue(padded)
	require.Error(t, inlineErr)

	assert.NoError(t, ValidateDocument(CamerasOptionName, []byte(padded)))
}

func TestValidateDocumentAppliesContentRules(t *testing.T) {
	assert.NoError(t, ValidateDocument(CamerasOptionName, []byte(testCameraModel)))
	assert.Error(t, ValidateDocument(CamerasOptionName, []byte(`{"cam":{"projection_type":"brown","width":1,"height":1}}`)))
	assert.Error(t, ValidateDocument(BoundaryOptionName, []byte(`{"type":"MultiPolygon","coordinates":[]}`)))
	assert.NoError(t, ValidateDocument("unknown-option", []byte(`not even json`)))
}
