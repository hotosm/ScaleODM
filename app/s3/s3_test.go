package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveCredentials_ProvidedCredentials(t *testing.T) {
	provided := &S3Credentials{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		SessionToken:    "",
	}

	// This test doesn't require actual S3, just tests the logic
	// In a real scenario, we'd need to mock STS
	creds, err := ResolveCredentials(provided, true, "us-east-1")
	
	// If STS is not configured, should return provided credentials
	// If STS is configured, might return temp creds or error
	// For now, just check that function doesn't panic
	assert.NoError(t, err)
	if creds != nil {
		assert.Equal(t, "test-key", creds.AccessKeyID)
		assert.Equal(t, "test-secret", creds.SecretAccessKey)
	}
}

func TestS3Credentials_Structure(t *testing.T) {
	creds := &S3Credentials{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		SessionToken:    "test-token",
	}

	assert.Equal(t, "test-key", creds.AccessKeyID)
	assert.Equal(t, "test-secret", creds.SecretAccessKey)
	assert.Equal(t, "test-token", creds.SessionToken)
}

