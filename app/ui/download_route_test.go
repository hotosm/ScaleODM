package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The download route registered in app/api/main.go must coexist with the UI's
// own /ui/api/tasks/{uuid} routes: the more specific .../download/{asset} path
// reaches the download handler, and ServeMux accepts both without panicking.
func TestUIDownloadRouteCoexists(t *testing.T) {
	h, err := NewHandler(nil, nil, false, "test")
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	hit := false
	mux.Handle("GET /ui/api/tasks/{uuid}/download/{asset}", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hit = true
			if got := r.PathValue("uuid"); got != "wf-1" {
				t.Errorf("uuid = %q, want wf-1", got)
			}
			if got := r.PathValue("asset"); got != "orthophoto" {
				t.Errorf("asset = %q, want orthophoto", got)
			}
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/wf-1/download/orthophoto", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if !hit {
		t.Fatal("download route did not reach its handler")
	}
}
