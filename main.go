// REST API for job queue management
// @title           ScaleODM Job Queue API
// @version         1.0.0
// @description     API for managing distributed job queues across clusters.
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
	"github.com/hotosm/scaleodm/api"
	"github.com/hotosm/scaleodm/db"
	"github.com/hotosm/scaleodm/queue"
	"github.com/hotosm/scaleodm/worker"
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

	// Database conn
	connString := getDatabaseURL()
	database, err := db.NewDB(connString)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	if err := database.InitSchema(ctx); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}
	if err := database.InitLocalClusterRecord(ctx); err != nil {
		log.Fatalf("Failed to add local cluster record: %v", err)
	}

	jobQueue := queue.NewQueue(database)

	// Start job workers in background
	numWorkers := 3
	log.Printf("Starting %d workers...", numWorkers)
	go startDbWorkers(ctx, jobQueue, numWorkers)

	// Start cluster health check worker in background
	healthChecker := queue.NewClusterHealthChecker(jobQueue)
	go healthChecker.Start(ctx, 30*time.Second)

	// Enqueue test jobs
	if getEnv("ENQUEUE_TEST_JOBS", "false") == "true" {
		log.Println("Enqueuing test jobs...")
		if err := enqueueTestJobs(ctx, jobQueue); err != nil {
			log.Printf("Failed to enqueue test jobs: %v", err)
		}
	}

	// === HUMA CLI ===
	cli := humacli.New(func(hooks humacli.Hooks, options *Options) {
		// Create API (register routes and get the HTTP handler)
		apiObj, handler := api.NewAPI(jobQueue)

		// Start HTTP server via Huma
		hooks.OnStart(func() {
			log.Printf("API server starting on :%d", options.Port)
			log.Printf("   Docs: http://localhost:%d", options.Port)
			log.Printf("   OpenAPI: http://localhost:%d/openapi.json.yaml", options.Port)

			if err := http.ListenAndServe(fmt.Sprintf(":%d", options.Port), handler); err != nil {
				log.Fatalf("HTTP server failed: %v", err)
			}

			// Make sure apiObj (if any) can be used later; currently not required.
			_ = apiObj
		})

		// Graceful shutdown
		hooks.OnStop(func() {
			log.Println("Shutting down API server...")
		})
	})
	cli.Run()

	// Wait for shutdown signal
	<-sigCh
	log.Println("Received shutdown signal...")

	// Cancel workers
	cancel()

	// Give workers time to finish
	time.Sleep(2 * time.Second)

	log.Println("Shutdown complete")
}

func getDatabaseURL() string {
	if url := os.Getenv("SCALEODM_DATABASE_URL"); url != "" {
		return url
	}
	log.Fatalf("SCALEODM_DATABASE_URL is required")
	return ""
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func enqueueTestJobs(ctx context.Context, q *queue.Queue) error {
	testJobs := []worker.ODMJobPayload{
		{
			ProjectID:  "project-1",
			ImageURLs:  []string{"s3://bucket/img1.jpg", "s3://bucket/img2.jpg"},
			NodeODMURL: "http://nodeodm:3000",
			Options:    map[string]interface{}{"quality": "high"},
		},
		{
			ProjectID:  "project-2",
			ImageURLs:  []string{"s3://bucket/img3.jpg", "s3://bucket/img4.jpg"},
			NodeODMURL: "http://nodeodm:3001",
			Options:    map[string]interface{}{"fast-orthophoto": true},
		},
	}

	for i, payload := range testJobs {
		job, err := q.Enqueue(ctx, "http://localhost:8080", "nodeodm", payload, i)
		if err != nil {
			return fmt.Errorf("job %d: %w", i, err)
		}
		log.Printf("Test job %d enqueued: %s (ID: %d)", i+1, payload.ProjectID, job.ID)
	}
	return nil
}

func startDbWorkers(ctx context.Context, q *queue.Queue, count int) {
	processor := &worker.ODMProcessor{}
	clusterID := "http://localhost:8080"

	for i := 0; i < count; i++ {
		id := fmt.Sprintf("worker-%d", i+1)
		w := queue.NewWorker(id, clusterID, q, processor)

		go func(w *queue.Worker, id string) {
			if err := w.Start(ctx); err != nil && err != context.Canceled {
				log.Printf("Worker %s error: %v", id, err)
			} else {
				log.Printf("Worker %s stopped", id)
			}
		}(w, id)
	}
}
