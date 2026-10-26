package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
)

const validCamerasJSON = `{"dji fc8482 4032 3024 brown 0.6666":{"projection_type":"brown","width":4032,"height":3024,` +
	`"focal_x":0.7415979,"focal_y":0.7450442,"c_x":-0.0061633,"c_y":0.0022128,` +
	`"k1":0.0933579,"k2":-0.1437135,"p1":-0.0006259,"p2":-0.0011740,"k3":0.0822343}}`

const shortIDCamerasJSON = `{"cam":{"projection_type":"brown","width":4032,"height":3024,"focal_x":0.74,"focal_y":0.74}}`

func camerasTaskNewRequest(t *testing.T, bucket, camerasValue string) []byte {
	t.Helper()

	options, err := json.Marshal([]TaskOption{{Name: "cameras", Value: camerasValue}})
	require.NoError(t, err)

	body, err := json.Marshal(TaskNewRequest{
		Name:        "cameras-project",
		ReadS3Path:  "s3://" + bucket + "/images/",
		WriteS3Path: "s3://" + bucket + "/output/",
		S3Region:    "us-east-1",
		S3Endpoint:  "http://" + testutil.TestS3Endpoint(),
		Options:     string(options),
	})
	require.NoError(t, err)
	return body
}

func postTaskNew(t *testing.T, handler http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/task/new", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestTaskNew_AcceptsInlineCameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-inline"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")

	var submitted *workflows.ODMPipelineConfig
	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			submitted = cfg
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "wf-cameras-inline"}}, nil
		},
	}
	_, handler := NewAPI(meta.NewStore(db), wfClient)

	w := postTaskNew(t, handler, camerasTaskNewRequest(t, bucket, validCamerasJSON))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, submitted)
	assert.Contains(t, submitted.Cameras.Inline, "dji fc8482 4032 3024 brown 0.6666")
	assert.Empty(t, submitted.Cameras.S3Path)
	assert.NotContains(t, submitted.ODMFlags, "--use-fixed-camera-params")
}

func TestTaskNew_AcceptsPresentS3Cameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-s3-ok"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")
	ensureTestObjectInBucket(ctx, t, bucket, "calibration/cameras.json", validCamerasJSON)

	camerasPath := "s3://" + bucket + "/calibration/cameras.json"
	var submitted *workflows.ODMPipelineConfig
	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			submitted = cfg
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "wf-cameras-s3"}}, nil
		},
	}
	_, handler := NewAPI(meta.NewStore(db), wfClient)

	w := postTaskNew(t, handler, camerasTaskNewRequest(t, bucket, camerasPath))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, submitted)
	assert.Equal(t, camerasPath, submitted.Cameras.S3Path)
}

func TestTaskNew_RejectsUnreadableCameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-missing"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")

	wfClient := &recordingWorkflowClient{}
	_, handler := NewAPI(meta.NewStore(db), wfClient)

	w := postTaskNew(t, handler, camerasTaskNewRequest(t, bucket, "s3://"+bucket+"/calibration/cameras.json"))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Unable to read cameras")
	assert.Empty(t, wfClient.createdNames, "no workflow should be submitted")
}

func TestTaskNew_RejectsInvalidS3CamerasContent(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-invalid"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")
	ensureTestObjectInBucket(ctx, t, bucket, "calibration/cameras.json", shortIDCamerasJSON)

	wfClient := &recordingWorkflowClient{}
	_, handler := NewAPI(meta.NewStore(db), wfClient)

	w := postTaskNew(t, handler, camerasTaskNewRequest(t, bucket, "s3://"+bucket+"/calibration/cameras.json"))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid cameras")
	assert.Empty(t, wfClient.createdNames, "no workflow should be submitted")
}

func TestTaskNew_RejectsInvalidInlineCameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-inline-invalid"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")

	wfClient := &recordingWorkflowClient{}
	_, handler := NewAPI(meta.NewStore(db), wfClient)

	w := postTaskNew(t, handler, camerasTaskNewRequest(t, bucket, shortIDCamerasJSON))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "space-separated")
	assert.Empty(t, wfClient.createdNames)
}

func TestTaskRestart_ReplaysStoredCameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-restart"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")

	metadataStore := meta.NewStore(db)
	_, err := metadataStore.CreateJob(ctx, "old-wf", "project",
		"s3://"+bucket+"/images/", "s3://"+bucket+"/output/", []string{"--fast-orthophoto"}, "us-east-1",
		map[string]any{
			metadataCamerasJSONKey: validCamerasJSON,
			metadataS3EndpointKey:  "http://" + testutil.TestS3Endpoint(),
		})
	require.NoError(t, err)

	var submitted *workflows.ODMPipelineConfig
	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			submitted = cfg
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "new-wf"}}, nil
		},
	}
	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodPost, "/task/restart", bytes.NewReader([]byte(`{"uuid":"old-wf"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.NotNil(t, submitted)
	assert.Equal(t, validCamerasJSON, submitted.Cameras.Inline)
}

// New options replace stored options; omitting cameras clears calibration.
func TestTaskRestart_NewOptionsOverrideStoredCameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	bucket := "test-bucket-cameras-restart-override"
	require.NoError(t, testutil.SetupTestS3Bucket(ctx, bucket))
	ensureTestImageInBucket(ctx, t, bucket, "images/input.jpg")

	metadataStore := meta.NewStore(db)
	_, err := metadataStore.CreateJob(ctx, "old-wf", "project",
		"s3://"+bucket+"/images/", "s3://"+bucket+"/output/", []string{"--fast-orthophoto"}, "us-east-1",
		map[string]any{
			metadataCamerasJSONKey: validCamerasJSON,
			metadataS3EndpointKey:  "http://" + testutil.TestS3Endpoint(),
		})
	require.NoError(t, err)

	var submitted *workflows.ODMPipelineConfig
	wfClient := &recordingWorkflowClient{
		createFn: func(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
			submitted = cfg
			return &wfv1.Workflow{ObjectMeta: metav1.ObjectMeta{Name: "new-wf"}}, nil
		},
	}
	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodPost, "/task/restart",
		bytes.NewReader([]byte(`{"uuid":"old-wf","options":"[{\"name\":\"dsm\",\"value\":true}]"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.NotNil(t, submitted)
	assert.False(t, submitted.Cameras.IsSet(), "dropping the option must clear the stored calibration")

	job, err := metadataStore.GetJob(ctx, "new-wf")
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Empty(t, metadataWorkspaceFiles(job.Metadata))
}

func TestTaskInfo_ReportsStoredCameras(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	metadataStore := meta.NewStore(db)
	_, err := metadataStore.CreateJob(ctx, "info-wf", "project",
		"s3://test-bucket/images/", "s3://test-bucket/output/", []string{"--fast-orthophoto"}, "us-east-1",
		map[string]any{metadataCamerasS3PathKey: "s3://test-bucket/calibration/cameras.json"})
	require.NoError(t, err)

	_, handler := NewAPI(metadataStore, &recordingWorkflowClient{})

	req := httptest.NewRequest(http.MethodGet, "/task/info-wf/info", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var info TaskInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))

	var found bool
	for _, opt := range info.Options {
		if opt.Name == workflows.CamerasOptionName {
			found = true
			assert.Equal(t, "s3://test-bucket/calibration/cameras.json", opt.Value)
		}
	}
	assert.True(t, found, "cameras should be reported back in task info")
}
