package workflows

import (
	"testing"

	"github.com/hotosm/scaleodm/app/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDefaultODMConfig(t *testing.T) {
	projectID := "test-project"
	readPath := "s3://bucket/images/"
	writePath := "s3://bucket/output/"
	flags := []string{"--fast-orthophoto", "--dsm"}

	config := NewDefaultODMConfig(projectID, readPath, writePath, flags)

	assert.Equal(t, projectID, config.ODMProjectID)
	assert.Equal(t, readPath, config.ReadS3Path)
	assert.Equal(t, writePath, config.WriteS3Path)
	assert.Equal(t, flags, config.ODMFlags)
	assert.Equal(t, "us-east-1", config.S3Region)
	assert.Equal(t, "argo-odm", config.ServiceAccount)
	assert.Equal(t, "docker.io/rclone/rclone:1", config.RcloneImage)
	assert.Nil(t, config.S3Credentials) // Should be nil initially
}

func TestBuildODMWorkflow(t *testing.T) {
	config := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	config.S3Credentials = &s3.S3Credentials{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		SessionToken:    "",
	}

	// Create a mock client (we can't actually create workflows without k8s)
	// But we can test the buildODMWorkflow logic
	client := &Client{
		namespace: "test-namespace",
	}

	wf := client.buildODMWorkflow(config)

	require.NotNil(t, wf)
	assert.Equal(t, "test-namespace", wf.Namespace)
	assert.Equal(t, "main", wf.Spec.Entrypoint)
	assert.Equal(t, "argo-odm", wf.Spec.ServiceAccountName)
	assert.NotEmpty(t, wf.Spec.Templates)
}

func TestBuildODMWorkflow_PanicsWithoutCredentials(t *testing.T) {
	config := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	// S3Credentials is nil

	client := &Client{
		namespace: "test-namespace",
	}

	// Should panic if credentials are not provided
	assert.Panics(t, func() {
		client.buildODMWorkflow(config)
	})
}

func TestBuildODMWorkflow_WithSTSCredentials(t *testing.T) {
	config := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)
	config.S3Credentials = &s3.S3Credentials{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		SessionToken:    "test-session-token",
	}

	client := &Client{
		namespace: "test-namespace",
	}

	wf := client.buildODMWorkflow(config)

	require.NotNil(t, wf)
	// Verify that session token is included in environment variables
	// This is checked in the download/upload containers
	mainTemplate := wf.Spec.Templates[0]
	require.NotNil(t, mainTemplate.ContainerSet)
	
	// Check that containers have AWS environment variables
	containers := mainTemplate.ContainerSet.Containers
	require.Greater(t, len(containers), 0)
	
	// Download container should have AWS env vars
	downloadContainer := containers[0]
	hasSessionToken := false
	for _, env := range downloadContainer.Env {
		if env.Name == "AWS_SESSION_TOKEN" {
			hasSessionToken = true
			assert.Equal(t, "test-session-token", env.Value)
		}
	}
	assert.True(t, hasSessionToken, "AWS_SESSION_TOKEN should be set")
}

