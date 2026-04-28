package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	script := GenerateDownloadScript("job-1", "s3://bucket/input/")
	assert.NotContains(t, script, "|| true")
	assert.Contains(t, script, "ERROR: Failed to extract zip archive")
	assert.Contains(t, script, "ERROR: Failed to extract tar archive")
}

func TestGenerateDownloadScript_DeletesArchivesOnlyAfterSuccess(t *testing.T) {
	script := GenerateDownloadScript("job-1", "s3://bucket/input/")
	zipExtract := "if ! unzip -q -o -j \"$zipfile\" -d \"$(dirname \"$zipfile\")\"; then"
	zipDelete := "rm -f \"$zipfile\""
	assert.Greater(t, strings.Index(script, zipDelete), strings.Index(script, zipExtract))

	tarExtract := "if ! tar --no-same-owner --no-same-permissions --no-absolute-filenames --transform='s|.*/||' -xf \"$tarfile\" -C \"$(dirname \"$tarfile\")\" 2>/dev/null; then"
	tarDelete := "rm -f \"$tarfile\""
	assert.Greater(t, strings.Index(script, tarDelete), strings.Index(script, tarExtract))
}

func TestGenerateDownloadScript_AvoidsFindPipeSubshellForArchiveDetection(t *testing.T) {
	script := GenerateDownloadScript("job-1", "s3://bucket/input/")
	assert.Contains(t, script, "while IFS= read -r zipfile;")
	assert.Contains(t, script, "while IFS= read -r tarfile;")
	assert.NotContains(t, script, "| while read zipfile")
	assert.NotContains(t, script, "| while read tarfile")
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
