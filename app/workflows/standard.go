package workflows

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	apiv1 "k8s.io/api/core/v1"

	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/s3"
)

// ODMPipelineConfig holds configuration for ODM pipeline workflow
type ODMPipelineConfig struct {
	ODMProjectID   string
	ReadS3Path     string   // S3 path where raw imagery is located (can contain zips)
	WriteS3Path    string   // S3 path where final ODM outputs will be written
	ODMFlags       []string // ODM command line flags
	S3Region       string
	S3Endpoint     string // Optional custom S3 endpoint for non-AWS providers
	ServiceAccount string
	RcloneImage    string
	ODMImage       string
}

// NewDefaultODMConfig returns default configuration
func NewDefaultODMConfig(odmProjectID, readS3Path, writeS3Path string, odmFlags []string) *ODMPipelineConfig {
	return &ODMPipelineConfig{
		ODMProjectID:   odmProjectID,
		ReadS3Path:     readS3Path,
		WriteS3Path:    writeS3Path,
		ODMFlags:       odmFlags,
		S3Region:       "us-east-1",
		S3Endpoint:     "",
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

// s3SecretEnvVars returns env vars that reference the S3 credentials Kubernetes
// Secret via secretKeyRef. This ensures credentials are never inlined in the
// Argo Workflow spec and are only resolved at pod runtime.
func s3SecretEnvVars(cfg *ODMPipelineConfig) []apiv1.EnvVar {
	secretName := config.SCALEODM_S3_SECRET_NAME

	envVars := []apiv1.EnvVar{
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &apiv1.EnvVarSource{
				SecretKeyRef: &apiv1.SecretKeySelector{
					LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_ACCESS_KEY_ID",
				},
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &apiv1.EnvVarSource{
				SecretKeyRef: &apiv1.SecretKeySelector{
					LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_SECRET_ACCESS_KEY",
				},
			},
		},
		{
			Name: "AWS_DEFAULT_REGION",
			ValueFrom: &apiv1.EnvVarSource{
				SecretKeyRef: &apiv1.SecretKeySelector{
					LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
					Key:                  "AWS_DEFAULT_REGION",
					Optional:             boolPtr(true),
				},
			},
		},
	}

	// If a custom S3 endpoint is specified (e.g., for MinIO), expose it as an env var
	if cfg.S3Endpoint != "" {
		envVars = append(envVars, apiv1.EnvVar{
			Name:  "AWS_S3_ENDPOINT",
			Value: cfg.S3Endpoint,
		})
	}

	return envVars
}

func boolPtr(b bool) *bool { return &b }

// buildODMWorkflow constructs the workflow specification
func (c *Client) buildODMWorkflow(cfg *ODMPipelineConfig) *wfv1.Workflow {
	awsEnv := s3SecretEnvVars(cfg)

	// Generate unique job ID for this workflow instance
	jobID := "{{workflow.name}}"

	// Download container - downloads from readS3Path and extracts zips
	// Uses include filters to only download image files and archives
	// Logs are written to shared workspace for later collection
	downloadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "download",
			Image:   cfg.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				// Redirect output to log file in workspace for later collection
				s3.GenerateDownloadScript(jobID, cfg.ReadS3Path) + " 2>&1 | tee /workspace/{{workflow.name}}/.download.log",
			},
			Env: awsEnv,
		},
	}

	// ODM processing container
	// Logs are written to shared workspace for later collection
	odmFlagsStr := strings.Join(cfg.ODMFlags, " ")
	odmContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "process",
			Image:   cfg.ODMImage,
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
				`, cfg.ODMProjectID, odmFlagsStr),
			},
		},
		Dependencies: []string{"download"},
	}

	// Upload container - uploads results to writeS3Path
	// Logs are written to shared workspace for later collection
	uploadContainer := wfv1.ContainerNode{
		Container: apiv1.Container{
			Name:    "upload",
			Image:   cfg.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				s3.GenerateUploadScript(cfg.WriteS3Path) + " 2>&1 | tee /workspace/{{workflow.name}}/.upload.log",
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
			Image:   cfg.RcloneImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				s3.GenerateLogUploadScript(cfg.WriteS3Path),
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
			ServiceAccountName: cfg.ServiceAccount,
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
