package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testWorkflowClient struct {
	logs   string
	phases map[string]wfv1.WorkflowPhase
}

func (c *testWorkflowClient) CreateODMWorkflow(ctx context.Context, cfg *workflows.ODMPipelineConfig) (*wfv1.Workflow, error) {
	return nil, errors.New("not implemented")
}

func (c *testWorkflowClient) GetWorkflow(ctx context.Context, name string) (*wfv1.Workflow, error) {
	phase := c.phases[name]
	return &wfv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     wfv1.WorkflowStatus{Phase: phase},
	}, nil
}

func (c *testWorkflowClient) ListWorkflows(ctx context.Context, labelSelector string) (*wfv1.WorkflowList, error) {
	return &wfv1.WorkflowList{}, nil
}

func (c *testWorkflowClient) DeleteWorkflow(ctx context.Context, name string) error {
	return nil
}

func (c *testWorkflowClient) GetWorkflowLogs(ctx context.Context, workflowName string, writer io.Writer) error {
	_, err := io.WriteString(writer, c.logs)
	return err
}

func (c *testWorkflowClient) GetWorkflowLogsWithArchiveFallback(ctx context.Context, workflowName string, writer io.Writer) error {
	_, err := io.WriteString(writer, c.logs)
	return err
}

func (c *testWorkflowClient) WatchWorkflow(ctx context.Context, workflowName string) (*wfv1.Workflow, error) {
	return nil, errors.New("not implemented")
}

func (c *testWorkflowClient) GetWorkflowStatus(ctx context.Context, workflowName string) (wfv1.WorkflowPhase, string, error) {
	return wfv1.WorkflowRunning, "", nil
}

func (c *testWorkflowClient) IsWorkflowComplete(ctx context.Context, workflowName string) (bool, error) {
	return false, nil
}

func testDB(t *testing.T) (*db.DB, func()) {
	t.Helper()

	database, err := db.NewDB(testutil.TestDBURL())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, database.InitSchema(ctx))

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = database.Pool.Exec(ctx, "TRUNCATE TABLE scaleodm_job_metadata CASCADE")
		database.Close()
	}

	return database, cleanup
}

func setupServer(t *testing.T) (*meta.Store, http.Handler) {
	t.Helper()
	return setupServerWithWorkflowClient(t, &testWorkflowClient{logs: "line0\nline1\nline2\nline3"})
}

func setupServerWithWorkflowClient(t *testing.T, workflow workflows.WorkflowClient) (*meta.Store, http.Handler) {
	t.Helper()
	database, cleanup := testDB(t)
	t.Cleanup(cleanup)

	store := meta.NewStore(database)
	handler, err := NewHandler(store, workflow, true, "test")
	require.NoError(t, err)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return store, mux
}

func setupServerWithoutWorkflowClient(t *testing.T) (*meta.Store, http.Handler) {
	t.Helper()
	database, cleanup := testDB(t)
	t.Cleanup(cleanup)

	store := meta.NewStore(database)
	handler, err := NewHandler(store, nil, true, "test")
	require.NoError(t, err)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return store, mux
}

func TestTasksPageRendersAndEscapes(t *testing.T) {
	store, server := setupServer(t)
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-1", "<script>alert(1)</script>", "s3://bucket/in/", "", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Contains(t, w.Body.String(), "ScaleODM UI")
	assert.Contains(t, w.Body.String(), "No authentication is enabled")
	assert.NotContains(t, w.Body.String(), "<script>alert(1)</script>")
	assert.Contains(t, w.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestTasksJSONFiltersAndLimit(t *testing.T) {
	store, server := setupServer(t)
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-2", "project-a", "s3://bucket/in/", "", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)
	require.NoError(t, store.UpdateJobStatus(ctx, "wf-ui-2", "running", nil))

	_, err = store.CreateJob(ctx, "wf-ui-3", "project-b", "s3://bucket/in/", "", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)
	require.NoError(t, store.UpdateJobStatus(ctx, "wf-ui-3", "completed", nil))

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks?status=running&projectID=project-a&limit=1", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var payload struct {
		Tasks []taskSummary `json:"tasks"`
		Count int           `json:"count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Len(t, payload.Tasks, 1)
	assert.Equal(t, 1, payload.Count)
	assert.Equal(t, "wf-ui-2", payload.Tasks[0].UUID)
	assert.Equal(t, "running", payload.Tasks[0].Status)
}

func TestTasksJSONStatusFilterIsAppliedAfterReconcile(t *testing.T) {
	store, server := setupServerWithWorkflowClient(t, &testWorkflowClient{
		logs: "line0\nline1",
		phases: map[string]wfv1.WorkflowPhase{
			"wf-ui-reconcile-filter": wfv1.WorkflowRunning,
		},
	})
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-reconcile-filter", "project-a", "s3://bucket/in/", "", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks?status=queued&projectID=project-a&limit=25", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var payload struct {
		Tasks []taskSummary `json:"tasks"`
		Count int           `json:"count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Empty(t, payload.Tasks)
	assert.Equal(t, 0, payload.Count)

	job, err := store.GetJob(ctx, "wf-ui-reconcile-filter")
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "running", job.JobStatus)
}

func TestTaskDetailEndpoints(t *testing.T) {
	store, server := setupServer(t)
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-4", "project-detail", "s3://bucket/in/", "s3://bucket/out/", []string{"--orthophoto-resolution=5"}, "us-east-1")
	require.NoError(t, err)

	pageReq := httptest.NewRequest(http.MethodGet, "/ui/tasks/wf-ui-4", nil)
	pageResp := httptest.NewRecorder()
	server.ServeHTTP(pageResp, pageReq)
	assert.Equal(t, http.StatusOK, pageResp.Code)
	assert.Contains(t, pageResp.Body.String(), "wf-ui-4")
	assert.Contains(t, pageResp.Body.String(), "/task/wf-ui-4/download/all.zip")

	jsonReq := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/wf-ui-4", nil)
	jsonResp := httptest.NewRecorder()
	server.ServeHTTP(jsonResp, jsonReq)
	assert.Equal(t, http.StatusOK, jsonResp.Code)

	var detail taskDetail
	require.NoError(t, json.Unmarshal(jsonResp.Body.Bytes(), &detail))
	assert.Equal(t, "wf-ui-4", detail.Task.UUID)
	assert.Equal(t, "project-detail", detail.Task.ProjectID)
	require.NotEmpty(t, detail.Assets)
}

func TestMissingTaskReturns404(t *testing.T) {
	_, server := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/tasks/missing", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/tasks/missing", nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestTaskOutputLineParsing(t *testing.T) {
	store, server := setupServer(t)
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-5", "project-output", "s3://bucket/in/", "", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/wf-ui-5/output?line=2", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "line2")
	assert.NotContains(t, w.Body.String(), "line0")

	req = httptest.NewRequest(http.MethodGet, "/ui/api/tasks/wf-ui-5/output?line=-1", nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTaskOutputWithInvalidS3PathFallsBackToWorkflowLogs(t *testing.T) {
	store, server := setupServer(t)
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-7", "project-output", "s3://bucket/in/", "invalid-path", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/wf-ui-7/output?line=0", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "line0")
	assert.Contains(t, w.Body.String(), "line3")
}

func TestTaskOutputWithoutWorkflowClientReturns500(t *testing.T) {
	store, server := setupServerWithoutWorkflowClient(t)
	ctx := context.Background()

	_, err := store.CreateJob(ctx, "wf-ui-6", "project-output", "s3://bucket/in/", "", []string{"--fast-orthophoto"}, "us-east-1")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/wf-ui-6/output", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInvalidQueryValidation(t *testing.T) {
	_, server := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks?limit=abc", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/tasks?limit=0", nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestStaticRouteServesCSS(t *testing.T) {
	_, server := setupServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/static/ui.css", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, strings.Contains(w.Header().Get("Content-Type"), "text/css") || strings.Contains(w.Header().Get("Content-Type"), "text/plain"))
	assert.Contains(t, w.Body.String(), "body")
}
