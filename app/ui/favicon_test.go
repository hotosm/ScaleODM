package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFaviconServed(t *testing.T) {
	h, err := NewHandler(nil, nil, false, "test")
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/static/favicon.svg", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" || ct[:5] != "image" {
		t.Errorf("content-type = %q, want image/svg+xml", ct)
	}

	// HEAD must also succeed: the security middleware once rejected every
	// non-GET method with 405, breaking curl -I and uptime probes.
	headReq := httptest.NewRequest(http.MethodHead, "/ui/static/favicon.svg", nil)
	headRec := httptest.NewRecorder()
	mux.ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", headRec.Code)
	}
}
