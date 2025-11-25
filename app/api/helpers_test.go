package api

import (
	"testing"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestWorkflowToStatusCode(t *testing.T) {
	tests := []struct {
		name     string
		phase    wfv1.WorkflowPhase
		expected int
	}{
		{"Pending", wfv1.WorkflowPending, StatusCodeQueued},
		{"Running", wfv1.WorkflowRunning, StatusCodeRunning},
		{"Succeeded", wfv1.WorkflowSucceeded, StatusCodeCompleted},
		{"Failed", wfv1.WorkflowFailed, StatusCodeFailed},
		{"Error", wfv1.WorkflowError, StatusCodeFailed},
		{"Unknown", wfv1.WorkflowPhase("Unknown"), StatusCodeQueued},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := workflowToStatusCode(tt.phase)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestWorkflowToProgress(t *testing.T) {
	tests := []struct {
		name     string
		phase    wfv1.WorkflowPhase
		expected int
	}{
		{"Pending", wfv1.WorkflowPending, 0},
		{"Running", wfv1.WorkflowRunning, 50},
		{"Succeeded", wfv1.WorkflowSucceeded, 100},
		{"Failed", wfv1.WorkflowFailed, 0},
		{"Error", wfv1.WorkflowError, 0},
		{"Unknown", wfv1.WorkflowPhase("Unknown"), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := workflowToProgress(tt.phase)
			assert.Equal(t, tt.expected, result)
		})
	}
}

