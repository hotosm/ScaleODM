// General schemas not specific to a router

package api

// General

type HealthResponse struct {
	Body struct {
		HealthStatus string `json:"status" example:"healthy"`
		Timestamp    string `json:"timestamp" example:"2025-04-05T12:00:00Z"`
	}
}

type ReadinessResponse struct {
	Body struct {
		Ready     bool              `json:"ready" example:"true"`
		Status    string            `json:"status" example:"ready"`
		Timestamp string            `json:"timestamp" example:"2025-04-05T12:00:00Z"`
		Checks    map[string]string `json:"checks,omitempty"`
	}
}

type MessageResponse struct {
	Body struct {
		Message string `json:"message"`
	}
}
