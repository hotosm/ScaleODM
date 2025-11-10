// REST API for job queue management
// @title           ScaleODM Job Queue API
// @version         1.0.0
// @description     NodeODM-compatible API for managing distributed ODM jobs via Argo Workflows
// @contact.name    Sam Woodcock
// @contact.url     https://slack.hotosm.org
// @license.name    AGPL-3.0-only
// @license.url     https://opensource.org/licenses/agpl-v3
// @host            localhost:8080
// @BasePath        /api/v1

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	Port int `help:"Port to listen on" short:"p" default:"8080"`
}

func main() {
	// Log to stdout for Docker
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	log.Println("Starting ScaleODM...")

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Validate environment variables
	config.ValidateEnv()

	// Database connection
	database, err := db.NewDB(config.SCALEODM_DATABASE_URL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// Initialize schema
	if err := database.InitSchema(ctx); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}

	// Create metadata store
	metadataStore := meta.NewStore(database)
	log.Println("Metadata store initialized")

	// Initialize Argo Workflows client
	log.Println("Initializing Argo Workflows client...")
	wfClient, err := workflows.NewClient(config.KUBECONFIG_PATH, config.K8S_NAMESPACE)
	if err != nil {
		log.Fatalf("Failed to initialize Argo Workflows client: %v", err)
	}
	log.Println("Argo Workflows client ready")

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
