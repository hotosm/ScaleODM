package api

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return &logs
}

func TestTaskNewErrorLoggingLogsFailureResponse(t *testing.T) {
	logs := captureLogs(t)

	handler := withTaskNewErrorLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "validation failed", http.StatusBadRequest)
	}))

	req := httptest.NewRequest(http.MethodPost, "/task/new", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.Code)
	}
	got := logs.String()
	for _, want := range []string{
		"POST /task/new: request failed",
		"status=400",
		`remote="10.0.0.1:12345"`,
		"validation failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got %q", want, got)
		}
	}
}

func TestTaskNewErrorLoggingIgnoresSuccessAndOtherRoutes(t *testing.T) {
	logs := captureLogs(t)

	handler := withTaskNewErrorLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/task/new" {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "created")
			return
		}
		http.Error(w, "not logged", http.StatusBadRequest)
	}))

	successReq := httptest.NewRequest(http.MethodPost, "/task/new", nil)
	successResp := httptest.NewRecorder()
	handler.ServeHTTP(successResp, successReq)

	otherReq := httptest.NewRequest(http.MethodPost, "/task/restart", nil)
	otherResp := httptest.NewRecorder()
	handler.ServeHTTP(otherResp, otherReq)

	if got := logs.String(); got != "" {
		t.Fatalf("expected no logs, got %q", got)
	}
}
