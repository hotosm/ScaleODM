package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hotosm/scaleodm/app/meta"
)

func TestFormCompat_RewritesFormBodyToJSON(t *testing.T) {
	var gotContentType, gotBody string
	handler := withFormCompat(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
	}))

	form := `uuid=old-wf&options=%5B%7B%22name%22%3A%22dsm%22%2C%22value%22%3Atrue%7D%5D`
	req := httptest.NewRequest(http.MethodPost, "/task/restart?token=abc", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "application/json", gotContentType)
	assert.JSONEq(t, `{"uuid":"old-wf","options":"[{\"name\":\"dsm\",\"value\":true}]"}`, gotBody)
}

func TestFormCompat_LeavesOtherRequestsUntouched(t *testing.T) {
	cases := []struct {
		name, method, path, contentType, body string
	}{
		{"json body", http.MethodPost, "/task/cancel", "application/json", `{"uuid":"x"}`},
		{"other route", http.MethodPost, "/task/new", "application/x-www-form-urlencoded", "name=x"},
		{"get request", http.MethodGet, "/task/cancel", "application/x-www-form-urlencoded", "uuid=x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotContentType, gotBody string
			handler := withFormCompat(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotContentType = r.Header.Get("Content-Type")
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
			}))

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tc.contentType, gotContentType)
			assert.Equal(t, tc.body, gotBody)
		})
	}
}

func TestTaskCancel_AcceptsPyODMFormRequest(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	wfClient := &recordingWorkflowClient{
		deleteFn: func(ctx context.Context, name string) error { return nil },
	}
	_, handler := NewAPI(meta.NewStore(db), wfClient)

	req := httptest.NewRequest(http.MethodPost, "/task/cancel", strings.NewReader("uuid=wf-form-cancel"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"success":true}`, w.Body.String())
	assert.Equal(t, []string{"wf-form-cancel"}, wfClient.deletedNames)
}
