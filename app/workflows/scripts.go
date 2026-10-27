package workflows

import (
	_ "embed"
	"strconv"
	"strings"

	"github.com/hotosm/scaleodm/app/s3"
)

// Container scripts are static files in scripts/. Per-job values are passed
// as quoted variable assignments prepended to the script, never spliced in.

var (
	//go:embed scripts/rclone.sh
	rcloneLib string
	//go:embed scripts/input_file.sh
	inputFileLib string
	//go:embed scripts/download.sh
	downloadSh string
	//go:embed scripts/process.sh
	processSh string
	//go:embed scripts/upload.sh
	uploadSh string
	//go:embed scripts/snapshot.sh
	snapshotSh string
)

// workspaceDir is where the workspace volume is mounted in each container.
const workspaceDir = "/workspace"

// scriptVars are NAME=value pairs, in order.
type scriptVars [][2]string

// shellQuote makes s a literal shell string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderScript prepends vars as assignments to the script parts.
func renderScript(vars scriptVars, parts ...string) string {
	var b strings.Builder
	for _, kv := range vars {
		b.WriteString(kv[0] + "=" + shellQuote(kv[1]) + "\n")
	}
	for _, part := range parts {
		b.WriteString("\n" + part)
	}
	return b.String()
}

func downloadScript(cfg *ODMPipelineConfig, jobID string) string {
	vars := scriptVars{
		{"ATTEMPT", "{{retries}}"},
		{"WORKSPACE", workspaceDir},
		{"JOB_ID", jobID},
		{"SRC_PATH", cfg.ReadS3Path},
		{"FILTER_RULES", s3.DownloadFilterRules(cfg.ExcludePaths)},
		{"MAX_DEPTH", strconv.Itoa(cfg.S3ScanDepth)},
	}
	vars = appendInputFileVars(vars, "BOUNDARY", BoundaryFilePath(jobID), cfg.Boundary)
	vars = appendInputFileVars(vars, "CAMERAS", CamerasFilePath(jobID), cfg.Cameras)
	return renderScript(vars, rcloneLib, inputFileLib, downloadSh)
}

// appendInputFileVars adds vars for fetch_input_file if source is set.
func appendInputFileVars(vars scriptVars, prefix, dest string, source FileSource) scriptVars {
	if !source.IsSet() {
		return vars
	}
	return append(vars,
		[2]string{prefix + "_DEST", dest},
		[2]string{prefix + "_S3_PATH", source.S3Path},
		[2]string{prefix + "_INLINE", source.Inline},
	)
}

func processScript(cfg *ODMPipelineConfig, jobID string) string {
	return renderScript(scriptVars{
		{"ATTEMPT", "{{retries}}"},
		{"WORKSPACE", workspaceDir},
		{"JOB_ID", jobID},
		{"ODM_PROJECT_ID", cfg.ODMProjectID},
	}, processSh)
}

func uploadScript(cfg *ODMPipelineConfig, jobID string) string {
	return renderScript(scriptVars{
		{"ATTEMPT", "{{retries}}"},
		{"WORKSPACE", workspaceDir},
		{"JOB_ID", jobID},
		{"DEST_PATH", cfg.WriteS3Path},
	}, rcloneLib, uploadSh)
}

func snapshotScript() string {
	return renderScript(scriptVars{{"WORKSPACE", workspaceDir}}, snapshotSh)
}
