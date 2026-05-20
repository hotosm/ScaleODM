package s3

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hotosm/scaleodm/testutil"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetS3Client_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = GetS3Client()
	})
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  string
		wantError bool
	}{
		{name: "aws host only", input: "s3.amazonaws.com", expected: "s3.amazonaws.com"},
		{name: "http endpoint with path", input: "http://localhost:9000/api/v1", expected: "http://localhost:9000"},
		{name: "https endpoint with query", input: "https://minio.example.com:9443?foo=bar", expected: "https://minio.example.com:9443"},
		{name: "host with path no scheme", input: "garage.local:3900/path", expected: "garage.local:3900"},
		{name: "trim whitespace", input: "  https://s3.example.com/  ", expected: "https://s3.example.com"},
		{name: "empty endpoint", input: "", wantError: true},
		{name: "missing host", input: "https://", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeEndpoint(tt.input)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestBucketLookupForEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		expected minio.BucketLookupType
	}{
		{name: "aws global endpoint", endpoint: "s3.amazonaws.com", expected: minio.BucketLookupAuto},
		{name: "aws regional endpoint", endpoint: "s3.us-east-1.amazonaws.com", expected: minio.BucketLookupAuto},
		{name: "custom endpoint", endpoint: "localhost:3900", expected: minio.BucketLookupPath},
		{name: "custom endpoint with port", endpoint: "minio.internal:9000", expected: minio.BucketLookupPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, bucketLookupForEndpoint(tt.endpoint))
		})
	}
}

func TestGeneratePresignedURL_UsesPathStyleForCustomEndpoint(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "location") {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer testServer.Close()

	testServerURL, err := url.Parse(testServer.URL)
	require.NoError(t, err)

	client, err := GetS3ClientWithCredentials(testServerURL.String(), "test-access", "test-secret", "")
	require.NoError(t, err)

	presigned, err := GeneratePresignedURL(
		context.Background(),
		client,
		"s3://scaleodm-test/project/output/",
		"all.zip",
		time.Hour,
	)
	require.NoError(t, err)

	parsed, err := url.Parse(presigned)
	require.NoError(t, err)

	assert.Equal(t, testServerURL.Host, parsed.Host)
	assert.Equal(t, "/scaleodm-test/project/output/all.zip", parsed.Path)
}

func TestRcloneS3ConfigSnippet_CustomEndpointUsesMinioAndPathStyle(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "snippet.sh")
	configPath := filepath.Join(tmpDir, "rclone.conf")

	script := "#!/bin/sh\nset -eu\nexport RCLONE_CONFIG=\"" + configPath + "\"\nexport AWS_S3_ENDPOINT=\"http://example.local:9000\"\n" + rcloneS3ConfigSnippet() + "\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	cmd := exec.Command("/bin/sh", scriptPath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	contentBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(contentBytes)

	assert.Contains(t, content, "provider = Minio")
	assert.Contains(t, content, "endpoint = http://example.local:9000")
	assert.Contains(t, content, "force_path_style = true")
	assert.Contains(t, content, "use_path_style = true")
}

func TestRcloneS3ConfigSnippet_NoEndpointKeepsAWSProvider(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "snippet.sh")
	configPath := filepath.Join(tmpDir, "rclone.conf")

	script := "#!/bin/sh\nset -eu\nexport RCLONE_CONFIG=\"" + configPath + "\"\nunset AWS_S3_ENDPOINT\n" + rcloneS3ConfigSnippet() + "\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	cmd := exec.Command("/bin/sh", scriptPath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	contentBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(contentBytes)

	assert.Contains(t, content, "provider = AWS")
	assert.NotContains(t, content, "endpoint =")
	assert.NotContains(t, content, "force_path_style = true")
}

func TestGenerateDownloadScript_FailsOnExtractionErrors(t *testing.T) {
	script := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 1)
	assert.NotContains(t, script, "|| true")
	assert.Contains(t, script, "ERROR: Failed to extract zip archive")
	assert.Contains(t, script, "ERROR: Failed to extract tar archive")
}

func TestGenerateDownloadScript_DeletesArchivesOnlyAfterSuccess(t *testing.T) {
	script := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 1)
	zipExtract := "if ! unzip -q -o -j \"$zipfile\" -d \"$(dirname \"$zipfile\")\"; then"
	zipDelete := "rm -f \"$zipfile\""
	assert.Greater(t, strings.Index(script, zipDelete), strings.Index(script, zipExtract))

	tarExtract := "if ! tar --no-same-owner --no-same-permissions --no-absolute-filenames --transform='s|.*/||' -xf \"$tarfile\" -C \"$(dirname \"$tarfile\")\" 2>/dev/null; then"
	tarDelete := "rm -f \"$tarfile\""
	assert.Greater(t, strings.Index(script, tarDelete), strings.Index(script, tarExtract))
}

func TestGenerateDownloadScript_AvoidsFindPipeSubshellForArchiveDetection(t *testing.T) {
	script := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 1)
	assert.Contains(t, script, "while IFS= read -r zipfile;")
	assert.Contains(t, script, "while IFS= read -r tarfile;")
	assert.NotContains(t, script, "| while read zipfile")
	assert.NotContains(t, script, "| while read tarfile")
}

func TestGenerateDownloadScript_UsesFilterFromFile(t *testing.T) {
	script := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 1)

	// rclone is invoked with --filter-from, not a long --filter chain.
	assert.Contains(t, script, "--filter-from \"$FILTER_FILE\"")
	assert.NotContains(t, script, "--filter \"+ *.jpg\"")

	// Built-in output excludes are still present even with no user excludes.
	assert.Contains(t, script, "- output/**")
	assert.Contains(t, script, "- **/output/**")

	// Includes are emitted in the filter file.
	for _, ext := range []string{"+ *.jpg", "+ *.tif", "+ *.zip", "+ *.tar.gz"} {
		assert.Contains(t, script, ext)
	}

	// Final catch-all drops everything else.
	assert.Contains(t, script, "\n- *\n")
}

func TestGenerateDownloadScript_EmbedsUserExcludesBeforeIncludes(t *testing.T) {
	excludes := []string{"odm_orthophoto/**", "submodels/**", "all.zip"}
	script := GenerateDownloadScript("job-1", "s3://bucket/input/", excludes, 1)

	for _, p := range excludes {
		assert.Contains(t, script, "- "+p+"\n")
	}

	// Excludes must appear before `+ *.jpg` so they win over the include rules.
	excludeIdx := strings.Index(script, "- odm_orthophoto/**")
	includeIdx := strings.Index(script, "+ *.jpg")
	require.Greater(t, excludeIdx, 0)
	require.Greater(t, includeIdx, 0)
	assert.Less(t, excludeIdx, includeIdx, "excludes must precede includes in filter file")
}

func TestGenerateDownloadScript_MaxDepthFlag(t *testing.T) {
	withDepth := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 3)
	assert.Contains(t, withDepth, "--max-depth 3")

	defaultDepth := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 1)
	assert.Contains(t, defaultDepth, "--max-depth 1")

	unlimited := GenerateDownloadScript("job-1", "s3://bucket/input/", nil, 0)
	assert.NotContains(t, unlimited, "--max-depth")
}

func TestGenerateUploadScript_TestWriteFailureSurvivesErrexit(t *testing.T) {
	script := GenerateUploadScript("s3://bucket/output/")

	assert.Contains(t, script, `if TEST_OUTPUT=$(rclone copyto "$TEST_FILE" "$TEST_OBJECT" 2>&1); then`)
	assert.Contains(t, script, "else\n  TEST_EXIT_CODE=$?")
	assert.Contains(t, script, "This might be a false positive. Continuing with actual upload...")
	assert.NotContains(t, script, "TEST_OUTPUT=$(rclone copyto \"$TEST_FILE\" \"$TEST_OBJECT\" 2>&1)\nTEST_EXIT_CODE=$?")
}

func TestRenderRcloneFilterFile_OrderingAndFormat(t *testing.T) {
	out := renderRcloneFilterFile([]string{"odm_orthophoto/**", "all.zip"})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 4)
	assert.Equal(t, "- odm_orthophoto/**", lines[0])
	assert.Equal(t, "- all.zip", lines[1])
	assert.Equal(t, "- *", lines[len(lines)-1])

	// Every line is one of: "- pattern", "+ pattern", or "- *".
	for _, line := range lines {
		require.True(t, strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "+ "), "unexpected filter line: %q", line)
	}
}

func TestExcludeMatcher_DirectoryNamePatterns(t *testing.T) {
	m := compileExcludeMatcher([]string{"odm_orthophoto/**", "**/odm_dem/**", "submodels/**"})
	prefix := "project-123/"

	assert.True(t, m.matches("project-123/task-a/odm_orthophoto/odm_orthophoto.tif", prefix))
	assert.True(t, m.matches("project-123/task-b/odm_dem/dsm.tif", prefix))
	assert.True(t, m.matches("project-123/submodels/000/images/img.jpg", prefix))
	assert.False(t, m.matches("project-123/task-a/images/img.jpg", prefix))
}

func TestExcludeMatcher_BasenameAndExtensionPatterns(t *testing.T) {
	m := compileExcludeMatcher([]string{"all.zip", "**/all.zip", "*.bak"})
	prefix := "project/"

	assert.True(t, m.matches("project/all.zip", prefix))
	assert.True(t, m.matches("project/task-a/all.zip", prefix))
	assert.True(t, m.matches("project/task-a/scratch.bak", prefix))
	assert.False(t, m.matches("project/task-a/img.jpg", prefix))
}

func TestExcludeMatcher_IgnoresUnrecognisedPatterns(t *testing.T) {
	// `task-*/odm_*` isn't in our supported subset; matcher should treat it as
	// inert (rclone is authoritative). No false positives on plain inputs.
	m := compileExcludeMatcher([]string{"task-*/odm_*"})
	assert.False(t, m.matches("project/task-a/odm_orthophoto.tif", "project/"))
	assert.False(t, m.matches("project/task-a/images/img.jpg", "project/"))
}

func TestAccumulateImageStatsFromObjectsWithExcludes_SkipsExcludedPaths(t *testing.T) {
	objectCh := make(chan minio.ObjectInfo, 6)
	objectCh <- minio.ObjectInfo{Key: "project/task-a/images/img1.jpg", Size: 100}
	objectCh <- minio.ObjectInfo{Key: "project/task-b/images/img2.jpg", Size: 200}
	objectCh <- minio.ObjectInfo{Key: "project/task-a/odm_orthophoto/odm_orthophoto.tif", Size: 9999}
	objectCh <- minio.ObjectInfo{Key: "project/task-b/odm_dem/dsm.tif", Size: 9999}
	objectCh <- minio.ObjectInfo{Key: "project/submodels/000/images/sub.jpg", Size: 50}
	objectCh <- minio.ObjectInfo{Key: "project/all.zip", Size: 7777}
	close(objectCh)

	matcher := compileExcludeMatcher([]string{
		"odm_orthophoto/**", "odm_dem/**", "submodels/**", "all.zip", "**/all.zip",
	})
	count, totalBytes, err := accumulateImageStatsFromObjectsWithExcludes(objectCh, "project/", matcher)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, int64(300), totalBytes)
}

func TestCountImageStatsInS3PathWithExcludes_AlwaysExcludesOutputDirs(t *testing.T) {
	// output/** and **/output/** are unconditionally excluded even when the caller
	// passes no user excludes (alwaysExcludePatterns are prepended internally).
	objectCh := make(chan minio.ObjectInfo, 4)
	objectCh <- minio.ObjectInfo{Key: "images/img1.jpg", Size: 100}
	objectCh <- minio.ObjectInfo{Key: "images/output/odm_orthophoto.tif", Size: 9999}
	objectCh <- minio.ObjectInfo{Key: "images/nested/output/result.tif", Size: 8888}
	objectCh <- minio.ObjectInfo{Key: "images/img2.tif", Size: 200}
	close(objectCh)

	matcher := compileExcludeMatcher(alwaysExcludePatterns)
	count, totalBytes, err := accumulateImageStatsFromObjectsWithExcludes(objectCh, "images/", matcher)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, int64(300), totalBytes)
}

func TestCountImageStatsInS3PathWithExcludes_ThumbsDirExcludedViaDefaultExcludes(t *testing.T) {
	// thumbs/** and **/thumbs/** are in DefaultProjectExcludes so thumbnail images
	// are not counted when useDefaultExcludes is true.
	objectCh := make(chan minio.ObjectInfo, 4)
	objectCh <- minio.ObjectInfo{Key: "images/img1.jpg", Size: 100}
	objectCh <- minio.ObjectInfo{Key: "images/thumbs/img1_thumb.jpg", Size: 20}
	objectCh <- minio.ObjectInfo{Key: "images/nested/thumbs/img2_thumb.jpg", Size: 30}
	objectCh <- minio.ObjectInfo{Key: "images/img2.tif", Size: 200}
	close(objectCh)

	matcher := compileExcludeMatcher([]string{"thumbs/**", "**/thumbs/**"})
	count, totalBytes, err := accumulateImageStatsFromObjectsWithExcludes(objectCh, "images/", matcher)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, int64(300), totalBytes)
}

func testS3Client(t *testing.T) *minio.Client {
	t.Helper()

	client, err := minio.New(testutil.TestS3Endpoint(), &minio.Options{
		Creds:  credentials.NewStaticV4(testutil.TestS3AccessKey(), testutil.TestS3SecretKey(), ""),
		Secure: false,
	})
	require.NoError(t, err)

	return client
}

func putTestObject(t *testing.T, client *minio.Client, bucket, key, content string) {
	t.Helper()
	payload := bytes.NewReader([]byte(content))
	_, err := client.PutObject(context.Background(), bucket, key, payload, int64(payload.Len()), minio.PutObjectOptions{ContentType: "application/octet-stream"})
	require.NoError(t, err)
}

func TestDeleteWorkflowLogsFromS3(t *testing.T) {
	ctx := context.Background()
	bucket := "test-bucket-delete-logs"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	client := testS3Client(t)
	prefix := "results/task-delete-logs/"
	putTestObject(t, client, bucket, prefix+".workflow-logs.txt", "stale logs")
	putTestObject(t, client, bucket, prefix+"orthophoto.tif", "keep me")

	writePath := "s3://" + bucket + "/" + prefix
	require.NoError(t, DeleteWorkflowLogsFromS3(ctx, client, writePath))

	exists, err := ObjectExistsInS3Path(ctx, client, writePath, ".workflow-logs.txt")
	require.NoError(t, err)
	assert.False(t, exists, "log object should be removed")

	exists, err = ObjectExistsInS3Path(ctx, client, writePath, "orthophoto.tif")
	require.NoError(t, err)
	assert.True(t, exists, "sibling objects should be untouched")

	// Deleting again should be a no-op.
	require.NoError(t, DeleteWorkflowLogsFromS3(ctx, client, writePath))

	// Missing prefix should also be a no-op.
	require.NoError(t, DeleteWorkflowLogsFromS3(ctx, client, "s3://"+bucket+"/never/created/"))
}

func TestObjectExistsInS3Path(t *testing.T) {
	ctx := context.Background()
	bucket := "test-bucket-object-exists"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	client := testS3Client(t)
	putTestObject(t, client, bucket, "results/task-1/orthophoto.tif", "orthophoto")

	exists, err := ObjectExistsInS3Path(ctx, client, "s3://"+bucket+"/results/task-1/", "orthophoto.tif")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = ObjectExistsInS3Path(ctx, client, "s3://"+bucket+"/results/task-1/", "missing.tif")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestListFilesInS3PathWithLimit_FiltersAndLimits(t *testing.T) {
	ctx := context.Background()
	bucket := "test-bucket-list-files-limit"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	client := testS3Client(t)
	prefix := "results/task-2/"
	putTestObject(t, client, bucket, prefix+"all.zip", "zip")
	putTestObject(t, client, bucket, prefix+"orthophoto.tif", "tif")
	putTestObject(t, client, bucket, prefix+".workflow-logs.txt", "hidden")
	putTestObject(t, client, bucket, prefix+"nested/file.txt", "nested")
	putTestObject(t, client, bucket, prefix+"point_cloud.laz", "pc")

	files, err := ListFilesInS3PathWithLimit(ctx, client, "s3://"+bucket+"/results/task-2/", 2)
	require.NoError(t, err)
	require.Len(t, files, 2)

	sort.Strings(files)
	for _, name := range files {
		assert.NotContains(t, name, "/")
		assert.False(t, strings.HasPrefix(name, "."))
	}
}

func TestCountImageStatsInS3Path_AccumulatesCountAndBytes(t *testing.T) {
	objectCh := make(chan minio.ObjectInfo, 4)
	objectCh <- minio.ObjectInfo{Key: "images/a.jpg", Size: 10}
	objectCh <- minio.ObjectInfo{Key: "images/b.tif", Size: 20}
	objectCh <- minio.ObjectInfo{Key: "images/c.txt", Size: 999}
	close(objectCh)

	count, totalBytes, err := accumulateImageStatsFromObjects(objectCh)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, int64(30), totalBytes)
}

func TestCountImageStatsInS3Path_FiltersSupportedExtensionsCaseInsensitive(t *testing.T) {
	objectCh := make(chan minio.ObjectInfo, 5)
	objectCh <- minio.ObjectInfo{Key: "images/a.JPG", Size: 11}
	objectCh <- minio.ObjectInfo{Key: "images/b.JPEG", Size: 12}
	objectCh <- minio.ObjectInfo{Key: "images/c.TIFF", Size: 13}
	objectCh <- minio.ObjectInfo{Key: "images/d.TIF", Size: 14}
	objectCh <- minio.ObjectInfo{Key: "images/e.png", Size: 15}
	close(objectCh)

	count, totalBytes, err := accumulateImageStatsFromObjects(objectCh)
	require.NoError(t, err)
	assert.Equal(t, 4, count)
	assert.Equal(t, int64(50), totalBytes)
}

func TestCountImageStatsInS3Path_PropagatesListingErrors(t *testing.T) {
	objectCh := make(chan minio.ObjectInfo, 1)
	objectCh <- minio.ObjectInfo{Err: errors.New("list failed")}
	close(objectCh)

	count, totalBytes, err := accumulateImageStatsFromObjects(objectCh)
	require.Error(t, err)
	assert.Equal(t, 0, count)
	assert.Equal(t, int64(0), totalBytes)
	assert.Contains(t, err.Error(), "failed to list objects")
}

func TestListObjectsRecursiveInS3Path_IncludesNestedKeys(t *testing.T) {
	ctx := context.Background()
	bucket := "test-bucket-list-recursive"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	client := testS3Client(t)
	prefix := "results/task-recursive/"
	putTestObject(t, client, bucket, prefix+"orthophoto.tif", "ortho")
	putTestObject(t, client, bucket, prefix+"odm_orthophoto/odm_orthophoto.tif", "nested-ortho")
	putTestObject(t, client, bucket, prefix+"odm_dem/dsm.tif", "dsm")

	objects, err := ListObjectsRecursiveInS3Path(ctx, client, "s3://"+bucket+"/results/task-recursive/")
	require.NoError(t, err)

	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	sort.Strings(keys)
	assert.Contains(t, keys, prefix+"orthophoto.tif")
	assert.Contains(t, keys, prefix+"odm_orthophoto/odm_orthophoto.tif")
	assert.Contains(t, keys, prefix+"odm_dem/dsm.tif")
}

func TestStreamS3PathAsZip_StreamsNestedEntries(t *testing.T) {
	ctx := context.Background()
	bucket := "test-bucket-stream-zip"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	client := testS3Client(t)
	prefix := "results/task-zip/"
	putTestObject(t, client, bucket, prefix+"odm_orthophoto/odm_orthophoto.tif", "ortho-bytes")
	putTestObject(t, client, bucket, prefix+"odm_dem/dsm.tif", "dsm-bytes")

	var buf bytes.Buffer
	count, err := StreamS3PathAsZip(ctx, client, "s3://"+bucket+"/results/task-zip/", &buf)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	entries := map[string]string{}
	for _, f := range zr.File {
		rc, openErr := f.Open()
		require.NoError(t, openErr)
		payload, readErr := io.ReadAll(rc)
		require.NoError(t, readErr)
		require.NoError(t, rc.Close())
		entries[f.Name] = string(payload)
	}

	assert.Equal(t, "ortho-bytes", entries["odm_orthophoto/odm_orthophoto.tif"])
	assert.Equal(t, "dsm-bytes", entries["odm_dem/dsm.tif"])
}

func TestSanitizeZipEntryName_RejectsUnsafePaths(t *testing.T) {
	prefix := "results/task/"

	name, ok := sanitizeZipEntryName(prefix, "results/task/odm_orthophoto/odm_orthophoto.tif")
	assert.True(t, ok)
	assert.Equal(t, "odm_orthophoto/odm_orthophoto.tif", name)

	name, ok = sanitizeZipEntryName(prefix, "results/task/")
	assert.False(t, ok)
	assert.Empty(t, name)

	name, ok = sanitizeZipEntryName(prefix, "results/task/../escape.txt")
	assert.False(t, ok)
	assert.Empty(t, name)
}
