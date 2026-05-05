// Package reconciler provides a background goroutine that periodically syncs
// Argo workflow completion status into the ScaleODM metadata database.
//
// # Problem this solves
//
// ScaleODM's DB status is only updated on-demand when GET /task/{uuid}/info is
// polled. Argo has its own TTL-based garbage collection for completed workflows
// (SCALEODM_WORKFLOW_TTL_SUCCESS_SECONDS, default 24 h, and
// SCALEODM_WORKFLOW_TTL_FAILURE_SECONDS, default 7 days). If the TTL expires
// before the caller polls, Argo deletes the workflow record while the DB row
// still says "running". The existing missing-workflow grace-period logic then
// marks the job as "failed" - even though ODM actually succeeded and the
// orthophoto is already in S3.
//
// # How it works
//
// Every N seconds (default 30, see SCALEODM_RECONCILER_INTERVAL_SECONDS) the
// reconciler:
//  1. Queries the DB for non-terminal jobs created within the last 7 days.
//  2. For each, fetches the live Argo workflow phase from the Kubernetes API.
//  3. If the live phase is a forward transition from the DB status, writes the
//     updated status immediately so subsequent polls get the correct answer.
//
// # Interval choice (30 s)
//
// 30 seconds is deliberately short. The default success TTL is 24 hours, giving
// ~2 880 reconcile cycles before Argo GC. Even with an aggressive 5-minute TTL
// there are 10 chances to catch the transition before the workflow is deleted.
// The per-cycle cost is negligible: one DB query plus one Kubernetes API call
// per active (non-terminal) job. When the system is idle the DB query returns
// zero rows and no Argo calls are made at all - the goroutine simply sleeps
// until the next tick.
//
// # 7-day lookback window
//
// Non-terminal jobs older than 7 days are excluded. 7 days matches
// SCALEODM_WORKFLOW_TTL_FAILURE_SECONDS (the longer of the two default Argo
// TTLs), so any job that old is guaranteed to have its workflow GC'd already.
// Filtering at the database level (rather than fetching all rows and discarding
// in Go) keeps the query cost proportional to the number of genuinely active
// jobs rather than to the all-time history of the table.
package reconciler

import (
	"context"
	"log"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
)

// activeLookback is how far back we scan for non-terminal jobs. It matches
// SCALEODM_WORKFLOW_TTL_FAILURE_SECONDS (7 days) so any older job is guaranteed
// to have its Argo workflow already GC'd and needs no reconciliation.
const activeLookback = 7 * 24 * time.Hour

func isNotFound(err error) bool {
	return k8serrors.IsNotFound(err)
}

// Start spawns a background goroutine that calls syncActiveJobs on the given
// interval. It is a no-op when wfClient is nil (e.g. SCALEODM_DOCS_ONLY=true).
// The goroutine exits when ctx is cancelled.
func Start(ctx context.Context, store *meta.Store, wfClient workflows.WorkflowClient, intervalSeconds int) {
	if wfClient == nil {
		return
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 30
	}
	go run(ctx, store, wfClient, time.Duration(intervalSeconds)*time.Second)
}

func run(ctx context.Context, store *meta.Store, wfClient workflows.WorkflowClient, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("reconciler: started (interval=%s, lookback=%s)", interval, activeLookback)

	for {
		select {
		case <-ctx.Done():
			log.Printf("reconciler: stopped")
			return
		case <-ticker.C:
			syncActiveJobs(ctx, store, wfClient)
		}
	}
}

func syncActiveJobs(ctx context.Context, store *meta.Store, wfClient workflows.WorkflowClient) {
	// Only fetch jobs that are non-terminal and within the lookback window.
	// Filtering at the DB level keeps the scan O(active jobs) not O(all history).
	since := time.Now().Add(-activeLookback)
	jobs, err := store.ListActiveJobs(ctx, since)
	if err != nil {
		log.Printf("reconciler: failed to list active jobs: %v", err)
		return
	}

	if len(jobs) == 0 {
		return
	}

	synced, skipped, errors := 0, 0, 0
	for _, job := range jobs {
		wf, wfErr := wfClient.GetWorkflow(ctx, job.WorkflowName)
		if wfErr != nil {
			if isNotFound(wfErr) {
				// Workflow deleted by Argo GC; the missing-workflow grace-period
				// logic inside GET /task/{uuid}/info handles this case.
				skipped++
				continue
			}
			log.Printf("reconciler: failed to get workflow %q: %v", job.WorkflowName, wfErr)
			errors++
			continue
		}

		liveStatus := meta.MapArgoPhaseToJobStatus(string(wf.Status.Phase))
		if liveStatus == job.JobStatus {
			skipped++
			continue
		}

		// Only advance forward through the state machine; never regress.
		if !isForwardTransition(job.JobStatus, liveStatus) {
			skipped++
			continue
		}

		var errMsg *string
		if wf.Status.Phase == wfv1.WorkflowFailed || wf.Status.Phase == wfv1.WorkflowError {
			if wf.Status.Message != "" {
				msg := wf.Status.Message
				errMsg = &msg
			}
		}
		if updateErr := store.UpdateJobStatus(ctx, job.WorkflowName, liveStatus, errMsg); updateErr != nil {
			log.Printf("reconciler: failed to update job %q %s→%s: %v", job.WorkflowName, job.JobStatus, liveStatus, updateErr)
			errors++
		} else {
			log.Printf("reconciler: synced job %q %s→%s", job.WorkflowName, job.JobStatus, liveStatus)
			synced++
		}
	}

	if synced > 0 || errors > 0 {
		log.Printf("reconciler: cycle done synced=%d skipped=%d errors=%d total_checked=%d", synced, skipped, errors, len(jobs))
	}
}

// isForwardTransition returns true when moving from src to dst is a valid
// forward progression (prevents accidental regressions like completed→running).
func isForwardTransition(src, dst string) bool {
	order := map[string]int{
		"queued":    0,
		"claimed":   1,
		"running":   2,
		"completed": 3,
		"failed":    3,
		"canceled":  3,
	}
	s, sOk := order[src]
	d, dOk := order[dst]
	if !sOk || !dOk {
		return false
	}
	return d > s
}
