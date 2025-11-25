# Testing Guide

All tests require the full stack (DB, S3, K8s) running via Docker Compose. Tests use real services - no mocks.

## Running Tests

```bash
# Start services and run all tests
docker compose up -d db s3
docker compose run --rm api

# Run specific test package
docker compose run --rm api go test -v ./app/meta/...

# Run E2E tests (requires build tag)
docker compose run --rm api go test -v -tags=e2e .
```

## Test Structure

- **Integration Tests** (`app/*/*_test.go`) - Test components with real database, S3, and Kubernetes
- **E2E Tests** (`main_test.go`) - Test full system end-to-end scenarios

## Test Helpers

Each test package provides helpers:

- `testDB(t)` - Creates database connection with cleanup (in `app/api`, `app/meta`, `app/db`)
- `testWorkflowClient(t)` - Creates real Argo Workflows client (in `app/api`)
- `testutil.TestDBURL()` - Returns database URL from `SCALEODM_DATABASE_URL`

**Example**:
```go
func TestFeature(t *testing.T) {
    db, cleanup := testDB(t)
    defer cleanup()
    
    store := meta.NewStore(db)
    // ... test code
}
```

## Test Data

- **Database**: Uses `SCALEODM_DATABASE_URL` (same as production). Tests automatically clean up after themselves.
- **S3**: Uses `SCALEODM_S3_ENDPOINT` (MinIO in compose stack).
- **Kubernetes**: Requires real cluster (Talos) with Argo Workflows installed.

## Troubleshooting

```bash
# Check services
docker compose ps

# Check database connection
psql postgres://odm:odm@localhost:31101/scaleodm?sslmode=disable

# Check Kubernetes
kubectl get pods -n argo
```
