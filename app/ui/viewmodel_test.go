package ui

import (
	"testing"
	"time"

	"github.com/hotosm/scaleodm/app/meta"
)

func TestTaskSummaryCanceledBeforeStart(t *testing.T) {
	completedAt := time.Now()
	job := &meta.JobMetadata{
		WorkflowName: "wf-1",
		JobStatus:    meta.JobCanceled,
		CompletedAt:  &completedAt,
	}

	summary := toTaskSummary(job, time.Now())

	if summary.CompletedAt == "" {
		t.Error("CompletedAt is empty, want formatted time")
	}
	if summary.RunDuration != "" {
		t.Errorf("RunDuration = %q, want empty", summary.RunDuration)
	}
}
