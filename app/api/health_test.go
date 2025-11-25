package api

import (
	"context"
	"testing"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/stretchr/testify/require"
)

func TestHealthCheck(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	metadataStore := meta.NewStore(db)
	
	// The health check should work
	ctx := context.Background()
	err := metadataStore.HealthCheck(ctx)
	require.NoError(t, err)
}

