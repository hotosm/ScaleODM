package workflows

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// shellSafeSourcePattern applies the API path rules to S3 object references.
var shellSafeSourcePattern = regexp.MustCompile(`^[a-zA-Z0-9\-_./=:@]+$`)

// FileSource identifies an ODM input document.
type FileSource struct {
	// Inline is a compacted, single-line JSON document.
	Inline string
	// S3Path is an s3://bucket/key URL for a JSON object.
	S3Path string
}

// IsSet reports whether a file source is provided.
func (f FileSource) IsSet() bool {
	return f.Inline != "" || f.S3Path != ""
}

// WorkspaceFilePath keeps inputs outside the pruned images directory.
func WorkspaceFilePath(jobID, fileName string) string {
	return fmt.Sprintf("/workspace/%s/%s", jobID, fileName)
}

// documentRules defines inline limits and validators.
var documentRules = map[string]struct {
	maxInlineBytes int
	validate       func(map[string]any) error
}{
	BoundaryOptionName: {maxBoundaryGeoJSONBytes, validateSinglePolygon},
	CamerasOptionName:  {maxCamerasJSONBytes, validateCameraModels},
}

// MaxRemoteDocumentBytes bounds memory use when reading S3 documents.
const MaxRemoteDocumentBytes = 8 * 1024 * 1024

// ValidateDocument checks S3 content without workflow inline size limits.
func ValidateDocument(name string, data []byte) error {
	rules, ok := documentRules[name]
	if !ok {
		return nil
	}
	_, err := decodeDocument(string(data), name, rules.validate)
	return err
}

// parseFileSource accepts S3 URLs, inline JSON, or plain paths.
func parseFileSource(raw, name string) (FileSource, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return FileSource{}, "", fmt.Errorf("%s is empty", name)
	}

	if strings.HasPrefix(value, "s3://") {
		if err := validateSourceS3Path(value, name); err != nil {
			return FileSource{}, "", err
		}
		return FileSource{S3Path: value}, "", nil
	}

	if strings.HasPrefix(value, "{") {
		rules := documentRules[name]
		if len(value) > rules.maxInlineBytes {
			return FileSource{}, "", fmt.Errorf("%s JSON exceeds %d bytes", name, rules.maxInlineBytes)
		}
		// One-line JSON cannot terminate the quoted heredoc.
		compacted, err := decodeDocument(value, name, rules.validate)
		if err != nil {
			return FileSource{}, "", err
		}
		return FileSource{Inline: compacted}, "", nil
	}

	// Leave plain paths to the API's normal validation.
	return FileSource{}, value, nil
}

func validateSourceS3Path(value, name string) error {
	rest := strings.TrimPrefix(value, "s3://")
	if rest == "" || strings.HasPrefix(rest, "/") {
		return fmt.Errorf("%s s3 path must be s3://bucket/key", name)
	}
	if !strings.Contains(rest, "/") {
		return fmt.Errorf("%s s3 path must name an object, not just a bucket", name)
	}
	if strings.HasSuffix(rest, "/") {
		return fmt.Errorf("%s s3 path must name an object, not a prefix", name)
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("%s s3 path must not contain '..'", name)
	}
	// The path is embedded in the rclone shell script.
	if !shellSafeSourcePattern.MatchString(value) {
		return fmt.Errorf("%s s3 path contains invalid characters", name)
	}
	return nil
}

// decodeDocument validates JSON and returns it compacted onto one line.
func decodeDocument(value, name string, validate func(map[string]any) error) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		return "", fmt.Errorf("%s is not valid JSON: %w", name, err)
	}

	if err := validate(doc); err != nil {
		return "", err
	}

	compacted, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("could not re-encode %s JSON: %w", name, err)
	}
	return string(compacted), nil
}
