package workflows

import (
	"testing"

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
}

func TestBuildODMWorkflow(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{
		namespace: "test-namespace",
	}

	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	assert.Equal(t, "test-namespace", wf.Namespace)
	assert.Equal(t, "main", wf.Spec.Entrypoint)
	assert.Equal(t, "argo-odm", wf.Spec.ServiceAccountName)
	assert.NotEmpty(t, wf.Spec.Templates)
}

func TestBuildODMWorkflow_UsesSecretKeyRef(t *testing.T) {
	cfg := NewDefaultODMConfig(
		"test-project",
		"s3://bucket/images/",
		"s3://bucket/output/",
		[]string{"--fast-orthophoto"},
	)

	client := &Client{
		namespace: "test-namespace",
	}

	wf := client.buildODMWorkflow(cfg)

	require.NotNil(t, wf)
	mainTemplate := wf.Spec.Templates[0]
	require.NotNil(t, mainTemplate.ContainerSet)

	// Check that containers use secretKeyRef for AWS credentials
	containers := mainTemplate.ContainerSet.Containers
	require.Greater(t, len(containers), 0)

	downloadContainer := containers[0]
	hasAccessKey := false
	hasSecretKey := false
	for _, env := range downloadContainer.Env {
		if env.Name == "AWS_ACCESS_KEY_ID" {
			hasAccessKey = true
			require.NotNil(t, env.ValueFrom, "AWS_ACCESS_KEY_ID should use ValueFrom")
			require.NotNil(t, env.ValueFrom.SecretKeyRef, "AWS_ACCESS_KEY_ID should use secretKeyRef")
			assert.Equal(t, "AWS_ACCESS_KEY_ID", env.ValueFrom.SecretKeyRef.Key)
		}
		if env.Name == "AWS_SECRET_ACCESS_KEY" {
			hasSecretKey = true
			require.NotNil(t, env.ValueFrom, "AWS_SECRET_ACCESS_KEY should use ValueFrom")
			require.NotNil(t, env.ValueFrom.SecretKeyRef, "AWS_SECRET_ACCESS_KEY should use secretKeyRef")
			assert.Equal(t, "AWS_SECRET_ACCESS_KEY", env.ValueFrom.SecretKeyRef.Key)
		}
	}
	assert.True(t, hasAccessKey, "AWS_ACCESS_KEY_ID should be present")
	assert.True(t, hasSecretKey, "AWS_SECRET_ACCESS_KEY should be present")
}
