// Global API config

package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
)

// Make the API, JobQueue, and WorkflowClient available on each endpoint
type API struct {
	api            huma.API
	workflowClient workflows.WorkflowClient
	metadataStore  *meta.Store
}

// NewAPI creates the Huma API and registers routes.
// It returns the API object and the HTTP handler (stdlib mux) that should be served.
func NewAPI(metadataStore *meta.Store, workflowClient workflows.WorkflowClient) (*API, http.Handler) {
	apiConfig := huma.DefaultConfig("ScaleODM API", "0.1.0")
	apiConfig.DocsPath = "/"
	apiConfig.OpenAPIPath = "/openapi.json"
	apiConfig.Servers = []*huma.Server{
		{URL: config.SCALEODM_CLUSTER_URL, Description: "ScaleODM"},
	}
	apiConfig.Info.Description = "Kubernetes-native auto-scaling and load balancing for OpenDroneMap."
	apiConfig.Info.Contact = &huma.Contact{
		Name: "HOTOSM",
		URL:  "https://slack.hotosm.org",
	}
	apiConfig.Info.License = &huma.License{
		Name: "AGPL-3.0-only",
		URL:  "https://opensource.org/licenses/agpl-v3",
	}
	apiConfig.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"tokenAuth": {
			Type: "apiKey",
			In:   "query",
			Name: "token",
		},
	}
	apiConfig.Security = []map[string][]string{
		{"tokenAuth": {}},
		{}, // token is optional for compatibility with existing NodeODM behavior
	}
	if apiConfig.Components.Responses == nil {
		apiConfig.Components.Responses = map[string]*huma.Response{}
	}
	apiConfig.Components.Responses["BadRequest"] = &huma.Response{
		Description: "Bad Request",
		Content: map[string]*huma.MediaType{
			"application/problem+json": {
				Schema: &huma.Schema{
					Ref: "#/components/schemas/ErrorModel",
				},
			},
		},
	}
	apiConfig.OnAddOperation = append(apiConfig.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
		if op == nil {
			return
		}
		if op.Responses == nil {
			op.Responses = map[string]*huma.Response{}
		}
		if has4xxResponse(op.Responses) {
			return
		}
		op.Responses["400"] = &huma.Response{Ref: "#/components/responses/BadRequest"}
	})

	router := http.NewServeMux()
	humaAPI := humago.New(router, apiConfig)
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

func has4xxResponse(responses map[string]*huma.Response) bool {
	for statusCode := range responses {
		normalized := strings.ToUpper(strings.TrimSpace(statusCode))
		if len(normalized) == 3 && normalized[0] == '4' {
			return true
		}
		if normalized == "4XX" {
			return true
		}
	}
	return false
}

func (a *API) registerGlobalMRoutes() {
	// Health check
	huma.Register(a.api, huma.Operation{
		OperationID: "health-check",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Description: "Returns service health status",
		Tags:        []string{"system"},
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
