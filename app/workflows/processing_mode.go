package workflows

import (
	"fmt"
	"strings"
)

// ProcessingMode selects the high-level pipeline shape for a task submission.
// The mode informs both pipeline behaviour and resource sizing - pick the one
// that matches the dataset size and post-processing needs.
//
//   - "standard" (default): the regular ODM pipeline (download → process →
//     upload). readS3Path is downloaded with the configured s3ScanDepth and
//     all matched imagery is fed into a single ODM run. Use a shallow depth
//     for one task's imagery dir, or a deeper depth to roll up several task
//     subdirs (e.g. projectid/taskid/images) into one run.
//   - "merge-existing" (reserved, 501): given a project root that already
//     contains per-task ODM outputs, run only the merge half of split-merge
//     to stitch the existing orthos/DEMs/point-clouds into a single set of
//     products. Much cheaper than re-running per-task processing from raw
//     imagery.
//   - "thermal" (reserved, 501): thermal imagery pipeline with a dedicated
//     pre-processing stage (radiometric handling, alignment) before ODM.
//   - "city-scale" (reserved, 501): large-area projects (>40 km²) that
//     iteratively fan out from a central task with corrective alignment via
//     prior LAZ point clouds and a final pass against a global DEM.
const (
	ProcessingModeStandard      = "standard"
	ProcessingModeMergeExisting = "merge-existing"
	ProcessingModeThermal       = "thermal"
	ProcessingModeCityScale     = "city-scale"
)

// S3 scan-depth controls how deep the rclone download walks beneath
// readS3Path. Depth 1 only matches files in the given directory itself -
// the default and right choice for inputs like
// "s3://bucket/project-id/task-id/images/". Higher values let callers point
// readS3Path at a higher-level prefix (e.g. "s3://bucket/project-id/") and
// still pick up imagery nested inside per-task subdirs.
const (
	DefaultS3ScanDepth = 1
	MaxS3ScanDepth     = 10
)

// IsImplementedProcessingMode reports whether mode has a working pipeline today.
func IsImplementedProcessingMode(mode string) bool {
	switch mode {
	case ProcessingModeStandard:
		return true
	default:
		return false
	}
}

// ValidateS3ScanDepth bounds the user-supplied scan depth to a sane range.
// Returns the depth to use plus an error when the input is out of range.
// A value of 0 is treated as "unset" and resolved to DefaultS3ScanDepth so
// the wire shape can use a pointer/optional and still be ergonomic.
func ValidateS3ScanDepth(depth int) (int, error) {
	if depth == 0 {
		return DefaultS3ScanDepth, nil
	}
	if depth < 1 {
		return 0, fmt.Errorf("s3ScanDepth must be >= 1 (got %d)", depth)
	}
	if depth > MaxS3ScanDepth {
		return 0, fmt.Errorf("s3ScanDepth must be <= %d (got %d)", MaxS3ScanDepth, depth)
	}
	return depth, nil
}

// IsReservedProcessingMode reports whether mode is a recognised but
// unimplemented pipeline. Callers should respond with 501 in that case so
// clients can probe support without ambiguity.
func IsReservedProcessingMode(mode string) bool {
	switch mode {
	case ProcessingModeMergeExisting, ProcessingModeThermal, ProcessingModeCityScale:
		return true
	default:
		return false
	}
}

// DefaultProjectExcludes is the canonical rclone-style exclude list applied
// when useDefaultExcludes is true. The directory entries cover every standard
// ODM output dir plus split-merge artefacts; the basename entries protect the
// archive-extraction step from re-ingesting a prior all.zip (which would be
// flattened with junk-paths into the input dir and ruin the run).
var DefaultProjectExcludes = []string{
	"odm_orthophoto/**",
	"odm_dem/**",
	"odm_dem_tiles/**",
	"odm_texturing/**",
	"odm_texturing_25d/**",
	"odm_meshing/**",
	"odm_filterpoints/**",
	"odm_georeferencing/**",
	"odm_report/**",
	"images_resize/**",
	"entwine_pointcloud/**",
	"mapproxy/**",
	"submodels/**",
	"*-output/**",
	"thumbs/**",
	"**/thumbs/**",
	"all.zip",
	"**/all.zip",
}

// ComposeExcludePatterns returns the final exclude list to apply for a task.
// Defaults are prepended only when useDefaults is true; user patterns are
// always appended. Order matters because rclone applies filters top-to-bottom,
// but excludes are commutative within the exclude block.
func ComposeExcludePatterns(useDefaults bool, userPatterns []string) []string {
	out := make([]string, 0, len(DefaultProjectExcludes)+len(userPatterns))
	if useDefaults {
		out = append(out, DefaultProjectExcludes...)
	}
	out = append(out, userPatterns...)
	return out
}

// ValidateExcludePattern checks that a user-supplied rclone filter pattern is
// safe to embed in a --filter-from file. It rejects path traversal, absolute
// paths, embedded newlines (which would break the filter file format), and
// shell metacharacters that have no business in a glob.
//
// Note: shell injection is structurally prevented because patterns are written
// to a tmpfile and passed via --filter-from, not through the shell. This
// validation is a defence-in-depth check for malformed input.
func ValidateExcludePattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("exclude pattern is empty")
	}
	if strings.ContainsAny(pattern, "\n\r") {
		return fmt.Errorf("exclude pattern must not contain newlines")
	}
	if strings.Contains(pattern, "..") {
		return fmt.Errorf("exclude pattern must not contain '..'")
	}
	if strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("exclude pattern must be relative (no leading '/')")
	}
	if strings.ContainsAny(pattern, "`$;&|<>\"'\\") {
		return fmt.Errorf("exclude pattern contains disallowed characters")
	}
	return nil
}
