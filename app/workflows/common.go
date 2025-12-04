package workflows

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	workflowclient "github.com/argoproj/argo-workflows/v3/pkg/client/clientset/versioned"
	apiv1 "k8s.io/api/core/v1"

	"github.com/hotosm/scaleodm/app/s3"
	"github.com/minio/minio-go/v7"
)

// Client wraps the Argo Workflows client and Kubernetes client
// Ensure Client implements WorkflowClient interface
var _ WorkflowClient = (*Client)(nil)

// Client provides common workflow operations that are shared across all workflow types
type Client struct {
	wfClientset *workflowclient.Clientset
	k8sClient   *kubernetes.Clientset
	namespace   string
}

// NewClient creates a new Argo Workflows client with Kubernetes client
func NewClient(kubeconfig, namespace string) (*Client, error) {
	var config *rest.Config
	var err error

	if kubeconfig == "" {
		// Use in-cluster config
		config, err = rest.InClusterConfig()
	} else {
		// Use kubeconfig file
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes config: %w", err)
	}

	// Set timeouts to avoid long waits during initialization
	// These are reasonable defaults that prevent hanging
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	// Create Argo Workflows clientset
	wfClientset, err := workflowclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow clientset: %w", err)
	}

	// Create Kubernetes clientset for accessing pods
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &Client{
		wfClientset: wfClientset,
		k8sClient:   k8sClient,
		namespace:   namespace,
	}, nil
}

// GetWorkflow retrieves a workflow by name
func (c *Client) GetWorkflow(ctx context.Context, name string) (*wfv1.Workflow, error) {
	wf, err := c.wfClientset.ArgoprojV1alpha1().Workflows(c.namespace).Get(
		ctx,
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow: %w", err)
	}
	return wf, nil
}

// ListWorkflows lists workflows with optional label selector
func (c *Client) ListWorkflows(ctx context.Context, labelSelector string) (*wfv1.WorkflowList, error) {
	wfList, err := c.wfClientset.ArgoprojV1alpha1().Workflows(c.namespace).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labelSelector,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflows: %w", err)
	}
	return wfList, nil
}

// DeleteWorkflow deletes a workflow by name
func (c *Client) DeleteWorkflow(ctx context.Context, name string) error {
	err := c.wfClientset.ArgoprojV1alpha1().Workflows(c.namespace).Delete(
		ctx,
		name,
		metav1.DeleteOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to delete workflow: %w", err)
	}
	return nil
}

// GetWorkflowLogs retrieves logs for a workflow
// If workflow is deleted/cleaned up, writeS3Path should be provided to fetch from S3
// For backward compatibility, this will try pods first, then return error if not found
func (c *Client) GetWorkflowLogs(ctx context.Context, workflowName string, writer io.Writer) error {
	wf, err := c.GetWorkflow(ctx, workflowName)
	if err != nil {
		// Workflow not found - caller should use GetWorkflowLogsWithS3Path with write path
		return fmt.Errorf("workflow not found: %w (use GetWorkflowLogsWithS3Path to fetch from S3)", err)
	}

	// Workflow exists - get logs from pods
	return c.getWorkflowLogsFromPods(ctx, wf, writer)
}

// getWorkflowLogsFromPods retrieves logs directly from workflow pods
// This is shared across all workflow types
func (c *Client) getWorkflowLogsFromPods(ctx context.Context, wf *wfv1.Workflow, writer io.Writer) error {
	podClient := c.k8sClient.CoreV1().Pods(c.namespace)

	// For ContainerSet workflows, we need to find the main pod
	// Argo creates pods with labels that include the workflow name
	// Try to find pods by workflow name label first
	podList, err := podClient.List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("workflows.argoproj.io/workflow=%s", wf.Name),
	})
	if err == nil && len(podList.Items) > 0 {
		// Found pods by label - get logs from all of them
		for _, pod := range podList.Items {
			fmt.Fprintf(writer, "\n=== Logs for pod: %s ===\n", pod.Name)

			// Get logs for each container in the pod
			for _, container := range pod.Spec.Containers {
				fmt.Fprintf(writer, "\n--- Container: %s ---\n", container.Name)

				req := podClient.GetLogs(pod.Name, &apiv1.PodLogOptions{
					Container: container.Name,
				})

				stream, err := req.Stream(ctx)
				if err != nil {
					fmt.Fprintf(writer, "Warning: failed to get logs for container %s: %v\n", container.Name, err)
					continue
				}

				_, err = io.Copy(writer, stream)
				stream.Close()
				if err != nil {
					fmt.Fprintf(writer, "Warning: failed to copy logs: %v\n", err)
				}
			}
		}
		return nil
	}

	// Fallback: Get logs from workflow nodes (for non-ContainerSet workflows)
	hasLogs := false
	for nodeName, node := range wf.Status.Nodes {
		if node.Type != wfv1.NodeTypePod {
			continue
		}

		hasLogs = true
		fmt.Fprintf(writer, "\n=== Logs for node: %s ===\n", nodeName)

		// Get pod logs using Kubernetes client
		podName := node.ID
		if podName == "" {
			// Try to find pod by node name
			podName = nodeName
		}

		// Get container names from the pod
		pod, err := podClient.Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(writer, "Warning: failed to get pod %s: %v\n", podName, err)
			continue
		}

		// Get logs for each container
		for _, container := range pod.Spec.Containers {
			fmt.Fprintf(writer, "\n--- Container: %s ---\n", container.Name)

			req := podClient.GetLogs(podName, &apiv1.PodLogOptions{
				Container: container.Name,
			})

			stream, err := req.Stream(ctx)
			if err != nil {
				fmt.Fprintf(writer, "Warning: failed to get logs for container %s: %v\n", container.Name, err)
				continue
			}

			_, err = io.Copy(writer, stream)
			stream.Close()
			if err != nil {
				fmt.Fprintf(writer, "Warning: failed to copy logs: %v\n", err)
			}
		}
	}

	if !hasLogs {
		return fmt.Errorf("no pod logs found for workflow %s", wf.Name)
	}

	return nil
}

// getWorkflowLogsFromS3 attempts to fetch workflow logs from S3
// This is used when the workflow has been cleaned up
// writeS3Path is the S3 path where logs should be stored (e.g., s3://bucket/path/)
// s3Client is the minio client to use for fetching logs
func (c *Client) getWorkflowLogsFromS3(ctx context.Context, workflowName, writeS3Path string, s3Client interface{}, writer io.Writer) error {
	fmt.Fprintf(writer, "Workflow %s not found (may have been cleaned up).\n", workflowName)
	fmt.Fprintf(writer, "Attempting to fetch logs from S3...\n\n")

	// Validate S3 path format
	if !strings.HasPrefix(writeS3Path, "s3://") {
		return fmt.Errorf("invalid S3 path: %s", writeS3Path)
	}

	// Type assert s3Client to *minio.Client
	minioClient, ok := s3Client.(*minio.Client)
	if !ok {
		return fmt.Errorf("invalid s3Client type: expected *minio.Client")
	}

	// Fetch logs from S3
	logContent, err := s3.GetWorkflowLogsFromS3(ctx, minioClient, writeS3Path)
	if err != nil {
		return fmt.Errorf("failed to fetch logs from S3: %w", err)
	}

	// Write log content to writer
	_, err = writer.Write([]byte(logContent))
	if err != nil {
		return fmt.Errorf("failed to write logs: %w", err)
	}

	return nil
}

// GetWorkflowLogsWithS3Path retrieves logs for a workflow, with fallback to S3
// writeS3Path is used to fetch logs from S3 if workflow is deleted
// s3Client is the minio client to use for S3 operations (can be nil if workflow exists)
func (c *Client) GetWorkflowLogsWithS3Path(ctx context.Context, workflowName, writeS3Path string, s3Client interface{}, writer io.Writer) error {
	wf, err := c.GetWorkflow(ctx, workflowName)
	if err != nil {
		// Workflow not found - try to fetch logs from S3
		return c.getWorkflowLogsFromS3(ctx, workflowName, writeS3Path, s3Client, writer)
	}

	// Workflow exists - get logs from pods
	return c.getWorkflowLogsFromPods(ctx, wf, writer)
}

// WatchWorkflow watches a workflow until completion and returns the final workflow
func (c *Client) WatchWorkflow(ctx context.Context, workflowName string) (*wfv1.Workflow, error) {
	// First, verify the workflow exists and get initial status
	wf, err := c.GetWorkflow(ctx, workflowName)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow: %w", err)
	}

	// If already complete, return immediately
	if wf.Status.Phase == wfv1.WorkflowSucceeded ||
		wf.Status.Phase == wfv1.WorkflowFailed ||
		wf.Status.Phase == wfv1.WorkflowError {
		return wf, nil
	}

	// Watch for workflow completion, with automatic reconnection on watch failures
	for {
		// Set up watcher
		watcher, err := c.wfClientset.ArgoprojV1alpha1().Workflows(c.namespace).Watch(
			ctx,
			metav1.ListOptions{
				FieldSelector: fmt.Sprintf("metadata.name=%s", workflowName),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to watch workflow: %w", err)
		}

		// Watch for events
		for {
			select {
			case <-ctx.Done():
				// Context cancelled - get final status before returning
				watcher.Stop()
				finalWf, err := c.GetWorkflow(ctx, workflowName)
				if err != nil {
					return nil, fmt.Errorf("context cancelled and failed to get workflow status: %w", err)
				}
				return finalWf, ctx.Err()
			case event, ok := <-watcher.ResultChan():
				if !ok {
					// Channel closed - watcher ended, check final status and potentially restart
					watcher.Stop()
					finalWf, err := c.GetWorkflow(ctx, workflowName)
					if err != nil {
						return nil, fmt.Errorf("watch ended and failed to get workflow status: %w", err)
					}
					// If workflow is complete, return it
					if finalWf.Status.Phase == wfv1.WorkflowSucceeded ||
						finalWf.Status.Phase == wfv1.WorkflowFailed ||
						finalWf.Status.Phase == wfv1.WorkflowError {
						return finalWf, nil
					}
					// Workflow still running - break inner loop to restart watch
					break
				}

				wf, ok := event.Object.(*wfv1.Workflow)
				if !ok {
					continue
				}

				// Check if workflow is complete
				if wf.Status.Phase == wfv1.WorkflowSucceeded ||
					wf.Status.Phase == wfv1.WorkflowFailed ||
					wf.Status.Phase == wfv1.WorkflowError {
					watcher.Stop()
					return wf, nil
				}
			}
			// Break inner loop to restart watch
			break
		}
		// Small delay before restarting watch to avoid tight loop
		select {
		case <-ctx.Done():
			finalWf, err := c.GetWorkflow(ctx, workflowName)
			if err != nil {
				return nil, fmt.Errorf("context cancelled: %w", err)
			}
			return finalWf, ctx.Err()
		case <-time.After(1 * time.Second):
			// Continue to restart watch
		}
	}
}

// GetWorkflowStatus returns the current phase and message of a workflow
func (c *Client) GetWorkflowStatus(ctx context.Context, workflowName string) (wfv1.WorkflowPhase, string, error) {
	wf, err := c.GetWorkflow(ctx, workflowName)
	if err != nil {
		return "", "", err
	}
	return wf.Status.Phase, wf.Status.Message, nil
}

// IsWorkflowComplete checks if a workflow has completed (succeeded, failed, or error)
func (c *Client) IsWorkflowComplete(ctx context.Context, workflowName string) (bool, error) {
	phase, _, err := c.GetWorkflowStatus(ctx, workflowName)
	if err != nil {
		return false, err
	}

	return phase == wfv1.WorkflowSucceeded ||
		phase == wfv1.WorkflowFailed ||
		phase == wfv1.WorkflowError, nil
}


