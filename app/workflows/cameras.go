package workflows

import (
	"fmt"
	"strings"
)

const (
	// CamerasOptionName is the ODM cameras option.
	CamerasOptionName = "cameras"

	// Avoid ODM overwriting the input as cameras.json.
	camerasFileName = "cameras-input.json"

	maxCamerasJSONBytes = 64 * 1024

	// Minimum ID parts required by opendm/camera.py.
	minCameraIDParts = 6
)

// CamerasFilePath returns the calibration path in the shared workspace.
func CamerasFilePath(jobID string) string {
	return WorkspaceFilePath(jobID, camerasFileName)
}

// ParseCamerasValue separates workspace calibrations from plain paths.
func ParseCamerasValue(raw string) (FileSource, string, error) {
	return parseFileSource(raw, CamerasOptionName)
}

// validateCameraModels checks structure; OpenSfM validates projection types.
func validateCameraModels(doc map[string]any) error {
	if len(doc) == 0 {
		return fmt.Errorf("cameras JSON has no camera entries")
	}

	if geomType, ok := doc["type"].(string); ok {
		return fmt.Errorf("cameras JSON looks like GeoJSON (%q), not an ODM cameras.json", geomType)
	}

	for key, raw := range doc {
		// ODM silently ignores calibration with shorter IDs.
		if len(strings.Split(key, " ")) < minCameraIDParts {
			return fmt.Errorf(
				"camera ID %q must have at least %d space-separated parts, e.g. %q",
				key, minCameraIDParts, "dji fc8482 4032 3024 brown 0.6666",
			)
		}

		camera, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("camera entry %q is not a JSON object", key)
		}
		if err := validateCameraModel(key, camera); err != nil {
			return err
		}
	}
	return nil
}

func validateCameraModel(key string, camera map[string]any) error {
	if projection, ok := camera["projection_type"].(string); !ok || strings.TrimSpace(projection) == "" {
		return fmt.Errorf("camera %q has no projection_type", key)
	}

	for _, dimension := range []string{"width", "height"} {
		value, ok := camera[dimension].(float64)
		if !ok {
			return fmt.Errorf("camera %q has no numeric %s", key, dimension)
		}
		if value <= 0 || value != float64(int64(value)) {
			return fmt.Errorf("camera %q has an invalid %s (%v)", key, dimension, value)
		}
	}

	// OpenSfM validates projection-specific focal fields; check pairing here.
	_, hasX := camera["focal_x"]
	_, hasY := camera["focal_y"]
	if hasX != hasY {
		return fmt.Errorf("camera %q must set focal_x and focal_y together", key)
	}

	return nil
}
