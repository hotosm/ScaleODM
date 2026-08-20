package workflows

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// shellSafeBoundaryPattern applies the API path rules to S3 boundaries.
var shellSafeBoundaryPattern = regexp.MustCompile(`^[a-zA-Z0-9\-_./=:@]+$`)

const (
	// BoundaryOptionName is the ODM boundary option.
	BoundaryOptionName = "boundary"

	// boundaryFileName names the boundary file stored outside the pruned images directory.
	boundaryFileName = "boundary.geojson"

	// maxBoundaryGeoJSONBytes keeps inline boundaries small enough for the workflow spec.
	maxBoundaryGeoJSONBytes = 256 * 1024
)

// BoundarySource holds a boundary that must be written to the workspace.
type BoundarySource struct {
	// GeoJSON is a compacted, single-line GeoJSON document.
	GeoJSON string
	// S3Path is an s3://bucket/key URL for a GeoJSON object.
	S3Path string
}

// IsSet reports whether a boundary needs materialising for this task.
func (b BoundarySource) IsSet() bool {
	return b.GeoJSON != "" || b.S3Path != ""
}

// BoundaryFilePath returns the boundary path in the shared workspace.
func BoundaryFilePath(jobID string) string {
	return fmt.Sprintf("/workspace/%s/%s", jobID, boundaryFileName)
}

// ParseBoundaryValue separates workspace boundaries from plain paths.
func ParseBoundaryValue(raw string) (BoundarySource, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return BoundarySource{}, "", fmt.Errorf("boundary is empty")
	}

	if strings.HasPrefix(value, "s3://") {
		if err := validateBoundaryS3Path(value); err != nil {
			return BoundarySource{}, "", err
		}
		return BoundarySource{S3Path: value}, "", nil
	}

	if strings.HasPrefix(value, "{") {
		compacted, err := compactBoundaryGeoJSON(value)
		if err != nil {
			return BoundarySource{}, "", err
		}
		return BoundarySource{GeoJSON: compacted}, "", nil
	}

	// Leave plain paths to the API's normal validation.
	return BoundarySource{}, value, nil
}

func validateBoundaryS3Path(value string) error {
	rest := strings.TrimPrefix(value, "s3://")
	if rest == "" || strings.HasPrefix(rest, "/") {
		return fmt.Errorf("boundary s3 path must be s3://bucket/key")
	}
	if !strings.Contains(rest, "/") {
		return fmt.Errorf("boundary s3 path must name an object, not just a bucket")
	}
	if strings.HasSuffix(rest, "/") {
		return fmt.Errorf("boundary s3 path must name an object, not a prefix")
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("boundary s3 path must not contain '..'")
	}
	// The path is embedded in the rclone shell script.
	if !shellSafeBoundaryPattern.MatchString(value) {
		return fmt.Errorf("boundary s3 path contains invalid characters")
	}
	return nil
}

// compactBoundaryGeoJSON validates and compacts JSON for the shell script.
func compactBoundaryGeoJSON(value string) (string, error) {
	if len(value) > maxBoundaryGeoJSONBytes {
		return "", fmt.Errorf("boundary GeoJSON exceeds %d bytes", maxBoundaryGeoJSONBytes)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		return "", fmt.Errorf("boundary is not valid JSON: %w", err)
	}

	if err := validateSinglePolygon(doc); err != nil {
		return "", err
	}

	compacted, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("could not re-encode boundary GeoJSON: %w", err)
	}
	return string(compacted), nil
}

// validateSinglePolygon enforces ODM's single Polygon boundary format.
func validateSinglePolygon(doc map[string]any) error {
	switch geomType, _ := doc["type"].(string); geomType {
	case "":
		return fmt.Errorf("boundary GeoJSON has no \"type\" member")
	case "Polygon":
		return validatePolygonRings(doc)
	case "Feature":
		return validateFeatureIsPolygon(doc)
	case "FeatureCollection":
		features, _ := doc["features"].([]any)
		if len(features) != 1 {
			return fmt.Errorf(
				"boundary FeatureCollection must hold exactly one feature, found %d; ODM rejects anything else",
				len(features),
			)
		}
		feature, ok := features[0].(map[string]any)
		if !ok {
			return fmt.Errorf("boundary feature is not a JSON object")
		}
		return validateFeatureIsPolygon(feature)
	default:
		return unsupportedBoundaryGeometry(geomType)
	}
}

func validateFeatureIsPolygon(feature map[string]any) error {
	geometry, ok := feature["geometry"].(map[string]any)
	if !ok {
		return fmt.Errorf("boundary feature has no geometry")
	}
	if geomType, _ := geometry["type"].(string); geomType != "Polygon" {
		return unsupportedBoundaryGeometry(geomType)
	}
	return validatePolygonRings(geometry)
}

// validatePolygonRings checks the first ring used by ODM.
func validatePolygonRings(geometry map[string]any) error {
	rings, _ := geometry["coordinates"].([]any)
	if len(rings) == 0 {
		return fmt.Errorf("boundary Polygon has no rings")
	}

	ring, _ := rings[0].([]any)
	if len(ring) == 0 {
		return fmt.Errorf("boundary Polygon ring has no coordinates")
	}

	for _, point := range ring {
		coords, _ := point.([]any)
		if len(coords) < 2 {
			return fmt.Errorf("boundary Polygon coordinates must be [x, y] pairs")
		}
		for _, ordinate := range coords {
			if _, ok := ordinate.(float64); !ok {
				return fmt.Errorf("boundary Polygon coordinates must be numbers")
			}
		}
	}
	return nil
}

func unsupportedBoundaryGeometry(geomType string) error {
	return fmt.Errorf(
		"boundary geometry type %q is not usable; ODM needs a single Polygon",
		geomType,
	)
}
