package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/hotosm/scaleodm/queue"
	// "github.com/hotosm/scaleodm/splitmerge"
)

type ODMJobPayload struct {
	ProjectID  string                 `json:"project_id"`
	ImageURLs  []string               `json:"image_urls"`
	NodeODMURL string                 `json:"nodeodm_url"`
	Options    map[string]interface{} `json:"options"`
}

type ODMProcessor struct{}

// Handle jobType
func (p *ODMProcessor) Process(ctx context.Context, job *queue.Job) error {
	if job.JobType == "nodeodm" {
		return p.NodeODM(ctx, job)
	}
	// TODO implement in splitmerge package
	// if job.JobType == "splitmerge" {
	// 	return splitmerge.SplitMergePipeline(ctx, job)
	// }

	errStr := fmt.Sprintf("jobType of %s does not exist", job.JobType)
	log.Fatalf("%s", errStr)
	return errors.New(errStr)
}

// Send to ScaleODM or NodeODM instance for processing
func (p *ODMProcessor) NodeODM(ctx context.Context, job *queue.Job) error {
	var payload ODMJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("invalid ODM job payload: %w", err)
	}

	// TODO: serialize payload.Options & ImageURLs as form data or JSON
	// Add here in place of nil
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, payload.NodeODMURL+"/task/new", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("ODM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ODM returned status %d", resp.StatusCode)
	}

	return nil
}

// Start Kubernetes Argo workflow for split-merge large-scale processing
func (p *ODMProcessor) SplitMerge(ctx context.Context, job *queue.Job) error {
	return nil
}
