// Package reconciler provides a background goroutine that periodically syncs
// Argo workflow completion status into the ScaleODM metadata database.
//
// # Problem this solves
//
// The reconciler is the single background writer of job status, and GET
// /task/{uuid}/info only reads. Argo has its own TTL-based garbage collection
// for completed workflows (SCALEODM_WORKFLOW_TTL_SUCCESS_SECONDS, default
// 24 h, and SCALEODM_WORKFLOW_TTL_FAILURE_SECONDS, default 7 days). Without a
// sync before that TTL, the workflow record disappears while the DB row still
// says "running", and the job is later failed as missing even though ODM
// succeeded and the orthophoto is already in S3.
//
// # How it works
//
// Every N seconds (default 30, see SCALEODM_RECONCILER_INTERVAL_SECONDS) the
// reconciler:
//  1. Queries the DB for all non-terminal jobs.
//  2. For each, fetches the live Argo workflow phase from the Kubernetes API.
//  3. If the live phase is a forward transition from the DB status, writes the
//     updated status immediately so subsequent polls get the correct answer.
//     Pod infra failures (image pull, missing secret) are not saved, since Argo
//     can still recover. GET /task/{uuid}/info reports them live instead.
//  4. If the workflow is missing for longer than
//     SCALEODM_WORKFLOW_MISSING_GRACE_SECONDS, marks the job failed.
//
// Terminal transitions notify the job's webhook, if it has one.
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
package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
)

const missingWorkflowMessage = "Workflow missing in Argo beyond grace window; marking task as failed"

type reconciler struct {
	store    *meta.Store
	wfClient workflows.WorkflowClient
	// missingGrace is how long a workflow may be missing before its job fails.
	missingGrace time.Duration
}

type syncOutcome int

const (
	outcomeSkipped syncOutcome = iota
	outcomeSynced
	outcomeError
)

type jobMetadata struct {
	Webhook          string `json:"webhook"`
	MissingFirstSeen string `json:"workflow_missing_first_seen_at"`
}

// Start spawns a background goroutine that calls syncActiveJobs on the given
// interval. It is a no-op when wfClient is nil (e.g. SCALEODM_DOCS_ONLY=true).
// The goroutine exits when ctx is cancelled.
func Start(ctx context.Context, store *meta.Store, wfClient workflows.WorkflowClient, intervalSeconds, missingGraceSeconds int) {
	if wfClient == nil {
		return
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 30
	}
	if missingGraceSeconds <= 0 {
		missingGraceSeconds = 300
	}
	r := &reconciler{store: store, wfClient: wfClient, missingGrace: time.Duration(missingGraceSeconds) * time.Second}
	go r.run(ctx, time.Duration(intervalSeconds)*time.Second)
}

func (r *reconciler) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("reconciler: started (interval=%s, missing_grace=%s)", interval, r.missingGrace)

	for {
		select {
		case <-ctx.Done():
			log.Printf("reconciler: stopped")
			return
		case <-ticker.C:
			r.syncActiveJobs(ctx)
		}
	}
}

func (r *reconciler) syncActiveJobs(ctx context.Context) {
	jobs, err := r.store.ListActiveJobs(ctx)
	if err != nil {
		log.Printf("reconciler: failed to list active jobs: %v", err)
		return
	}

	counts := map[syncOutcome]int{}
	for _, job := range jobs {
		counts[r.syncJob(ctx, job)]++
	}
	if counts[outcomeSynced] > 0 || counts[outcomeError] > 0 {
		log.Printf("reconciler: cycle done synced=%d skipped=%d errors=%d total_checked=%d",
			counts[outcomeSynced], counts[outcomeSkipped], counts[outcomeError], len(jobs))
	}
}

func (r *reconciler) syncJob(ctx context.Context, job *meta.JobMetadata) syncOutcome {
	var metadata jobMetadata
	_ = json.Unmarshal(job.Metadata, &metadata) // absent or invalid metadata reads as empty

	wf, err := r.wfClient.GetWorkflow(ctx, job.WorkflowName)
	if k8serrors.IsNotFound(err) {
		return r.failMissingWorkflow(ctx, job, metadata)
	}
	if err != nil {
		log.Printf("reconciler: failed to get workflow %q: %v", job.WorkflowName, err)
		return outcomeError
	}

	if metadata.MissingFirstSeen != "" {
		if err := r.store.MergeJobMetadata(ctx, job.WorkflowName, map[string]interface{}{meta.MetadataWorkflowMissingFirstSeenKey: nil}); err != nil {
			log.Printf("reconciler: failed clearing missing-workflow marker for %q: %v", job.WorkflowName, err)
		}
	}

	liveStatus := meta.MapArgoPhaseToJobStatus(string(wf.Status.Phase))
	var errMsg *string
	var failureDetails json.RawMessage
	if wf.Status.Phase == wfv1.WorkflowFailed || wf.Status.Phase == wfv1.WorkflowError {
		if wf.Status.Message != "" {
			msg := wf.Status.Message
			errMsg = &msg
		}
		// Persist per-failed-node detail so diagnosis survives Argo CR
		// TTL and log archive expiry. error_message is just the top-level
		// summary; failure_details captures which pod, which exit code.
		if details, marshalErr := buildFailureDetails(wf); marshalErr != nil {
			log.Printf("reconciler: failed to marshal failure details for %q: %v", job.WorkflowName, marshalErr)
		} else {
			failureDetails = details
		}
	}

	// Only advance forward through the state machine; never regress.
	if !job.JobStatus.CanAdvanceTo(liveStatus) {
		return outcomeSkipped
	}
	return r.updateStatus(ctx, job, metadata, liveStatus, errMsg, failureDetails)
}

// failMissingWorkflow fails a job only after the grace window, which covers
// gaps where a workflow is legitimately absent, such as mid-restart.
func (r *reconciler) failMissingWorkflow(ctx context.Context, job *meta.JobMetadata, metadata jobMetadata) syncOutcome {
	now := time.Now().UTC()
	firstSeen, err := time.Parse(time.RFC3339, metadata.MissingFirstSeen)
	if err != nil {
		patch := map[string]interface{}{meta.MetadataWorkflowMissingFirstSeenKey: now.Format(time.RFC3339)}
		if err := r.store.MergeJobMetadata(ctx, job.WorkflowName, patch); err != nil {
			log.Printf("reconciler: failed to record missing workflow for %q: %v", job.WorkflowName, err)
			return outcomeError
		}
		return outcomeSkipped
	}
	if now.Sub(firstSeen) < r.missingGrace {
		return outcomeSkipped
	}

	log.Printf("reconciliation transition uuid=%s source=metadata transition=missing_workflow_to_failed reason=missing_workflow_grace_expired", job.WorkflowName)
	msg := missingWorkflowMessage
	outcome := r.updateStatus(ctx, job, metadata, meta.JobFailed, &msg, nil)
	if outcome == outcomeSynced {
		_ = r.store.MergeJobMetadata(ctx, job.WorkflowName, map[string]interface{}{meta.MetadataWorkflowMissingFirstSeenKey: nil})
	}
	return outcome
}

// updateStatus persists a transition and notifies the caller's webhook on
// terminal ones. nil failureDetails leaves the stored column unchanged.
func (r *reconciler) updateStatus(ctx context.Context, job *meta.JobMetadata, metadata jobMetadata, status meta.JobStatus, errMsg *string, failureDetails json.RawMessage) syncOutcome {
	if err := r.store.UpdateJobStatusWithFailureDetails(ctx, job.WorkflowName, status, errMsg, failureDetails); err != nil {
		log.Printf("reconciler: failed to update job %q %s->%s: %v", job.WorkflowName, job.JobStatus, status, err)
		return outcomeError
	}
	log.Printf("reconciler: synced job %q %s->%s", job.WorkflowName, job.JobStatus, status)
	if status.IsTerminal() && metadata.Webhook != "" {
		notifyWebhook(metadata.Webhook, job.WorkflowName, status.NodeODMCode())
	}
	return outcomeSynced
}

const (
	webhookTimeout    = 5 * time.Second
	webhookMaxRetries = 3
)

// notifyWebhook POSTs {uuid, statusCode} to the caller's webhook, best-effort.
// Fire-and-forget in a goroutine so a slow or unreachable receiver never stalls
// the reconcile loop.
func notifyWebhook(hookURL, uuid string, statusCode int) {
	payload, err := json.Marshal(map[string]interface{}{
		"uuid":       uuid,
		"statusCode": statusCode,
	})
	if err != nil {
		log.Printf("reconciler: webhook marshal failed for %q: %v", uuid, err)
		return
	}

	go func() {
		for attempt := 1; attempt <= webhookMaxRetries; attempt++ {
			// Detached from the loop context so a tick or shutdown can't abort
			// a delivery mid-flight.
			reqCtx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
			req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodPost, hookURL, bytes.NewReader(payload))
			if reqErr != nil {
				cancel()
				log.Printf("reconciler: webhook request build failed for %q: %v", uuid, reqErr)
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, doErr := http.DefaultClient.Do(req)
			if doErr == nil {
				_ = resp.Body.Close()
				cancel()
				if resp.StatusCode < 400 {
					log.Printf("reconciler: webhook delivered uuid=%q code=%d http=%d", uuid, statusCode, resp.StatusCode)
					return
				}
				log.Printf("reconciler: webhook attempt %d for %q returned HTTP %d", attempt, uuid, resp.StatusCode)
			} else {
				cancel()
				log.Printf("reconciler: webhook attempt %d for %q failed: %v", attempt, uuid, doErr)
			}

			if attempt < webhookMaxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		log.Printf("reconciler: webhook giving up for %q after %d attempts", uuid, webhookMaxRetries)
	}()
}

// failedNode is the slimmed-down representation of an Argo node we persist to
// the DB on terminal failure. We include only the fields useful for
// post-mortem diagnosis - exit code, message, host node, finished timestamp -
// not the full Argo node struct (which is large and embeds parent/child
// references that would balloon the JSON).
type failedNode struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName,omitempty"`
	TemplateName string `json:"templateName,omitempty"`
	Phase        string `json:"phase"`
	Message      string `json:"message,omitempty"`
	ExitCode     string `json:"exitCode,omitempty"`
	HostNodeName string `json:"hostNodeName,omitempty"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

// buildFailureDetails extracts the failed pod nodes from a workflow's status
// and marshals them as a JSON array, sorted by FinishedAt then Name so the
// oldest failure (usually the root cause on retries) appears first. Returns
// (nil, nil) when there are no failed pod nodes - the caller treats that as
// "leave failure_details unchanged".
func buildFailureDetails(wf *wfv1.Workflow) (json.RawMessage, error) {
	if wf == nil {
		return nil, nil
	}
	var nodes []failedNode
	for _, n := range wf.Status.Nodes {
		if n.Type != wfv1.NodeTypePod {
			continue
		}
		if n.Phase != wfv1.NodeFailed && n.Phase != wfv1.NodeError {
			continue
		}
		fn := failedNode{
			Name:         n.Name,
			DisplayName:  n.DisplayName,
			TemplateName: n.TemplateName,
			Phase:        string(n.Phase),
			Message:      n.Message,
			HostNodeName: n.HostNodeName,
		}
		if n.Outputs != nil && n.Outputs.ExitCode != nil {
			fn.ExitCode = *n.Outputs.ExitCode
		}
		if !n.FinishedAt.IsZero() {
			fn.FinishedAt = n.FinishedAt.UTC().Format(time.RFC3339)
		}
		nodes = append(nodes, fn)
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].FinishedAt != nodes[j].FinishedAt {
			return nodes[i].FinishedAt < nodes[j].FinishedAt
		}
		return nodes[i].Name < nodes[j].Name
	})
	return json.Marshal(nodes)
}
