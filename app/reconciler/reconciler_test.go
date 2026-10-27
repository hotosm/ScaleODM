package reconciler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/workflows"
	"github.com/hotosm/scaleodm/testutil"
)

func TestBuildFailureDetails_ExtractsFailedPodNodes(t *testing.T) {
	finishedFirst := metav1.NewTime(time.Date(2026, 5, 20, 11, 29, 9, 0, time.UTC))
	finishedSecond := metav1.NewTime(time.Date(2026, 5, 20, 11, 32, 11, 0, time.UTC))
	exitCode := "1"
	wf := &wfv1.Workflow{
		Status: wfv1.WorkflowStatus{
			Nodes: map[string]wfv1.NodeStatus{
				// Failed pod we want to capture.
				"pod-a": {
					Name:         "odm-pipeline-s589l-main-2602622713",
					DisplayName:  "odm-pipeline-s589l(0)",
					TemplateName: "main",
					Type:         wfv1.NodeTypePod,
					Phase:        wfv1.NodeFailed,
					Message:      "process: Error (exit code 1)",
					HostNodeName: "ip-10-0-60-37.ec2.internal",
					FinishedAt:   finishedFirst,
					Outputs:      &wfv1.Outputs{ExitCode: &exitCode},
				},
				// Failed retry pod.
				"pod-b": {
					Name:         "odm-pipeline-s589l-main-3206764092",
					DisplayName:  "odm-pipeline-s589l(1)",
					TemplateName: "main",
					Type:         wfv1.NodeTypePod,
					Phase:        wfv1.NodeError,
					Message:      "download: Error (exit code 1)",
					FinishedAt:   finishedSecond,
				},
				// Succeeded pod - must NOT appear in failures.
				"pod-c": {
					Type:  wfv1.NodeTypePod,
					Phase: wfv1.NodeSucceeded,
				},
				// Non-pod node (e.g. the workflow's StepGroup) - filtered out
				// because its message duplicates the workflow-level summary.
				"group": {
					Type:  wfv1.NodeTypeStepGroup,
					Phase: wfv1.NodeFailed,
				},
			},
		},
	}

	raw, err := buildFailureDetails(wf)
	require.NoError(t, err)
	require.NotNil(t, raw)

	var got []failedNode
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 2, "only failed pod nodes should be captured")

	// Sorted by FinishedAt ascending: the original failure first, retry second.
	assert.Equal(t, "odm-pipeline-s589l-main-2602622713", got[0].Name)
	assert.Equal(t, "Failed", got[0].Phase)
	assert.Equal(t, "1", got[0].ExitCode)
	assert.Equal(t, "ip-10-0-60-37.ec2.internal", got[0].HostNodeName)
	assert.Equal(t, "2026-05-20T11:29:09Z", got[0].FinishedAt)

	assert.Equal(t, "odm-pipeline-s589l-main-3206764092", got[1].Name)
	assert.Equal(t, "Error", got[1].Phase)
}

func TestBuildFailureDetails_NilWhenNothingFailed(t *testing.T) {
	wf := &wfv1.Workflow{
		Status: wfv1.WorkflowStatus{
			Nodes: map[string]wfv1.NodeStatus{
				"pod-a": {Type: wfv1.NodeTypePod, Phase: wfv1.NodeSucceeded},
			},
		},
	}
	raw, err := buildFailureDetails(wf)
	require.NoError(t, err)
	assert.Nil(t, raw, "no failed pods -> leave failure_details unchanged")
}

func TestBuildFailureDetails_NilWorkflow(t *testing.T) {
	raw, err := buildFailureDetails(nil)
	require.NoError(t, err)
	assert.Nil(t, raw)
}

// fakeWorkflowClient serves workflows from a map, and absent names are NotFound.
type fakeWorkflowClient struct {
	workflows.WorkflowClient
	workflows map[string]*wfv1.Workflow
}

func (c *fakeWorkflowClient) GetWorkflow(ctx context.Context, name string) (*wfv1.Workflow, error) {
	if wf, ok := c.workflows[name]; ok {
		return wf, nil
	}
	return nil, k8serrors.NewNotFound(wfv1.Resource("workflows"), name)
}

func phaseWorkflow(phase wfv1.WorkflowPhase) *wfv1.Workflow {
	return &wfv1.Workflow{Status: wfv1.WorkflowStatus{Phase: phase}}
}

func newTestJob(t *testing.T, store *meta.Store, name string, status meta.JobStatus, metadata map[string]any) *meta.JobMetadata {
	t.Helper()
	ctx := context.Background()
	_, err := store.CreateJob(ctx, name, "project", "s3://bucket/images/", "s3://bucket/output/", []string{"--fast-orthophoto"}, "us-east-1", metadata)
	require.NoError(t, err)
	if status != meta.JobQueued {
		require.NoError(t, store.UpdateJobStatus(ctx, name, status, nil))
	}
	job, err := store.GetJob(ctx, name)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func TestSyncJob_AdvancesForwardButNeverRegresses(t *testing.T) {
	database, cleanup := testutil.TestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := meta.NewStore(database)
	wfClient := &fakeWorkflowClient{workflows: map[string]*wfv1.Workflow{"wf": phaseWorkflow(wfv1.WorkflowRunning)}}
	r := &reconciler{store: store, wfClient: wfClient, missingGrace: time.Minute}

	assert.Equal(t, outcomeSynced, r.syncJob(ctx, newTestJob(t, store, "wf", "queued", nil)))

	// An unknown phase maps to queued, which must not regress a running job.
	wfClient.workflows["wf"] = phaseWorkflow("")
	job, err := store.GetJob(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, meta.JobRunning, job.JobStatus)
	assert.Equal(t, outcomeSkipped, r.syncJob(ctx, job))

	job, err = store.GetJob(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, meta.JobRunning, job.JobStatus)
}

// Argo can recover from image pull backoff, so the reconciler must not finalize it.
func TestSyncJob_InfraFailureIsNotPersisted(t *testing.T) {
	database, cleanup := testutil.TestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := meta.NewStore(database)
	wf := phaseWorkflow(wfv1.WorkflowRunning)
	wf.Status.Nodes = map[string]wfv1.NodeStatus{"n1": {Message: "Error: ImagePullBackOff"}}
	r := &reconciler{store: store, wfClient: &fakeWorkflowClient{workflows: map[string]*wfv1.Workflow{"wf": wf}}, missingGrace: time.Minute}

	assert.Equal(t, outcomeSkipped, r.syncJob(ctx, newTestJob(t, store, "wf", meta.JobRunning, nil)))

	job, err := store.GetJob(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, meta.JobRunning, job.JobStatus)
	assert.Nil(t, job.ErrorMessage)
}

func TestSyncJob_MissingWorkflowFailsOnlyAfterGraceAndNotifiesWebhook(t *testing.T) {
	database, cleanup := testutil.TestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := meta.NewStore(database)

	hook := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(req.Body).Decode(&payload)
		hook <- payload
	}))
	defer server.Close()

	r := &reconciler{store: store, wfClient: &fakeWorkflowClient{}, missingGrace: time.Minute}
	job := newTestJob(t, store, "wf", "running", map[string]any{meta.MetadataWebhookKey: server.URL})

	// First sighting only records the marker.
	assert.Equal(t, outcomeSkipped, r.syncJob(ctx, job))
	job, err := store.GetJob(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, meta.JobRunning, job.JobStatus)
	assert.Contains(t, string(job.Metadata), meta.MetadataWorkflowMissingFirstSeenKey)

	// Still within the grace window.
	assert.Equal(t, outcomeSkipped, r.syncJob(ctx, job))

	expired := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	require.NoError(t, store.MergeJobMetadata(ctx, "wf", map[string]any{meta.MetadataWorkflowMissingFirstSeenKey: expired}))
	job, err = store.GetJob(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, outcomeSynced, r.syncJob(ctx, job))

	job, err = store.GetJob(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, meta.JobFailed, job.JobStatus)
	require.NotNil(t, job.ErrorMessage)
	assert.Equal(t, missingWorkflowMessage, *job.ErrorMessage)

	select {
	case payload := <-hook:
		assert.Equal(t, "wf", payload["uuid"])
		assert.EqualValues(t, meta.StatusCodeFailed, payload["statusCode"])
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not notified")
	}
}

func TestSyncJob_ReappearingWorkflowClearsMissingMarker(t *testing.T) {
	database, cleanup := testutil.TestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := meta.NewStore(database)
	r := &reconciler{store: store, wfClient: &fakeWorkflowClient{workflows: map[string]*wfv1.Workflow{"wf": phaseWorkflow(wfv1.WorkflowRunning)}}, missingGrace: time.Minute}
	job := newTestJob(t, store, "wf", "running", map[string]any{meta.MetadataWorkflowMissingFirstSeenKey: time.Now().UTC().Format(time.RFC3339)})

	assert.Equal(t, outcomeSkipped, r.syncJob(ctx, job))

	job, err := store.GetJob(ctx, "wf")
	require.NoError(t, err)
	var metadata jobMetadata
	require.NoError(t, json.Unmarshal(job.Metadata, &metadata))
	assert.Empty(t, metadata.MissingFirstSeen)
}
