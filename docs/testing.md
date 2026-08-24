# Testing

Everything runs against the real stack (DB, S3, Kubernetes) via Docker Compose.
No mocks.

```bash
# start services, then run the whole suite
docker compose -f compose.yaml -f compose.test.yaml up -d db s3
docker compose -f compose.yaml -f compose.test.yaml run --rm api

# one package
docker compose -f compose.yaml -f compose.test.yaml run --rm api go test -v ./app/meta/...

# E2E, behind a build tag
docker compose -f compose.yaml -f compose.test.yaml run --rm api go test -v -tags=e2e .
```

## Layout

- **Integration** (`app/*/*_test.go`) against a real database, S3 and Kubernetes.
- **E2E** (`main_test.go`) full system scenarios.

Helpers per package:

- `testDB(t)` database connection with cleanup (`app/api`, `app/meta`, `app/db`)
- `testWorkflowClient(t)` real Argo Workflows client (`app/api`)
- `testutil.TestDBURL()` reads `SCALEODM_DATABASE_URL`

```go
func TestFeature(t *testing.T) {
    db, cleanup := testDB(t)
    defer cleanup()

    store := meta.NewStore(db)
    // ...
}
```

## Test data

- **Database:** `SCALEODM_DATABASE_URL`, same var as prod. Tests clean up after
  themselves.
- **S3:** `AWS_S3_ENDPOINT`, the RustFS instance in the compose stack.
- **Kubernetes:** needs a real cluster (Talos) with Argo Workflows installed.

## When something breaks

```bash
docker compose -f compose.yaml -f compose.test.yaml ps           # services up?
psql postgres://odm:odm@localhost:31101/scaleodm?sslmode=disable # DB reachable?
kubectl get pods -n argo                                         # Argo alive?
```
