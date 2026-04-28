package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/stretchr/testify/assert"
)

func TestUIRoutesDisabledByDefault(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := &recordingWorkflowClient{}

	prevEnabled := config.SCALEODM_UI_ENABLED
	prevReadonly := config.SCALEODM_UI_READONLY
	config.SCALEODM_UI_ENABLED = false
	config.SCALEODM_UI_READONLY = true
	t.Cleanup(func() {
		config.SCALEODM_UI_ENABLED = prevEnabled
		config.SCALEODM_UI_READONLY = prevReadonly
	})

	_, handler := NewAPI(metadataStore, wfClient)

	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUIRoutesEnabledAlongsideExistingRoutes(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	wfClient := &recordingWorkflowClient{}

	prevEnabled := config.SCALEODM_UI_ENABLED
	prevReadonly := config.SCALEODM_UI_READONLY
	config.SCALEODM_UI_ENABLED = true
	config.SCALEODM_UI_READONLY = true
	t.Cleanup(func() {
		config.SCALEODM_UI_ENABLED = prevEnabled
		config.SCALEODM_UI_READONLY = prevReadonly
	})

	_, handler := NewAPI(metadataStore, wfClient)

	uiReq := httptest.NewRequest(http.MethodGet, "/ui", nil)
	uiResp := httptest.NewRecorder()
	handler.ServeHTTP(uiResp, uiReq)
	assert.Equal(t, http.StatusOK, uiResp.Code)

	uiPostReq := httptest.NewRequest(http.MethodPost, "/ui", nil)
	uiPostResp := httptest.NewRecorder()
	handler.ServeHTTP(uiPostResp, uiPostReq)
	assert.Equal(t, http.StatusMethodNotAllowed, uiPostResp.Code)

	docsReq := httptest.NewRequest(http.MethodGet, "/", nil)
	docsResp := httptest.NewRecorder()
	handler.ServeHTTP(docsResp, docsReq)
	assert.Equal(t, http.StatusOK, docsResp.Code)

	openAPIReq := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	openAPIResp := httptest.NewRecorder()
	handler.ServeHTTP(openAPIResp, openAPIReq)
	assert.Equal(t, http.StatusOK, openAPIResp.Code)

	downloadReq := httptest.NewRequest(http.MethodGet, "/task/unknown/download/all.zip", nil)
	downloadResp := httptest.NewRecorder()
	handler.ServeHTTP(downloadResp, downloadReq)
	assert.Equal(t, http.StatusNotFound, downloadResp.Code)
}
