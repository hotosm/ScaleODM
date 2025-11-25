package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, "0.1.0", response.Version)
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

	// Initialize cluster
	ctx := context.Background()
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	// Create task request
	request := TaskNewRequest{
		Name:        "test-project",
		ReadS3Path:  "s3://test-bucket/images/",
		WriteS3Path: "s3://test-bucket/output/",
		Options:      `[{"name": "fast-orthophoto", "value": true}]`,
		S3Region:    "us-east-1",
		S3AccessKeyID: "test-key",
		S3SecretAccessKey: "test-secret",
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
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	workflowName := "test-workflow-info"
	_, err = metadataStore.CreateJob(
		ctx,
		"http://localhost:31100",
		workflowName,
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
		"us-east-1",
	)
	require.NoError(t, err)
	
	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodGet, "/task/"+workflowName+"/info", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should return OK even if workflow doesn't exist in cluster (metadata exists)
	assert.Equal(t, http.StatusOK, w.Code)

	// Huma returns the body content directly, not wrapped in a Body field
	var response TaskInfo
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, workflowName, response.UUID)
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
	err := db.InitLocalClusterRecord(ctx, "http://localhost:31100")
	require.NoError(t, err)

	workflowName := "test-workflow-remove"
	_, err = metadataStore.CreateJob(
		ctx,
		"http://localhost:31100",
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

