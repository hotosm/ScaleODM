package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setInitializedForTest(t *testing.T, value bool) {
	t.Helper()
	initializedMu.Lock()
	initialized = value
	initializedMu.Unlock()
}

func TestNormalizeRoute(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "task new", input: "/task/new", expected: "/task/new"},
		{name: "task info", input: "/task/abc/info", expected: "/task/{uuid}/info"},
		{name: "task output", input: "/task/abc/output", expected: "/task/{uuid}/output"},
		{name: "task download", input: "/task/abc/download/all.zip", expected: "/task/{uuid}/download/{asset}"},
		{name: "empty path", input: "", expected: "unknown"},
		{name: "other path", input: "/ready", expected: "/ready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeRoute(tt.input))
		})
	}
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, "fallback", normalize("", "fallback"))
	assert.Equal(t, "fallback", normalize("   ", "fallback"))
	assert.Equal(t, "value", normalize("value", "fallback"))
}

func TestInitDisabledReturnsNoop(t *testing.T) {
	setInitializedForTest(t, false)

	shutdown, err := Init(context.Background(), Config{Enabled: false})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
	assert.False(t, IsEnabled())
}

func TestInitEnabledMissingEndpointFails(t *testing.T) {
	setInitializedForTest(t, false)

	shutdown, err := Init(context.Background(), Config{Enabled: true})
	require.Error(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
	assert.False(t, IsEnabled())
}

func TestWrapHTTPHandlerWhenDisabledReturnsOriginal(t *testing.T) {
	setInitializedForTest(t, false)

	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	wrapped := WrapHTTPHandler(h)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestWrapHTTPHandlerWhenEnabledServesRequests(t *testing.T) {
	setInitializedForTest(t, true)
	defer setInitializedForTest(t, false)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	wrapped := WrapHTTPHandler(h)

	req := httptest.NewRequest(http.MethodGet, "/task/123/info", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestRecordersDoNotPanicWithoutInstruments(t *testing.T) {
	originalTaskNewTotal := taskNewTotal
	originalTaskNewDuration := taskNewDuration
	originalWorkflowCreateTotal := workflowCreateTotal
	originalWorkflowCreateDuration := workflowCreateDuration
	originalWorkflowReconciliationTotal := workflowReconciliationTotal
	originalJobStatusUpdateTotal := jobStatusUpdateTotal
	originalReadinessChecksTotal := readinessChecksTotal
	originalReadinessDependencyFailures := readinessDependencyFailures
	originalReadinessDuration := readinessDuration
	defer func() {
		taskNewTotal = originalTaskNewTotal
		taskNewDuration = originalTaskNewDuration
		workflowCreateTotal = originalWorkflowCreateTotal
		workflowCreateDuration = originalWorkflowCreateDuration
		workflowReconciliationTotal = originalWorkflowReconciliationTotal
		jobStatusUpdateTotal = originalJobStatusUpdateTotal
		readinessChecksTotal = originalReadinessChecksTotal
		readinessDependencyFailures = originalReadinessDependencyFailures
		readinessDuration = originalReadinessDuration
	}()

	taskNewTotal = nil
	taskNewDuration = nil
	workflowCreateTotal = nil
	workflowCreateDuration = nil
	workflowReconciliationTotal = nil
	jobStatusUpdateTotal = nil
	readinessChecksTotal = nil
	readinessDependencyFailures = nil
	readinessDuration = nil

	assert.NotPanics(t, func() {
		RecordTaskNew("success", "none", 50*time.Millisecond)
		RecordWorkflowCreate("failure", "argo_create_failed", 75*time.Millisecond)
		RecordWorkflowReconciliation("running_to_failed", "infra_failure")
		RecordJobStatusUpdate("success", "running", "none", 20*time.Millisecond)
		RecordReadinessCheck(false, 10*time.Millisecond)
		RecordReadinessDependencyFailure("s3", "s3_probe_failed")
	})
}
