FROM golang:1.25 AS base


# Build statically compiled binary
FROM base AS build
WORKDIR /code
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /code/scaleodm


# Run the tests in the container
FROM build AS run-test-stage
RUN go test -v ./...


# Add a non-root user to passwd file
FROM base AS useradd
RUN groupadd -g 1000 nonroot
RUN useradd -u 1000 nonroot -g 1000


# Add ca-certs required for calling remote signed APIs
FROM base AS certs
RUN apt-get update --quiet \
    && DEBIAN_FRONTEND=noninteractive \
    apt-get install -y --quiet --no-install-recommends \
        "ca-certificates" \
    && rm -rf /var/lib/apt/lists/* \
    && update-ca-certificates


# Deploy the application binary into sratch image
FROM scratch AS release
WORKDIR /code
COPY --from=build /code/scaleodm /code/scaleodm
COPY --from=useradd /etc/group /etc/group
COPY --from=useradd /etc/passwd /etc/passwd
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
USER nonroot:nonroot
ENTRYPOINT ["/code/scaleodm"]
