// REST API for job queue management
// @title           ScaleODM Job Queue API
// @version         0.1.0
// @description     NodeODM-compatible API for managing distributed ODM jobs via Argo Workflows
// @contact.name    Sam Woodcock
// @contact.url     https://slack.hotosm.org
// @license.name    AGPL-3.0-only
// @license.url     https://opensource.org/licenses/agpl-v3
// @host            localhost:31100
// @BasePath        /api/v1

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

	// Initialize schema
	log.Println("Initializing database schema...")
	schemaStart := time.Now()
	if err := database.InitSchema(ctx); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}
	log.Printf("Schema initialization complete (took %v)", time.Since(schemaStart))

	// Initialize local cluster record
	log.Println("Initializing local cluster record...")
	clusterStart := time.Now()
	if err := database.InitLocalClusterRecord(ctx, config.SCALEODM_CLUSTER_URL); err != nil {
		log.Fatalf("Failed to initialize local cluster record: %v", err)
	}
	log.Printf("Local cluster registered: %s (took %v)", config.SCALEODM_CLUSTER_URL, time.Since(clusterStart))

	// Create metadata store
	log.Println("Creating metadata store...")
	metaStart := time.Now()
	metadataStore := meta.NewStore(database)
	log.Printf("Metadata store initialized (took %v)", time.Since(metaStart))

	docsOnly := strings.EqualFold(os.Getenv("SCALEODM_DOCS_ONLY"), "true")
	var wfClient workflows.WorkflowClient
	if docsOnly {
		log.Println("SCALEODM_DOCS_ONLY=true, skipping Argo Workflows client initialization")
	} else {
		// Initialize Argo Workflows client
		log.Println("Initializing Argo Workflows client...")
		k8sStart := time.Now()
		wfClient, err = workflows.NewClient(config.KUBECONFIG_PATH, config.K8S_NAMESPACE)
		if err != nil {
			log.Fatalf("Failed to initialize Argo Workflows client: %v", err)
		}
		log.Printf("Argo Workflows client ready (took %v)", time.Since(k8sStart))
	}

	// === HUMA CLI ===
	cli := humacli.New(func(hooks humacli.Hooks, options *Options) {
		// Create API (register routes and get the HTTP handler)
		apiObj, handler := api.NewAPI(metadataStore, wfClient)

		// Start HTTP server
		hooks.OnStart(func() {
			log.Printf("API server starting on :%d", options.Port)
			log.Printf("   Docs: http://localhost:%d/docs", options.Port)
			log.Printf("   OpenAPI: http://localhost:%d/openapi.json", options.Port)

			if err := http.ListenAndServe(fmt.Sprintf(":%d", options.Port), handler); err != nil {
				log.Fatalf("HTTP server failed: %v", err)
			}

			// Ensure apiObj is used
			_ = apiObj
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

	// Give time to finish
	time.Sleep(2 * time.Second)

	log.Println("Shutdown complete")
}
