# curl Example

Uses `curl` to test the ScaleODM NodeODM-compatible API.

- Run with `just example-curl`.
- The recipe starts the local test dependencies, seeds example imagery into S3,
  creates a task with `POST /task/new`, polls `GET /task/{uuid}/info`, and then
  calls the list, info, output, and download endpoints.
