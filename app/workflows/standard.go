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

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/s3"
)

// Client wraps the Argo Workflows client and Kubernetes client
// Ensure Client implements WorkflowClient interface
var _ WorkflowClient = (*Client)(nil)

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

// ODMPipelineConfig holds configuration for ODM pipeline workflow
type ODMPipelineConfig struct {
	ODMProjectID   string
	ReadS3Path     string   // S3 path where raw imagery is located (can contain zips)
	WriteS3Path    string   // S3 path where final ODM outputs will be written
	ODMFlags       []string // ODM command line flags
	S3Region       string
	S3Endpoint     string            // Optional custom S3 endpoint for non-AWS providers
	S3Credentials  *s3.S3Credentials // S3 credentials for the workflow
	ServiceAccount string
	RcloneImage    string
	ODMImage       string
}

// NewDefaultODMConfig returns default configuration
// Note: S3Credentials must be set separately before creating the workflow (always required)
func NewDefaultODMConfig(odmProjectID, readS3Path, writeS3Path string, odmFlags []string) *ODMPipelineConfig {
	return &ODMPipelineConfig{
		ODMProjectID:   odmProjectID,
		ReadS3Path:     readS3Path,
		WriteS3Path:    writeS3Path,
		ODMFlags:       odmFlags,
		S3Region:       "us-east-1",
		S3Endpoint:     "",
		S3Credentials:  nil, // Must be set before creating workflow (always required)
		ServiceAccount: "argo-odm",
		RcloneImage:    "docker.io/rclone/rclone:1",
		ODMImage:       config.SCALEODM_ODM_IMAGE,
	}
}

// CreateODMWorkflow creates and submits an ODM processing workflow
func (c *Client) CreateODMWorkflow(ctx context.Context, config *ODMPipelineConfig) (*wfv1.Workflow, error) {
	wf := c.buildODMWorkflow(config)

	created, err := c.wfClientset.ArgoprojV1alpha1().Workflows(c.namespace).Create(
		ctx,
		wf,
		metav1.CreateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow: %w", err)
	}

	return created, nil
}

// buildODMWorkflow constructs the workflow specification
func (c *Client) buildODMWorkflow(config *ODMPipelineConfig) *wfv1.Workflow {
	// Credentials are always required
	if config.S3Credentials == nil {
		panic("S3Credentials must be provided - credentials are required for all S3 operations")
	}

	// Configure AWS/S3 credentials via environment variables
	// Note: We don't use RCLONE_CONFIG_* env vars because ContainerSet filters them
	// Instead, we create rclone config on-the-fly in the scripts using AWS/S3 env vars
	awsEnv := []apiv1.EnvVar{
		// Credentials for S3-compatible access (these are NOT filtered by ContainerSet)
		{Name: "AWS_ACCESS_KEY_ID", Value: config.S3Credentials.AccessKeyID},
		{Name: "AWS_SECRET_ACCESS_KEY", Value: config.S3Credentials.SecretAccessKey},
		{Name: "AWS_DEFAULT_REGION", Value: config.S3Region},
	}
	// Add session token if using STS credentials
	if config.S3Credentials.SessionToken != "" {
		awsEnv = append(awsEnv, apiv1.EnvVar{
			Name:  "AWS_SESSION_TOKEN",
			Value: config.S3Credentials.SessionToken,
		})
	}

	// If a custom S3 endpoint is specified (e.g., for MinIO), expose it as an env var
	if config.S3Endpoint != "" {
		awsEnv = append(awsEnv, apiv1.EnvVar{
			Name:  "AWS_S3_ENDPOINT",
			Value: config.S3Endpoint,
		})
	}

	// Generate unique job ID for this workflow instance
	jobID := "{{workflow.name}}"

	// Download container - downloads from readS3Path and extracts zips
	// Uses include filters to only download image files and archives
	// Logs are written to shared workspace for later collection
	downloadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "download",
			Image:   config.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				// Redirect output to log file in workspace for later collection
				// Use workflow name template directly in tee path since it's templated by Argo
				s3.GenerateDownloadScript(jobID, config.ReadS3Path) + " 2>&1 | tee /workspace/{{workflow.name}}/.download.log",
			},
			Env: awsEnv,
		},
	}

	// ODM processing container
	// Logs are written to shared workspace for later collection
	odmFlagsStr := strings.Join(config.ODMFlags, " ")
	odmContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "process",
			Image:   config.ODMImage,
			Command: []string{"/bin/bash", "-c"},
			Args: []string{
				fmt.Sprintf(`
set -e
JOB_ID="{{workflow.name}}"
LOG_FILE="/workspace/$JOB_ID/.process.log"
echo "Running ODM processing..." | tee -a "$LOG_FILE"
echo "Processing job: $JOB_ID" | tee -a "$LOG_FILE"
echo "ODM Project ID: %s" | tee -a "$LOG_FILE"
odm_args="%s --project-path /workspace $JOB_ID"
echo "Executing: python3 run.py $odm_args" | tee -a "$LOG_FILE"
python3 run.py $odm_args 2>&1 | tee -a "$LOG_FILE"
echo "ODM processing complete" | tee -a "$LOG_FILE"
				`, config.ODMProjectID, odmFlagsStr),
			},
		},
		Dependencies: []string{"download"},
	}

	// Upload container - uploads results to writeS3Path
	// Logs are written to shared workspace for later collection
	uploadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "upload",
			Image:   config.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				// Redirect output to log file in workspace for later collection
				// Use workflow name template directly in tee path since it's templated by Argo
				s3.GenerateUploadScript(config.WriteS3Path) + " 2>&1 | tee /workspace/{{workflow.name}}/.upload.log",
			},
			Env: awsEnv,
		},
		Dependencies: []string{"process"},
	}

	// Cleanup container - collects logs and uploads to S3, then workflow will be deleted
	// This runs after upload to preserve logs before workflow cleanup
	cleanupContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "cleanup",
			Image:   config.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				s3.GenerateLogUploadScript(config.WriteS3Path),
			},
			Env: append(awsEnv,
				// Add namespace for log collection
				apiv1.EnvVar{
					Name:  "ARGO_NAMESPACE",
					Value: c.namespace,
				},
			),
		},
		Dependencies: []string{"upload"},
	}

	// Create workflow
	wf := &wfv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "odm-pipeline-",
			Namespace:    c.namespace,
		},
		Spec: wfv1.WorkflowSpec{
			Entrypoint:         "main",
			ServiceAccountName: config.ServiceAccount,
			Templates: []wfv1.Template{
				{
					Name: "main",
					Volumes: []apiv1.Volume{
						{
							Name: "workspace",
							VolumeSource: apiv1.VolumeSource{
								EmptyDir: &apiv1.EmptyDirVolumeSource{},
							},
						},
					},
					ContainerSet: &wfv1.ContainerSetTemplate{
						VolumeMounts: []apiv1.VolumeMount{
							{
								Name:      "workspace",
								MountPath: "/workspace",
							},
						},
						Containers: []wfv1.ContainerNode{
							downloadContainer,
							odmContainer,
							uploadContainer,
							cleanupContainer,
						},
					},
				},
			},
		},
	}

	return wf
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
func (c *Client) getWorkflowLogsFromPods(ctx context.Context, wf *wfv1.Workflow, writer io.Writer) error {
	// Get logs for each node in the workflow
	for nodeName, node := range wf.Status.Nodes {
		if node.Type != wfv1.NodeTypePod {
			continue
		}

		fmt.Fprintf(writer, "\n=== Logs for node: %s ===\n", nodeName)

		// Get pod logs using Kubernetes client
		podName := node.ID
		podClient := c.k8sClient.CoreV1().Pods(c.namespace)

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
			defer stream.Close()

			_, err = io.Copy(writer, stream)
			if err != nil {
				fmt.Fprintf(writer, "Warning: failed to copy logs: %v\n", err)
			}
		}
	}

	return nil
}

// getWorkflowLogsFromS3 attempts to fetch workflow logs from S3
// This is used when the workflow has been cleaned up
// writeS3Path is the S3 path where logs should be stored (e.g., s3://bucket/path/)
// s3Client is the minio client to use for fetching logs
// Note: This function is a placeholder - the API layer calls s3.GetWorkflowLogsFromS3 directly
func (c *Client) getWorkflowLogsFromS3(ctx context.Context, workflowName, writeS3Path string, s3Client interface{}, writer io.Writer) error {
	fmt.Fprintf(writer, "Workflow %s not found (may have been cleaned up).\n", workflowName)
	fmt.Fprintf(writer, "Attempting to fetch logs from S3...\n\n")
	
	// Validate S3 path format
	if !strings.HasPrefix(writeS3Path, "s3://") {
		return fmt.Errorf("invalid S3 path: %s", writeS3Path)
	}
	
	// The API layer should call s3.GetWorkflowLogsFromS3 directly
	// This function signature is kept for compatibility but the actual
	// S3 fetch is done in the API layer to avoid circular dependencies
	return fmt.Errorf("S3 log retrieval should be handled by API layer using s3.GetWorkflowLogsFromS3")
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
