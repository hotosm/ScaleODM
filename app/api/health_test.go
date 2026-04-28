package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthCheck(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)

	// The health check should work
	ctx := context.Background()
	err := metadataStore.HealthCheck(ctx)
	require.NoError(t, err)
}

func TestHeartbeatLightweightEndpoints(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	_, handler := NewAPI(metadataStore, nil)

	endpoints := []string{"/__lbheartbeat__", "/health"}
	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var response struct {
				Status string `json:"status"`
			}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)
			assert.Equal(t, "healthy", response.Status)
		})
	}
}

func TestHeartbeatDependencyAwareEndpoints(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	_, handler := NewAPI(metadataStore, nil)

	endpoints := []string{"/__heartbeat__", "/ready"}
	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})
	}
}
