// Global API config

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"

	"github.com/hotosm/scaleodm/queue"
)

// Make the API and JobQueue available on each endpoint
type API struct {
	queue *queue.Queue
	api   huma.API
}

// NewAPI creates the Huma API and registers routes.
// It returns the API object and the HTTP handler (stdlib mux) that should be served.
func NewAPI(queue *queue.Queue) (*API, http.Handler) {
	config := huma.DefaultConfig("ScaleODM API", "1.0.0")
	config.DocsPath = "/"
	config.OpenAPIPath = "/openapi.json"
	config.Servers = []*huma.Server{
		{URL: "http://localhost:8080", Description: "ScaleODM"},
	}

	mux := http.NewServeMux()
	humaAPI := humago.New(mux, config)
	a := &API{queue: queue, api: humaAPI}

	a.registerGlobalMRoutes()
	a.registerNodeODMRoutes()
	a.registerScaleODMRoutes()

	return a, mux
}

func (a *API) registerGlobalMRoutes() {
	// Health check
	huma.Register(a.api, huma.Operation{
		OperationID: "health-check",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Description: "Returns service health status",
		Tags:        []string{"System"},
	}, func(ctx context.Context, input *struct{}) (*HealthResponse, error) {
		if err := a.queue.HealthCheck(ctx); err != nil {
			return nil, huma.NewError(503, "Database unavailable", err)
		}
		resp := &HealthResponse{}
		resp.Body.HealthStatus = "healthy"
		resp.Body.Timestamp = time.Now().UTC().Format(time.RFC3339)
		return resp, nil
	})
}
