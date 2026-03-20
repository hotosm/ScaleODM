package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetS3Client_DoesNotPanic(t *testing.T) {
	// GetS3Client uses config vars which default to empty/s3.amazonaws.com
	// Just verify it doesn't panic during normal construction
	assert.NotPanics(t, func() {
		_ = GetS3Client()
	})
}
