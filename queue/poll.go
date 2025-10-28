package queue

import (
	"context"
	"log"
	"time"
)

type Worker struct {
	id        string
	clusterID string
	queue     *Queue
	processor JobProcessor
}

// JobProcessor defines the interface for processing jobs
type JobProcessor interface {
	Process(ctx context.Context, job *Job) error
}

// NewWorker creates a new worker instance
func NewWorker(id, clusterID string, queue *Queue, processor JobProcessor) *Worker {
	return &Worker{
		id:        id,
		clusterID: clusterID,
		queue:     queue,
		processor: processor,
	}
}

// Start begins the worker loop
func (w *Worker) Start(ctx context.Context) error {
	log.Printf("[Worker %s] Starting for cluster %s", w.id, w.clusterID)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Worker %s] Shutting down", w.id)
			return ctx.Err()

		case <-ticker.C:
			job, err := w.queue.ClaimJob(ctx, w.clusterID, w.id)
			if err != nil {
				log.Printf("[Worker %s] Claim error: %v", w.id, err)
				continue
			}

			if job == nil {
				// No available jobs, skip
				continue
			}

			log.Printf("[Worker %s] Processing job %d (%s)", w.id, job.ID, job.JobType)
			start := time.Now()

			if err := w.processor.Process(ctx, job); err != nil {
				log.Printf("[Worker %s] Job %d failed: %v", w.id, job.ID, err)
				_ = w.queue.FailJob(ctx, job.ID, err.Error())
			} else {
				log.Printf("[Worker %s] Job %d completed in %v", w.id, job.ID, time.Since(start))
				_ = w.queue.CompleteJob(ctx, job.ID)
			}
		}
	}
}
