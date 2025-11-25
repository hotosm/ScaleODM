package workflows

import (
	"context"
	"io"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
)

// WorkflowClient defines the interface for workflow operations
// This allows for mocking in tests
type WorkflowClient interface {
	CreateODMWorkflow(ctx context.Context, config *ODMPipelineConfig) (*wfv1.Workflow, error)
	GetWorkflow(ctx context.Context, name string) (*wfv1.Workflow, error)
	ListWorkflows(ctx context.Context, labelSelector string) (*wfv1.WorkflowList, error)
	DeleteWorkflow(ctx context.Context, name string) error
	GetWorkflowLogs(ctx context.Context, workflowName string, writer io.Writer) error
	GetWorkflowLogsWithS3Path(ctx context.Context, workflowName, writeS3Path string, s3Client interface{}, writer io.Writer) error
	WatchWorkflow(ctx context.Context, workflowName string) (*wfv1.Workflow, error)
	GetWorkflowStatus(ctx context.Context, workflowName string) (wfv1.WorkflowPhase, string, error)
	IsWorkflowComplete(ctx context.Context, workflowName string) (bool, error)
}

