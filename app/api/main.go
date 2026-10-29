// Global API config

package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	_ "github.com/danielgtaylor/huma/v2/formats/cbor"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/ui"
	"github.com/hotosm/scaleodm/app/version"
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
	apiConfig := huma.DefaultConfig("ScaleODM API", version.Version)
	apiConfig.DocsPath = "/"
	apiConfig.OpenAPIPath = "/openapi.json"
	apiConfig.Servers = []*huma.Server{
		{URL: "/", Description: "ScaleODM"},
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

	// Raw routes (outside Huma) so downloads can redirect to pre-signed URLs.
	// The /ui mirror is reachable through the UI-only ingress.
	router.HandleFunc("GET /task/{uuid}/download/{asset}", apiObj.downloadTaskAsset)
	router.HandleFunc("GET /ui/api/tasks/{uuid}/download/{asset}", apiObj.downloadTaskAsset)

	if config.SCALEODM_UI_ENABLED {
		uiHandler, err := ui.NewHandler(metadataStore, workflowClient, config.SCALEODM_UI_READONLY, version.Version)
		if err != nil {
			log.Printf("ui disabled: failed to initialize handler: %v", err)
		} else {
			uiHandler.RegisterRoutes(router)
		}
	}

	// withTaskNewErrorLogging wraps the whole router rather than a specific handler
	// because POST /task/new is registered via Huma, which doesn't expose the
	// underlying http.Handler for per-route wrapping. The middleware short-circuits
	// immediately for all other routes, so the overhead is a single conditional.
	return apiObj, withTaskNewErrorLogging(withFormCompat(router))
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
	lightweightHandler := func(ctx context.Context, input *struct{}) (*HealthResponse, error) {
		resp := &HealthResponse{}
		resp.Body.HealthStatus = "healthy"
		resp.Body.Timestamp = time.Now().UTC().Format(time.RFC3339)
		return resp, nil
	}

	dependencyAwareHandler := func(ctx context.Context, input *struct{}) (*ReadinessResponse, error) {
		_ = input
		checks := map[string]string{}
		ready := true
		fail := func(dependency, reason, detail string, err error) {
			ready = false
			checks[dependency] = "unavailable: " + detail
			if err != nil {
				log.Printf("readiness dependency failure dependency=%s reason=%s error=%v", dependency, reason, err)
			} else {
				log.Printf("readiness dependency failure dependency=%s reason=%s", dependency, reason)
			}
		}

		if err := a.metadataStore.HealthCheck(ctx); err != nil {
			fail("database", "db_healthcheck_failed", err.Error(), err)
		} else {
			checks["database"] = "ok"
		}

		if a.workflowClient == nil {
			fail("argo", "argo_client_missing", "workflow client not initialized", nil)
		} else if _, err := a.workflowClient.ListWorkflows(ctx, ""); err != nil {
			if errors.IsUnauthorized(err) || errors.IsForbidden(err) {
				fail("argo", "argo_forbidden", fmt.Sprintf("insufficient permissions (%v)", err), err)
			} else {
				fail("argo", "argo_list_failed", err.Error(), err)
			}
		} else {
			checks["argo"] = "ok"
		}

		if config.SCALEODM_READINESS_CHECK_S3 {
			a.checkS3Readiness(ctx, checks, fail)
		}

		resp := &ReadinessResponse{}
		resp.Body.Ready = ready
		resp.Body.Status = "ready"
		if !ready {
			resp.Body.Status = "not_ready"
		}
		resp.Body.Timestamp = time.Now().UTC().Format(time.RFC3339)
		resp.Body.Checks = checks

		if !ready {
			return nil, huma.NewError(http.StatusServiceUnavailable, "Dependencies not ready")
		}
		return resp, nil
	}

	huma.Register(a.api, huma.Operation{
		OperationID: "lb-heartbeat",
		Method:      http.MethodGet,
		Path:        "/__lbheartbeat__",
		Summary:     "Load balancer heartbeat",
		Description: "Returns lightweight process heartbeat",
		Tags:        []string{"system"},
	}, lightweightHandler)

	huma.Register(a.api, huma.Operation{
		OperationID: "heartbeat",
		Method:      http.MethodGet,
		Path:        "/__heartbeat__",
		Summary:     "Dependency heartbeat",
		Description: "Returns dependency readiness status",
		Tags:        []string{"system"},
	}, dependencyAwareHandler)

	huma.Register(a.api, huma.Operation{
		OperationID: "health-check",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check alias",
		Description: "Compatibility alias for /__lbheartbeat__",
		Tags:        []string{"system"},
	}, lightweightHandler)

	huma.Register(a.api, huma.Operation{
		OperationID: "readiness-check",
		Method:      http.MethodGet,
		Path:        "/ready",
		Summary:     "Readiness check alias",
		Description: "Compatibility alias for /__heartbeat__",
		Tags:        []string{"system"},
	}, dependencyAwareHandler)
}

func (a *API) checkS3Readiness(ctx context.Context, checks map[string]string, fail func(dependency, reason, detail string, err error)) {
	probePath := config.SCALEODM_READINESS_S3_PROBE_PATH
	if strings.TrimSpace(probePath) == "" {
		fail("s3", "s3_probe_path_missing", "SCALEODM_READINESS_S3_PROBE_PATH not configured", nil)
		return
	}
	timeout := time.Duration(config.SCALEODM_READINESS_TIMEOUT_SECONDS) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s3Client, err := s3.GetS3ClientForEndpoint(config.AWS_S3_ENDPOINT)
	if err != nil {
		fail("s3", "s3_client_init_failed", fmt.Sprintf("failed to initialize client: %v", err), err)
		return
	}
	if err := s3.ProbeS3Path(ctx, s3Client, probePath); err != nil {
		fail("s3", "s3_probe_failed", err.Error(), err)
		return
	}
	checks["s3"] = "ok"
}
