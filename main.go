// REST API for job queue management.
//
// The OpenAPI document is generated at runtime by Huma in app/api; the
// version surfaced there is injected via -ldflags into
// github.com/hotosm/scaleodm/app/version.Version (see .goreleaser.yaml).
// No swag-style annotations are consumed here.

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2/humacli"

	"github.com/hotosm/scaleodm/app/api"
	"github.com/hotosm/scaleodm/app/config"
	"github.com/hotosm/scaleodm/app/db"
	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/reconciler"
	"github.com/hotosm/scaleodm/app/workflows"
)

// Huma CLI Options
type Options struct {
	Port int `help:"Port to listen on" short:"p" default:"31100"`
}

func main() {
	// Log to stdout for Docker (unbuffered)
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	startTime := time.Now()
	log.Println("Starting ScaleODM...")

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Validate environment variables
	log.Println("Validating environment variables...")
	config.ValidateEnv()
	log.Printf("Environment validation complete (took %v)", time.Since(startTime))

	// Database connection
	log.Println("Connecting to database...")
	dbStart := time.Now()
	database, err := db.NewDB(config.SCALEODM_DATABASE_URL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()
	log.Printf("Database connection established (took %v)", time.Since(dbStart))

	log.Println("Initializing database schema...")
	schemaStart := time.Now()
	if err := database.InitSchema(ctx); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}
	log.Printf("Schema initialization complete (took %v)", time.Since(schemaStart))

	log.Println("Creating metadata store...")
	metaStart := time.Now()
	metadataStore := meta.NewStore(database)
	log.Printf("Metadata store initialized (took %v)", time.Since(metaStart))

	docsOnly := strings.EqualFold(os.Getenv("SCALEODM_DOCS_ONLY"), "true")
	var wfClient workflows.WorkflowClient
	if docsOnly {
		log.Println("SCALEODM_DOCS_ONLY=true, skipping Argo Workflows client initialization")
	} else {
		log.Println("Initializing Argo Workflows client...")
		k8sStart := time.Now()
		wfClient, err = workflows.NewClient(config.KUBECONFIG_PATH, config.K8S_NAMESPACE)
		if err != nil {
			log.Fatalf("Failed to initialize Argo Workflows client: %v", err)
		}
		log.Printf("Argo Workflows client initialized (availability checked by readiness probe, took %v)", time.Since(k8sStart))
	}

	// Start background reconciler. Does not run when wfClient is nil (docs-only mode).
	reconciler.Start(ctx, metadataStore, wfClient, config.SCALEODM_RECONCILER_INTERVAL_SECONDS, config.SCALEODM_WORKFLOW_MISSING_GRACE_SECONDS)

	// === HUMA CLI ===
	// Channel to communicate the *http.Server back from the OnStart hook so
	// we can shut it down gracefully when we receive a signal.
	serverCh := make(chan *http.Server, 1)

	cli := humacli.New(func(hooks humacli.Hooks, options *Options) {
		_, handler := api.NewAPI(metadataStore, wfClient)
		srv := newHTTPServer(options.Port, handler)

		hooks.OnStart(func() {
			log.Printf("API server starting on :%d", options.Port)
			log.Printf("   Docs: http://localhost:%d/", options.Port)
			log.Printf("   OpenAPI: http://localhost:%d/openapi.json", options.Port)

			serverCh <- srv

			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP server failed: %v", err)
			}
		})

		// Graceful shutdown
		hooks.OnStop(func() {
			log.Println("Shutting down API server...")
		})
	})

	// Start CLI in background
	go cli.Run()

	// Wait for shutdown signal
	<-sigCh
	log.Println("Received shutdown signal...")

	// Cancel context
	cancel()

	// Gracefully shut down the HTTP server with a deadline
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	select {
	case srv := <-serverCh:
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
	default:
		// Server never started
	}

	log.Println("Shutdown complete")
}

func newHTTPServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: secondsOr(config.SCALEODM_SERVER_READ_HEADER_TIMEOUT_SECONDS, 10*time.Second),
		ReadTimeout:       secondsOr(config.SCALEODM_SERVER_READ_TIMEOUT_SECONDS, 30*time.Second),
		WriteTimeout:      secondsOr(config.SCALEODM_SERVER_WRITE_TIMEOUT_SECONDS, 300*time.Second),
		IdleTimeout:       secondsOr(config.SCALEODM_SERVER_IDLE_TIMEOUT_SECONDS, 120*time.Second),
	}
}

func secondsOr(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
