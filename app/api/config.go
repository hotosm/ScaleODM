// Global API config

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
)

// Make the API, JobQueue, and WorkflowClient available on each endpoint
type API struct {
	api            huma.API
	workflowClient *workflows.Client
	metadataStore  *meta.Store
}

// NewAPI creates the Huma API and registers routes.
// It returns the API object and the HTTP handler (stdlib mux) that should be served.
func NewAPI(metadataStore *meta.Store, workflowClient *workflows.Client) (*API, http.Handler) {
	config := huma.DefaultConfig("ScaleODM API", "1.0.0")
	config.DocsPath = "/"
	config.OpenAPIPath = "/openapi.json"
	config.Servers = []*huma.Server{
		{URL: "http://localhost:8080", Description: "ScaleODM"},
	}
	config.Info.Description = "Kubernetes-native auto-scaling and load balancing for OpenDroneMap."
	config.Info.Contact = &huma.Contact{
		Name: "Sam Woodcock",
		URL:  "https://slack.hotosm.org",
	}
	config.Info.License = &huma.License{
		Name: "AGPL-3.0-only",
		URL:  "https://opensource.org/licenses/agpl-v3",
	}

	router := http.NewServeMux()
	humaAPI := humago.New(router, config)
	apiObj := &API{
		metadataStore:  metadataStore,
		workflowClient: workflowClient,
		api:            humaAPI,
	}

	apiObj.registerGlobalMRoutes()
	apiObj.registerNodeODMRoutes()
	// apiObj.registerScaleODMRoutes()

	return apiObj, router
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
		if err := a.metadataStore.HealthCheck(ctx); err != nil {
			return nil, huma.NewError(503, "Database unavailable", err)
		}
		resp := &HealthResponse{}
		resp.Body.HealthStatus = "healthy"
		resp.Body.Timestamp = time.Now().UTC().Format(time.RFC3339)
		return resp, nil
	})
}
