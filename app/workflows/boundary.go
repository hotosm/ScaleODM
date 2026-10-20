package workflows

import (
	"fmt"
)

const (
	// BoundaryOptionName is the ODM boundary option.
	BoundaryOptionName = "boundary"

	// boundaryFileName names the boundary file stored outside the pruned images directory.
	boundaryFileName = "boundary.geojson"

	// maxBoundaryGeoJSONBytes keeps inline boundaries small enough for the workflow spec.
	maxBoundaryGeoJSONBytes = 256 * 1024
)

// BoundaryFilePath returns the boundary path in the shared workspace.
func BoundaryFilePath(jobID string) string {
	return WorkspaceFilePath(jobID, boundaryFileName)
}

// ParseBoundaryValue separates workspace boundaries from plain paths.
func ParseBoundaryValue(raw string) (FileSource, string, error) {
	return parseFileSource(raw, BoundaryOptionName)
}

// validateSinglePolygon enforces ODM's single Polygon format: bare Polygon, Feature, or
// single-feature FeatureCollection. MultiPolygon/GeometryCollection aren't handled by ODM.
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
