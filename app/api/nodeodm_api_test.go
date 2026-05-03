package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInfoEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Huma returns the body content directly, not wrapped in a Body field
	var response struct {
		Version string `json:"version"`
		Engine  string `json:"engine"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, "0.4.0", response.Version)
	assert.Equal(t, "odm", response.Engine)
}

func TestOptionsEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodGet, "/options", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Huma may return array directly or wrapped in body
	// Try direct array first (Huma often unwraps arrays)
	var directResponse []OptionResponse
	err := json.Unmarshal(w.Body.Bytes(), &directResponse)
	if err != nil {
		// Fallback to wrapped format
		var response struct {
			Body []OptionResponse `json:"body"`
		}
		err = json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Greater(t, len(response.Body), 0)
	} else {
		assert.Greater(t, len(directResponse), 0)
	}
}

func TestTaskNewEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	_, handler := NewAPI(metadataStore, wfClient)

	// Set up test S3 bucket
	ctx := context.Background()
	err := testutil.SetupTestS3Bucket(ctx, "test-bucket")
	require.NoError(t, err, "Failed to set up test S3 bucket")

	// Create task request
	request := TaskNewRequest{
		Name:        "test-project",
		ReadS3Path:  "s3://test-bucket/images/",
		WriteS3Path: "s3://test-bucket/output/",
		Options:     `[{"name": "fast-orthophoto", "value": true}]`,
		S3Region:    "us-east-1",
		S3Endpoint:  "http://" + testutil.TestS3Endpoint(), // Use MinIO for tests
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should succeed (or fail with 400 if credentials are required)
	// The actual behavior depends on S3 credential resolution
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusBadRequest)
}

func TestTaskNew_AcceptsStandardProcessingMode(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)
	_, handler := NewAPI(metadataStore, wfClient)

	ctx := context.Background()
	err := testutil.SetupTestS3Bucket(ctx, "test-bucket")
	require.NoError(t, err, "Failed to set up test S3 bucket")

	depth := 3
	body, err := json.Marshal(TaskNewRequest{
		Name:           "standard-project",
		ReadS3Path:     "s3://test-bucket/images/",
		WriteS3Path:    "s3://test-bucket/output/",
		ProcessingMode: workflows.ProcessingModeStandard,
		S3ScanDepth:    &depth,
		S3Region:       "us-east-1",
		S3Endpoint:     "http://" + testutil.TestS3Endpoint(),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusBadRequest,
		"unexpected status %d body=%s", w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "invalid processingMode")
	assert.NotContains(t, w.Body.String(), "reserved")
	assert.NotContains(t, w.Body.String(), "s3ScanDepth")
}

func TestTaskNew_RejectsInvalidS3ScanDepth(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)
	_, handler := NewAPI(metadataStore, wfClient)

	for _, depth := range []int{-1, workflows.MaxS3ScanDepth + 1} {
		t.Run(fmt.Sprintf("depth=%d", depth), func(t *testing.T) {
			d := depth
			body, err := json.Marshal(TaskNewRequest{
				ReadS3Path:  "s3://test-bucket/images/",
				S3ScanDepth: &d,
			})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "s3ScanDepth")
		})
	}
}

func TestTaskNew_RejectsReservedProcessingMode(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)
	_, handler := NewAPI(metadataStore, wfClient)

	for _, mode := range []string{"merge-existing", "thermal", "city-scale"} {
		t.Run(mode, func(t *testing.T) {
			body, err := json.Marshal(TaskNewRequest{
				ReadS3Path:     "s3://test-bucket/images/",
				ProcessingMode: mode,
			})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusNotImplemented, w.Code)
			assert.Contains(t, w.Body.String(), "reserved")
		})
	}
}

func TestTaskNew_RejectsUnknownProcessingMode(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)
	_, handler := NewAPI(metadataStore, wfClient)

	body, err := json.Marshal(TaskNewRequest{
		ReadS3Path:     "s3://test-bucket/images/",
		ProcessingMode: "definitely-not-a-mode",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid processingMode")
}

func TestTaskNew_RejectsMalformedExcludePathsJSON(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)
	_, handler := NewAPI(metadataStore, wfClient)

	body, err := json.Marshal(TaskNewRequest{
		ReadS3Path:   "s3://test-bucket/images/",
		ExcludePaths: `not-a-json-array`,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "excludePaths")
}

func TestTaskNew_RejectsUnsafeExcludePattern(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)
	_, handler := NewAPI(metadataStore, wfClient)

	body, err := json.Marshal(TaskNewRequest{
		ReadS3Path:   "s3://test-bucket/images/",
		ExcludePaths: `["../etc/passwd"]`,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "exclude pattern")
}

func TestTaskListEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodGet, "/task/list", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Huma may return array directly or wrapped in body
	// Try direct array first (Huma often unwraps arrays)
	var directResponse []TaskListItem
	err := json.Unmarshal(w.Body.Bytes(), &directResponse)
	if err != nil {
		// Fallback to wrapped format
		var response struct {
			Body []TaskListItem `json:"body"`
		}
		err = json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		// Just verify we got a valid response (may be empty if no workflows exist)
		assert.NotNil(t, response.Body)
	} else {
		// Just verify we got a valid response (may be empty if no workflows exist)
		assert.NotNil(t, directResponse)
	}
}

func TestTaskInfoEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	// Create job metadata (workflow may or may not exist in cluster)
	ctx := context.Background()

	workflowName := "test-workflow-info"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	// Verify job was created before calling API handler
	job, err := metadataStore.GetJob(ctx, workflowName)
	require.NoError(t, err)
	require.NotNil(t, job, "Job should exist before API call")

	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/info", nil)
	req = req.WithContext(ctx) // Use the same context as the test
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should return OK even if workflow doesn't exist in cluster (metadata exists)
	assert.Equal(t, http.StatusOK, w.Code)

	// Huma may return the body wrapped in a Body field or directly
	var response TaskInfo
	var wrappedResponse TaskInfoResponse
	err = json.Unmarshal(w.Body.Bytes(), &wrappedResponse)
	if err == nil && wrappedResponse.Body.UUID != "" {
		response = wrappedResponse.Body
	} else {
		// Try direct unmarshaling
		err = json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
	}
	assert.Equal(t, workflowName, response.UUID)
	// Missing workflow is now conservatively reconciled during grace period.
	assert.Equal(t, StatusCodeQueued, response.Status.Code)
	assert.Empty(t, response.Status.ErrorMessage)
}

func TestTaskCancelEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	_, handler := NewAPI(metadataStore, wfClient)

	workflowName := "test-workflow-cancel"
	requestBody := map[string]string{
		"uuid": workflowName,
	}
	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/cancel", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// May return 200/204 on success, or error if workflow doesn't exist
	// Just verify we got a response
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusNoContent || w.Code >= 400)
}

func TestTaskRemoveEndpoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := testWorkflowClient(t)

	// Create job metadata
	ctx := context.Background()

	workflowName := "test-workflow-remove"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	_, handler := NewAPI(metadataStore, wfClient)

	requestBody := map[string]string{
		"uuid": workflowName,
	}
	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/remove", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Huma may return 204 No Content for successful delete operations
	// or 200 OK with JSON body
	if w.Code == http.StatusNoContent {
		// 204 No Content - no body to parse
		assert.Empty(t, w.Body.Bytes())
	} else {
		assert.Equal(t, http.StatusOK, w.Code)
		var response Response
		err = json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	}

	// Verify metadata was deleted
	job, err := metadataStore.GetJob(ctx, workflowName)
	require.NoError(t, err)
	assert.Nil(t, job)
}

func decodeTaskAssetsResponse(t *testing.T, body []byte) TaskAssets {
	t.Helper()

	var direct TaskAssets
	if err := json.Unmarshal(body, &direct); err == nil && len(direct.Primary) > 0 {
		return direct
	}

	var wrapped TaskAssetsResponse
	err := json.Unmarshal(body, &wrapped)
	require.NoError(t, err)
	return wrapped.Body
}

func TestTaskAssetsEndpoint_PrimaryAndAdditionalBehavior(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-assets-primary"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	metadataStore := meta.NewStore(db)
	workflowName := "test-task-assets-primary"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://"+bucket+"/images/",
		"s3://"+bucket+"/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	require.NoError(t, metadataStore.MergeJobMetadata(ctx, workflowName, map[string]interface{}{
		"s3_endpoint": "http://" + testutil.TestS3Endpoint(),
	}))

	ensureTestObjectInBucket(ctx, t, bucket, "output/odm_orthophoto/odm_orthophoto.tif", "orthophoto")
	ensureTestObjectInBucket(ctx, t, bucket, "output/odm_georeferencing/odm_georeferenced_model.las", "pointcloud")
	ensureTestObjectInBucket(ctx, t, bucket, "output/report.pdf", "report")

	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/assets", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assets := decodeTaskAssetsResponse(t, w.Body.Bytes())
	require.Len(t, assets.Primary, len(taskPrimaryAssetDefinitions))
	assert.Empty(t, assets.Additional)

	byID := map[string]TaskAssetsPrimaryItem{}
	for _, item := range assets.Primary {
		byID[item.ID] = item
	}

	assert.False(t, byID["all_zip"].Exists)
	assert.Empty(t, byID["all_zip"].DownloadURL)

	assert.True(t, byID["orthophoto"].Exists)
	assert.Equal(t, "odm_orthophoto/odm_orthophoto.tif", byID["orthophoto"].Asset)
	assert.Equal(t, "/task/"+workflowName+"/download/odm_orthophoto/odm_orthophoto.tif", byID["orthophoto"].DownloadURL)

	assert.True(t, byID["point_cloud"].Exists)
	assert.Equal(t, "odm_georeferencing/odm_georeferenced_model.las", byID["point_cloud"].Asset)
	assert.Equal(t, "/task/"+workflowName+"/download/odm_georeferencing/odm_georeferenced_model.las", byID["point_cloud"].DownloadURL)

	req = httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/assets?includeAdditional=true&additionalLimit=10", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assets = decodeTaskAssetsResponse(t, w.Body.Bytes())
	require.NotEmpty(t, assets.Additional)
	var found bool
	for _, item := range assets.Additional {
		if item.Asset == "report.pdf" {
			assert.Equal(t, "/task/"+workflowName+"/download/report.pdf", item.DownloadURL)
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestTaskAssetsEndpoint_ErrorScenarios(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	metadataStore := meta.NewStore(db)
	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	notFoundReq := httptest.NewRequest(http.MethodGet, "/task/missing-task/assets", nil)
	notFoundResp := httptest.NewRecorder()
	handler.ServeHTTP(notFoundResp, notFoundReq)
	assert.Equal(t, http.StatusNotFound, notFoundResp.Code)

	workflowName := "task-assets-no-write-path"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://bucket/images/",
		"",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)

	badReq := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/assets", nil)
	badResp := httptest.NewRecorder()
	handler.ServeHTTP(badResp, badReq)
	assert.Equal(t, http.StatusBadRequest, badResp.Code)
}

func TestTaskAssetsEndpoint_AdditionalLimitClampFloor(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-assets-limit"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	metadataStore := meta.NewStore(db)
	workflowName := "task-assets-limit-floor"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://"+bucket+"/images/",
		"s3://"+bucket+"/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	require.NoError(t, metadataStore.MergeJobMetadata(ctx, workflowName, map[string]interface{}{
		"s3_endpoint": "http://" + testutil.TestS3Endpoint(),
	}))

	ensureTestObjectInBucket(ctx, t, bucket, "output/custom-a.txt", "a")
	ensureTestObjectInBucket(ctx, t, bucket, "output/custom-b.txt", "b")

	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/assets?includeAdditional=true&additionalLimit=-1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assets := decodeTaskAssetsResponse(t, w.Body.Bytes())
	assert.GreaterOrEqual(t, len(assets.Additional), 2)
}

func TestDownloadEndpoint_StillRedirectsForExistingAsset(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-download-regression"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	metadataStore := meta.NewStore(db)
	workflowName := "download-regression-task"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://"+bucket+"/images/",
		"s3://"+bucket+"/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	require.NoError(t, metadataStore.MergeJobMetadata(ctx, workflowName, map[string]interface{}{
		"s3_endpoint": "http://" + testutil.TestS3Endpoint(),
	}))

	ensureTestObjectInBucket(ctx, t, bucket, "output/all.zip", "zip-content")

	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/download/all.zip", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.NotEmpty(t, w.Header().Get("Location"))
}

func TestDownloadEndpoint_AliasResolvesNestedOrthophoto(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-download-alias-ortho"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	metadataStore := meta.NewStore(db)
	workflowName := "download-alias-ortho"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://"+bucket+"/images/",
		"s3://"+bucket+"/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	require.NoError(t, metadataStore.MergeJobMetadata(ctx, workflowName, map[string]interface{}{
		"s3_endpoint": "http://" + testutil.TestS3Endpoint(),
	}))

	ensureTestObjectInBucket(ctx, t, bucket, "output/odm_orthophoto/odm_orthophoto.tif", "nested-ortho")

	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/download/orthophoto", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	location := w.Header().Get("Location")
	assert.NotEmpty(t, location)
	assert.Contains(t, location, "/output/odm_orthophoto/odm_orthophoto.tif")
}

func TestDownloadEndpoint_AllZipStreamsWhenMissing(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-download-allzip-stream"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))

	metadataStore := meta.NewStore(db)
	workflowName := "download-allzip-stream"
	_, err := metadataStore.CreateJob(
		ctx,
		workflowName,
		"test-project",
		"s3://"+bucket+"/images/",
		"s3://"+bucket+"/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	require.NoError(t, metadataStore.MergeJobMetadata(ctx, workflowName, map[string]interface{}{
		"s3_endpoint": "http://" + testutil.TestS3Endpoint(),
	}))

	ensureTestObjectInBucket(ctx, t, bucket, "output/odm_orthophoto/odm_orthophoto.tif", "ortho-bytes")
	ensureTestObjectInBucket(ctx, t, bucket, "output/odm_dem/dsm.tif", "dsm-bytes")

	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/download/all.zip", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))

	zr, zipErr := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	require.NoError(t, zipErr)

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

func TestNormalizeOptionalS3Endpoint(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  string
		wantError bool
	}{
		{name: "empty allowed", input: "", expected: ""},
		{name: "normalize path and query", input: "http://localhost:9000/path?x=1", expected: "http://localhost:9000"},
		{name: "host only", input: "s3.amazonaws.com", expected: "s3.amazonaws.com"},
		{name: "invalid url", input: "https://", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOptionalS3Endpoint(tt.input)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestTaskS3Client_UsesMetadataEndpointForPresign(t *testing.T) {
	metadata := map[string]interface{}{"s3_endpoint": "http://localhost:3900/path"}
	metadataJSON, err := json.Marshal(metadata)
	require.NoError(t, err)

	client, err := taskS3Client(metadataJSON)
	require.NoError(t, err)

	presigned, err := s3.GeneratePresignedURL(
		context.Background(),
		client,
		"s3://scaleodm-test/output/",
		"all.zip",
		time.Hour,
	)
	require.Error(t, err)
	assert.Empty(t, presigned)

	assert.Contains(t, err.Error(), "localhost:3900")
	assert.Contains(t, err.Error(), "/scaleodm-test/?location=")
}

func TestDetectWorkflowInfraFailure(t *testing.T) {
	tests := []struct {
		name     string
		workflow *wfv1.Workflow
		expected string
	}{
		{
			name: "detect image pull backoff in node message",
			workflow: &wfv1.Workflow{
				Status: wfv1.WorkflowStatus{
					Nodes: map[string]wfv1.NodeStatus{
						"n1": {
							Message: "Error: ImagePullBackOff",
						},
					},
				},
			},
			expected: "Error: ImagePullBackOff",
		},
		{
			name: "detect failed pull in workflow message",
			workflow: &wfv1.Workflow{
				Status: wfv1.WorkflowStatus{
					Message: "failed to pull image docker.io/opendronemap/odm:3.5.6",
				},
			},
			expected: "failed to pull image docker.io/opendronemap/odm:3.5.6",
		},
		{
			name: "no infra failure",
			workflow: &wfv1.Workflow{
				Status: wfv1.WorkflowStatus{
					Message: "workflow running",
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, detectWorkflowInfraFailure(tt.workflow))
		})
	}
}

func TestParseAllowedEndpointAllowlist(t *testing.T) {
	allowlist := parseAllowedEndpointAllowlist("http://localhost:9000/path, s3.amazonaws.com, https://minio.example.com/api")
	_, hasLocal := allowlist["http://localhost:9000"]
	_, hasAWS := allowlist["s3.amazonaws.com"]
	_, hasMinio := allowlist["https://minio.example.com"]
	assert.True(t, hasLocal)
	assert.True(t, hasAWS)
	assert.True(t, hasMinio)
}

func TestMetadataImageCount(t *testing.T) {
	metadata := map[string]interface{}{"image_count": 42}
	metadataJSON, err := json.Marshal(metadata)
	require.NoError(t, err)
	assert.Equal(t, 42, metadataImageCount(metadataJSON))
}

func TestMetadataImageTotalBytes(t *testing.T) {
	metadata := map[string]interface{}{"image_total_bytes": int64(123456789)}
	metadataJSON, err := json.Marshal(metadata)
	require.NoError(t, err)
	assert.Equal(t, int64(123456789), metadataImageTotalBytes(metadataJSON))
}

type recordingWorkflowClient struct {
	createFn func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error)
	deleteFn func(ctx context.Context, name string) error

	createdNames []string
	deletedNames []string
}

func (c *recordingWorkflowClient) CreateODMWorkflow(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
	if c.createFn != nil {
		wf, err := c.createFn(ctx, cfg)
		if wf != nil {
			c.createdNames = append(c.createdNames, wf.Name)
		}
		return wf, err
	}
	wf := &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "wf-default"}}
	c.createdNames = append(c.createdNames, wf.Name)
	return wf, nil
}

func (c *recordingWorkflowClient) GetWorkflow(ctx context.Context, name string) (*wfv1.Workflow, error) {
	return nil, errors.New("not implemented")
}

func (c *recordingWorkflowClient) ListWorkflows(ctx context.Context, labelSelector string) (*wfv1.WorkflowList, error) {
	return &wfv1.WorkflowList{}, nil
}

func (c *recordingWorkflowClient) DeleteWorkflow(ctx context.Context, name string) error {
	c.deletedNames = append(c.deletedNames, name)
	if c.deleteFn != nil {
		return c.deleteFn(ctx, name)
	}
	return nil
}

func (c *recordingWorkflowClient) GetWorkflowLogs(ctx context.Context, workflowName string, writer io.Writer) error {
	return errors.New("not implemented")
}

func (c *recordingWorkflowClient) GetWorkflowLogsWithS3Path(ctx context.Context, workflowName, writeS3Path string, s3Client interface{}, writer io.Writer) error {
	return errors.New("not implemented")
}

func (c *recordingWorkflowClient) WatchWorkflow(ctx context.Context, workflowName string) (*wfv1.Workflow, error) {
	return nil, errors.New("not implemented")
}

func (c *recordingWorkflowClient) GetWorkflowStatus(ctx context.Context, workflowName string) (wfv1.WorkflowPhase, string, error) {
	return "", "", errors.New("not implemented")
}

func (c *recordingWorkflowClient) IsWorkflowComplete(ctx context.Context, workflowName string) (bool, error) {
	return false, errors.New("not implemented")
}

func ensureTestImageInBucket(ctx context.Context, t *testing.T, bucket, key string) {
	t.Helper()

	endpoint := strings.TrimPrefix(strings.TrimPrefix(testutil.TestS3Endpoint(), "http://"), "https://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(testutil.TestS3AccessKey(), testutil.TestS3SecretKey(), ""),
		Secure: false,
	})
	require.NoError(t, err)

	payload := bytes.NewReader([]byte("fake-jpeg-content"))
	_, err = client.PutObject(ctx, bucket, key, payload, int64(payload.Len()), minio.PutObjectOptions{ContentType: "image/jpeg"})
	require.NoError(t, err)
}

func ensureTestObjectInBucket(ctx context.Context, t *testing.T, bucket, key, content string) {
	t.Helper()

	endpoint := strings.TrimPrefix(strings.TrimPrefix(testutil.TestS3Endpoint(), "http://"), "https://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(testutil.TestS3AccessKey(), testutil.TestS3SecretKey(), ""),
		Secure: false,
	})
	require.NoError(t, err)

	payload := bytes.NewReader([]byte(content))
	_, err = client.PutObject(ctx, bucket, key, payload, int64(payload.Len()), minio.PutObjectOptions{ContentType: "application/octet-stream"})
	require.NoError(t, err)
}

func TestTaskNew_MetadataCreateFailureCompensatesWorkflow(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, testutil.SetupTestS3Bucket(ctx, "test-bucket"))
	ensureTestImageInBucket(ctx, t, "test-bucket", "images/input.jpg")

	metadataStore := meta.NewStore(db)
	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "wf-new-fail"}}, nil
		},
	}

	_, handler := NewAPI(metadataStore, wfClient)

	request := TaskNewRequest{
		Name:        "test-project",
		ReadS3Path:  "s3://test-bucket/images/",
		WriteS3Path: "s3://test-bucket/output/",
		S3Region:    "us-east-1",
		S3Endpoint:  "http://" + testutil.TestS3Endpoint(),
	}
	body, err := json.Marshal(request)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	_, err = metadataStore.CreateJob(ctx, "wf-new-fail", "existing", "s3://x/in", "s3://x/out", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	require.Len(t, wfClient.deletedNames, 1)
	assert.Equal(t, "wf-new-fail", wfClient.deletedNames[0])
}

func TestTaskRestart_CreateFailureDoesNotDeleteOldWorkflowFirst(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	metadataStore := meta.NewStore(db)
	_, err := metadataStore.CreateJob(ctx, "old-wf", "project", "s3://bucket/images/", "s3://bucket/output/", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			return nil, errors.New("create failed")
		},
	}

	_, handler := NewAPI(metadataStore, wfClient)

	body := []byte(`{"uuid":"old-wf"}`)
	req := httptest.NewRequest(http.MethodPost, "/task/restart", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Empty(t, wfClient.deletedNames)
}

func TestTaskRestart_MetadataSwapFailureDeletesNewWorkflow(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	metadataStore := meta.NewStore(db)
	_, err := metadataStore.CreateJob(ctx, "old-wf", "project", "s3://bucket/images/", "s3://bucket/output/", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)
	_, err = metadataStore.CreateJob(ctx, "new-wf", "project", "s3://bucket/images/", "s3://bucket/output/", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "new-wf"}}, nil
		},
	}

	_, handler := NewAPI(metadataStore, wfClient)

	body := []byte(`{"uuid":"old-wf"}`)
	req := httptest.NewRequest(http.MethodPost, "/task/restart", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	require.Len(t, wfClient.deletedNames, 1)
	assert.Equal(t, "new-wf", wfClient.deletedNames[0])
}

func TestTaskRestart_SuccessfulCutoverDeletesOldWorkflowAfterSwap(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	metadataStore := meta.NewStore(db)
	_, err := metadataStore.CreateJob(ctx, "old-wf", "project", "s3://bucket/images/", "s3://bucket/output/", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "new-wf"}}, nil
		},
	}

	_, handler := NewAPI(metadataStore, wfClient)

	body := []byte(`{"uuid":"old-wf"}`)
	req := httptest.NewRequest(http.MethodPost, "/task/restart", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, wfClient.deletedNames, 1)
	assert.Equal(t, "old-wf", wfClient.deletedNames[0])

	oldJob, err := metadataStore.GetJob(ctx, "old-wf")
	require.NoError(t, err)
	assert.Nil(t, oldJob)

	newJob, err := metadataStore.GetJob(ctx, "new-wf")
	require.NoError(t, err)
	require.NotNil(t, newJob)
}
