package reconciler

import (
	"encoding/json"
	"testing"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	assert.Nil(t, raw, "no failed pods → leave failure_details unchanged")
}

func TestBuildFailureDetails_NilWorkflow(t *testing.T) {
	raw, err := buildFailureDetails(nil)
	require.NoError(t, err)
	assert.Nil(t, raw)
}
