package workflows

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runShell runs script with shell, with stubDir prepended to PATH.
func runShell(t *testing.T, shell, stubDir, script string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(shell, append([]string{"-c", script, "test"}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeStub writes an executable shell stub named name into dir.
func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
}

func TestScripts_AreValidShell(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", []string{"--dsm"})
	cfg.Boundary = FileSource{S3Path: "s3://bucket/aoi.geojson"}
	cfg.Cameras = FileSource{Inline: testCameraModel}

	for name, tc := range map[string]struct{ shell, script string }{
		"download": {"/bin/sh", downloadScript(cfg, "{{workflow.name}}")},
		"process":  {"/bin/bash", processScript(cfg, "{{workflow.name}}")},
		"upload":   {"/bin/sh", uploadScript(cfg, "{{workflow.name}}")},
		"snapshot": {"/bin/sh", snapshotScript()},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command(tc.shell, "-n", "-c", tc.script).CombinedOutput()
			require.NoError(t, err, string(out))
		})
	}
}

func TestRenderScript_QuotesValuesLiterally(t *testing.T) {
	values := []string{
		"plain",
		"it's quoted",
		`$(touch pwned) ` + "`touch pwned`" + ` $HOME "double"`,
		"multi\nline\n",
		"",
	}
	for _, v := range values {
		dir := t.TempDir()
		script := renderScript(scriptVars{{"V", v}}, `printf '%s' "$V"`)
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err)
		assert.Equal(t, v, string(out))
		assert.NoFileExists(t, filepath.Join(dir, "pwned"))
	}
}

func TestSetupRclone_InfersProviderFromHost(t *testing.T) {
	for _, tc := range []struct {
		endpoint     string
		wantProvider string
		wantEndpoint bool
	}{
		{"", "AWS", false},
		{"s3.amazonaws.com", "AWS", false},
		{"https://s3.amazonaws.com", "AWS", false},
		{"s3.eu-west-1.amazonaws.com", "AWS", false},
		{"http://localhost:31102", "Minio", true},
		{"https://minio.internal:9000", "Minio", true},
		{"garage.example.org", "Minio", true},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("AWS_S3_ENDPOINT", tc.endpoint)
			script := renderScript(scriptVars{{"DIR", dir}}, rcloneLib, `set -eu; setup_rclone "$DIR"; echo "$RCLONE_CONFIG"`)

			out, err := runShell(t, "/bin/sh", dir, script)
			require.NoError(t, err, out)
			assert.Equal(t, filepath.Join(dir, "rclone.conf"), strings.TrimSpace(out))

			content, err := os.ReadFile(filepath.Join(dir, "rclone.conf"))
			require.NoError(t, err)
			assert.Contains(t, string(content), "provider = "+tc.wantProvider)
			if tc.wantEndpoint {
				assert.Contains(t, string(content), "endpoint = "+tc.endpoint)
				assert.Contains(t, string(content), "force_path_style = true")
				assert.Contains(t, string(content), "use_path_style = true")
			} else {
				assert.NotContains(t, string(content), "endpoint =")
				assert.NotContains(t, string(content), "force_path_style")
			}
		})
	}
}

// fetchInputFile runs fetch_input_file with an rclone stub ($4 is the target).
func fetchInputFile(t *testing.T, rcloneBody, s3Path, inline string) (dir, out string, err error) {
	t.Helper()
	dir = t.TempDir()
	writeStub(t, dir, "rclone", `echo "$3" > "$(dirname "$4")/rclone-src"`+"\n"+rcloneBody)

	script := renderScript(scriptVars{
		{"DEST", filepath.Join(dir, "boundary.geojson")},
		{"S3_PATH", s3Path},
		{"INLINE", inline},
	}, rcloneLib, inputFileLib, `set -e; fetch_input_file boundary "$DEST" "$S3_PATH" "$INLINE"`)
	out, err = runShell(t, "/bin/sh", dir, script)
	return dir, out, err
}

func TestFetchInputFile_S3Success(t *testing.T) {
	dir, out, err := fetchInputFile(t, `echo '{"type":"Polygon"}' > "$4"`, "s3://bucket/aoi.geojson", "")
	require.NoError(t, err, out)

	content, err := os.ReadFile(filepath.Join(dir, "boundary.geojson"))
	require.NoError(t, err)
	assert.Equal(t, "{\"type\":\"Polygon\"}\n", string(content))
	assert.NoFileExists(t, filepath.Join(dir, "boundary.geojson.part"))

	src, err := os.ReadFile(filepath.Join(dir, "rclone-src"))
	require.NoError(t, err)
	assert.Equal(t, "s3:bucket/aoi.geojson\n", string(src), "s3:// is converted to the rclone remote form")
	assert.Contains(t, out, "boundary written to")
}

// A requested file that fails to fetch must stop the job.
func TestFetchInputFile_S3FailuresAreFatal(t *testing.T) {
	for name, tc := range map[string]struct{ rcloneBody, wantErr string }{
		"missing": {"exit 3", "ERROR: boundary not found or unreadable at s3://bucket/aoi.geojson"},
		"prefix":  {`mkdir -p "$4"; touch "$4/inner"`, "ERROR: boundary path s3://bucket/aoi.geojson is a prefix, not a single object"},
		"empty":   {`: > "$4"`, "ERROR: boundary at s3://bucket/aoi.geojson is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, out, err := fetchInputFile(t, tc.rcloneBody, "s3://bucket/aoi.geojson", "")
			require.Error(t, err)
			assert.Contains(t, out, tc.wantErr)
			assert.NoFileExists(t, filepath.Join(dir, "boundary.geojson"))
			assert.NoDirExists(t, filepath.Join(dir, "boundary.geojson.part"))
		})
	}
}

// An earlier retry may leave a directory at the destination.
func TestFetchInputFile_RemovesRetryLeftovers(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "rclone", `[ -e "$4" ] && exit 9; echo ok > "$4"`)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "boundary.geojson", "stale"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "boundary.geojson.part"), 0o755))

	script := renderScript(scriptVars{{"DEST", filepath.Join(dir, "boundary.geojson")}},
		rcloneLib, inputFileLib, `set -e; fetch_input_file boundary "$DEST" s3://bucket/aoi.geojson ""`)
	out, err := runShell(t, "/bin/sh", dir, script)
	require.NoError(t, err, out)
	assert.FileExists(t, filepath.Join(dir, "boundary.geojson"))
}

func TestFetchInputFile_InlineWrittenVerbatim(t *testing.T) {
	// Old heredoc delimiter and shell syntax must be written as-is.
	inline := "{\"a\":\"it's $(touch pwned)\"}\nSCALEODM_BOUNDARY_EOF\n`touch pwned`"
	dir, out, err := fetchInputFile(t, "exit 1", "", inline)
	require.NoError(t, err, out)

	content, err := os.ReadFile(filepath.Join(dir, "boundary.geojson"))
	require.NoError(t, err)
	assert.Equal(t, inline+"\n", string(content))
	assert.NoFileExists(t, filepath.Join(dir, "pwned"))
	assert.NoFileExists(t, filepath.Join(dir, "rclone-src"), "inline must not call rclone")
	assert.Contains(t, out, "Writing inline ODM boundary")
}

func TestFetchInputFile_NothingRequestedIsNoop(t *testing.T) {
	dir, out, err := fetchInputFile(t, "exit 1", "", "")
	require.NoError(t, err, out)
	assert.Empty(t, out)
	assert.NoFileExists(t, filepath.Join(dir, "boundary.geojson"))
}

// Each ODM flag reaches run.py as one argument, unparsed by the shell.
func TestProcessScript_PassesFlagsAsArgv(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "python3", `printf '%s\n' "$@" > "$ARGS_FILE"`)
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "MPLCONFIGDIR"} {
		t.Setenv(name, filepath.Join(dir, strings.ToLower(name)))
	}
	t.Setenv("ARGS_FILE", filepath.Join(dir, "args"))

	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", nil)
	flags := []string{"--dsm", "--odm-option=a b", "--other=$(touch pwned)"}
	out, err := runShell(t, "/bin/bash", dir, processScript(cfg, "job-1"), flags...)
	require.NoError(t, err, out)

	args, err := os.ReadFile(filepath.Join(dir, "args"))
	require.NoError(t, err)
	assert.Equal(t, "-u\nrun.py\n--dsm\n--odm-option=a b\n--other=$(touch pwned)\n--project-path\n/workspace\njob-1\n", string(args))
	assert.NoFileExists(t, filepath.Join(dir, "pwned"))
	assert.DirExists(t, filepath.Join(dir, "home"), "some tools expect $HOME to exist")
}

func TestDownloadScript_Vars(t *testing.T) {
	cfg := NewDefaultODMConfig("test-project", "s3://bucket/input/", "s3://bucket/output/", nil)
	cfg.ExcludePaths = []string{"odm_orthophoto/**"}
	cfg.S3ScanDepth = 3

	script := downloadScript(cfg, "{{workflow.name}}")
	assert.Contains(t, script, "JOB_ID='{{workflow.name}}'\n")
	assert.Contains(t, script, "SRC_PATH='s3://bucket/input/'\n")
	assert.Contains(t, script, "MAX_DEPTH='3'\n")
	assert.Contains(t, script, "FILTER_RULES='- output/**\n")
	assert.Contains(t, script, "- odm_orthophoto/**\n")
	assert.Contains(t, script, `--filter-from "$FILTER_FILE"`)
	assert.NotContains(t, script, "BOUNDARY_S3_PATH=", "no boundary requested")
	assert.NotContains(t, script, "CAMERAS_INLINE=", "no cameras requested")
}

func TestDownloadScript_MaxDepthArgs(t *testing.T) {
	for _, tc := range []struct {
		depth string
		want  string
	}{
		{"1", "--max-depth 1"},
		{"3", "--max-depth 3"},
		{"0", ""},
	} {
		t.Run(tc.depth, func(t *testing.T) {
			dir := t.TempDir()
			writeStub(t, dir, "rclone", `echo "$*" >> "$RCLONE_LOG"; touch "$3/a.jpg"`)
			t.Setenv("RCLONE_LOG", filepath.Join(dir, "rclone.log"))

			script := renderScript(scriptVars{
				{"ATTEMPT", "0"},
				{"WORKSPACE", dir},
				{"JOB_ID", "job-1"},
				{"SRC_PATH", "s3://bucket/input/"},
				{"FILTER_RULES", "- *\n"},
				{"MAX_DEPTH", tc.depth},
			}, rcloneLib, inputFileLib, downloadSh)
			out, err := runShell(t, "/bin/sh", dir, script)
			require.NoError(t, err, out)

			log, err := os.ReadFile(filepath.Join(dir, "rclone.log"))
			require.NoError(t, err)
			wantCmd := "copy s3:bucket/input/ " + filepath.Join(dir, "job-1/images") +
				" --filter-from " + filepath.Join(dir, "job-1/.rclone/filters.txt")
			if tc.want != "" {
				wantCmd += " " + tc.want
			}
			assert.Equal(t, wantCmd+"\n", string(log))
		})
	}
}

func TestDownloadScript_FailsOnExtractionErrors(t *testing.T) {
	assert.NotContains(t, downloadSh, "|| true")
	assert.Contains(t, downloadSh, "ERROR: Failed to extract zip archive")
	assert.Contains(t, downloadSh, "ERROR: Failed to extract tar archive")
}

func TestDownloadScript_DeletesArchivesOnlyAfterSuccess(t *testing.T) {
	zipExtract := `if ! unzip -q -o -j "$zipfile" -d "$(dirname "$zipfile")"; then`
	assert.Greater(t, strings.Index(downloadSh, `rm -f "$zipfile"`), strings.Index(downloadSh, zipExtract))

	tarExtract := `if ! tar --no-same-owner --no-same-permissions --no-absolute-filenames --transform='s|.*/||' -xf "$tarfile"`
	assert.Greater(t, strings.Index(downloadSh, `rm -f "$tarfile"`), strings.Index(downloadSh, tarExtract))
}

func TestDownloadScript_AvoidsFindPipeSubshellForArchiveDetection(t *testing.T) {
	assert.Contains(t, downloadSh, "while IFS= read -r zipfile;")
	assert.Contains(t, downloadSh, "while IFS= read -r tarfile;")
	assert.NotContains(t, downloadSh, "| while read")
}

func TestUploadScript_TestWriteFailureSurvivesErrexit(t *testing.T) {
	assert.Contains(t, uploadSh, `if TEST_OUTPUT=$(rclone copyto "$TEST_FILE" "$TEST_OBJECT" 2>&1); then`)
	assert.Contains(t, uploadSh, "else\n  TEST_EXIT_CODE=$?")
	assert.Contains(t, uploadSh, "This might be a false positive. Continuing with actual upload...")
}

func TestUploadScript_ExcludesUndistortedOpenSfMImages(t *testing.T) {
	assert.Contains(t, uploadSh, `--exclude ".rclone/**"`)
	assert.Contains(t, uploadSh, `--exclude "opensfm/undistorted/**"`)
	assert.Contains(t, uploadSh, `--exclude "**/opensfm/undistorted/**"`)
	assert.Contains(t, uploadSh, `rclone copy "$SRC_DIR" "$S3_REMOTE"`)
}
