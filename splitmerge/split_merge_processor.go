package splitmerge

import (
	"context"
	"fmt"
	"log"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/hotosm/scaleodm/worker"
)

// type ImageSplit struct {
// 	ID        string   `json:"id"`
// 	Images    []string `json:"images"`
// 	BBox      BBox     `json:"bbox"`
// 	OutputDir string   `json:"output_dir"`
// }

// type BBox struct {
// 	MinLat float64 `json:"min_lat"`
// 	MaxLat float64 `json:"max_lat"`
// 	MinLon float64 `json:"min_lon"`
// 	MaxLon float64 `json:"max_lon"`
// }

// type WorkflowManager struct {
// 	k8sClient *kubernetes.Clientset
// 	namespace string
// }

// func NewWorkflowManager() *WorkflowManager {
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		log.Printf("Warning: Not running in cluster, using default config: %v", err)
// 		return &WorkflowManager{namespace: "default"}
// 	}

// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		log.Printf("Warning: Failed to create k8s client: %v", err)
// 		return &WorkflowManager{namespace: "default"}
// 	}

// 	return &WorkflowManager{
// 		k8sClient: clientset,
// 		namespace: "scaleodm",
// 	}
// }

// // SplitImages divides the image set into spatially coherent groups
// func (wm *WorkflowManager) SplitImages(ctx context.Context, images []string, params worker.SplitParameters) ([]ImageSplit, error) {
// 	// TODO: Implement intelligent image splitting
// 	// For now, simple split by count

// 	splits := []ImageSplit{}
// 	imagesPerSplit := params.MaxImages
// 	if imagesPerSplit <= 0 {
// 		imagesPerSplit = 100
// 	}

// 	for i := 0; i < len(images); i += imagesPerSplit {
// 		end := i + imagesPerSplit
// 		if end > len(images) {
// 			end = len(images)
// 		}

// 		split := ImageSplit{
// 			ID:        fmt.Sprintf("split-%d", i/imagesPerSplit),
// 			Images:    images[i:end],
// 			OutputDir: fmt.Sprintf("/output/split-%d", i/imagesPerSplit),
// 		}
// 		splits = append(splits, split)
// 	}

// 	return splits, nil
// }

// // CreateArgoWorkflow creates an Argo workflow for parallel ODM processing
// func (wm *WorkflowManager) CreateArgoWorkflow(ctx context.Context, jobID int64, splits []ImageSplit, payload worker.SplitMergePayload) (string, error) {
// 	workflowName := fmt.Sprintf("odm-splitmerge-%d", jobID)

// 	// Create workflow specification
// 	workflow := wm.buildWorkflowSpec(workflowName, splits, payload)

// 	// Submit to Argo (using dynamic client or Argo SDK)
// 	log.Printf("[Workflow] Creating workflow: %s with %d splits", workflowName, len(splits))

// 	// For now, log the workflow spec
// 	// TODO: Implement actual Argo workflow submission
// 	log.Printf("[Workflow] Workflow spec: %+v", workflow)

// 	return workflowName, nil
// }

// func (wm *WorkflowManager) buildWorkflowSpec(name string, splits []ImageSplit, payload worker.SplitMergePayload) map[string]interface{} {
// 	// Build Argo workflow YAML structure
// 	workflow := map[string]interface{}{
// 		"apiVersion": "argoproj.io/v1alpha1",
// 		"kind":       "Workflow",
// 		"metadata": map[string]interface{}{
// 			"generateName": name + "-",
// 			"namespace":    wm.namespace,
// 		},
// 		"spec": map[string]interface{}{
// 			"entrypoint": "splitmerge",
// 			"arguments": map[string]interface{}{
// 				"parameters": []map[string]interface{}{
// 					{"name": "project-id", "value": payload.ProjectID},
// 					{"name": "output-path", "value": payload.OutputPath},
// 				},
// 			},
// 			"templates": []map[string]interface{}{
// 				// Main workflow template
// 				{
// 					"name": "splitmerge",
// 					"dag": map[string]interface{}{
// 						"tasks": wm.buildDAGTasks(splits, payload),
// 					},
// 				},
// 				// Process split template
// 				{
// 					"name": "process-split",
// 					"inputs": map[string]interface{}{
// 						"parameters": []map[string]string{
// 							{"name": "split-id"},
// 							{"name": "images"},
// 							{"name": "output-dir"},
// 						},
// 					},
// 					"container": map[string]interface{}{
// 						"image": "opendronemap/odm:latest",
// 						"command": []string{"/bin/bash", "-c"},
// 						"args": []string{
// 							wm.buildODMCommand(payload),
// 						},
// 						"resources": map[string]interface{}{
// 							"requests": map[string]string{
// 								"memory": "8Gi",
// 								"cpu":    "4",
// 							},
// 							"limits": map[string]string{
// 								"memory": "16Gi",
// 								"cpu":    "8",
// 							},
// 						},
// 						"volumeMounts": []map[string]string{
// 							{"name": "data", "mountPath": "/datasets"},
// 							{"name": "output", "mountPath": "/output"},
// 						},
// 					},
// 				},
// 				// Merge results template
// 				{
// 					"name": "merge-results",
// 					"inputs": map[string]interface{}{
// 						"parameters": []map[string]string{
// 							{"name": "split-dirs"},
// 							{"name": "output-path"},
// 						},
// 					},
// 					"container": map[string]interface{}{
// 						"image": "opendronemap/odm:latest",
// 						"command": []string{"/bin/bash", "-c"},
// 						"args": []string{
// 							wm.buildMergeCommand(payload),
// 						},
// 						"resources": map[string]interface{}{
// 							"requests": map[string]string{
// 								"memory": "16Gi",
// 								"cpu":    "8",
// 							},
// 						},
// 						"volumeMounts": []map[string]string{
// 							{"name": "output", "mountPath": "/output"},
// 						},
// 					},
// 				},
// 			},
// 			"volumes": []map[string]interface{}{
// 				{
// 					"name": "data",
// 					"persistentVolumeClaim": map[string]string{
// 						"claimName": "scaleodm-data-pvc",
// 					},
// 				},
// 				{
// 					"name": "output",
// 					"persistentVolumeClaim": map[string]string{
// 						"claimName": "scaleodm-output-pvc",
// 					},
// 				},
// 			},
// 		},
// 	}

// 	return workflow
// }

// func (wm *WorkflowManager) buildDAGTasks(splits []ImageSplit, payload worker.SplitMergePayload) []map[string]interface{} {
// 	tasks := []map[string]interface{}{}

// 	// Create processing tasks for each split
// 	splitTaskNames := []string{}
// 	for i, split := range splits {
// 		taskName := fmt.Sprintf("process-%d", i)
// 		splitTaskNames = append(splitTaskNames, taskName)

// 		tasks = append(tasks, map[string]interface{}{
// 			"name":     taskName,
// 			"template": "process-split",
// 			"arguments": map[string]interface{}{
// 				"parameters": []map[string]string{
// 					{"name": "split-id", "value": split.ID},
// 					{"name": "images", "value": fmt.Sprintf("%v", split.Images)},
// 					{"name": "output-dir", "value": split.OutputDir},
// 				},
// 			},
// 		})
// 	}

// 	// Create merge task that depends on all processing tasks
// 	tasks = append(tasks, map[string]interface{}{
// 		"name":     "merge",
// 		"template": "merge-results",
// 		"dependencies": splitTaskNames,
// 		"arguments": map[string]interface{}{
// 			"parameters": []map[string]string{
// 				{"name": "split-dirs", "value": wm.joinSplitDirs(splits)},
// 				{"name": "output-path", "value": payload.OutputPath},
// 			},
// 		},
// 	})

// 	return tasks
// }

// func (wm *WorkflowManager) buildODMCommand(payload worker.SplitMergePayload) string {
// 	// Build ODM CLI command with options
// 	cmd := "odm_orthophoto"

// 	// Add common options
// 	if dsm, ok := payload.ODMOptions["dsm"].(bool); ok && dsm {
// 		cmd += " --dsm"
// 	}
// 	if dtm, ok := payload.ODMOptions["dtm"].(bool); ok && dtm {
// 		cmd += " --dtm"
// 	}
// 	if res, ok := payload.ODMOptions["orthophoto-resolution"].(float64); ok {
// 		cmd += fmt.Sprintf(" --orthophoto-resolution %.2f", res)
// 	}

// 	// Add input/output paths
// 	cmd += " --project-path /datasets/{{inputs.parameters.split-id}}"
// 	cmd += " --output /output/{{inputs.parameters.output-dir}}"

// 	return cmd
// }

// func (wm *WorkflowManager) buildMergeCommand(payload worker.SplitMergePayload) string {
// 	// ODM merge command for combining orthophotos
// 	return `
// 		odm_orthophoto \
// 		--merge {{inputs.parameters.split-dirs}} \
// 		--output {{inputs.parameters.output-path}}
// 	`
// }

// func (wm *WorkflowManager) joinSplitDirs(splits []ImageSplit) string {
// 	dirs := ""
// 	for i, split := range splits {
// 		if i > 0 {
// 			dirs += ","
// 		}
// 		dirs += split.OutputDir
// 	}
// 	return dirs
// }

// // MonitorWorkflow monitors an Argo workflow and updates job status
// func (wm *WorkflowManager) MonitorWorkflow(ctx context.Context, workflowName string, jobID int64) error {
// 	log.Printf("[Workflow] Monitoring workflow: %s", workflowName)

// 	// Poll workflow status
// 	ticker := time.NewTicker(30 * time.Second)
// 	defer ticker.Stop()

// 	timeout := time.After(24 * time.Hour) // Max workflow duration

// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return ctx.Err()
// 		case <-timeout:
// 			return fmt.Errorf("workflow timeout exceeded")
// 		case <-ticker.C:
// 			status, err := wm.getWorkflowStatus(ctx, workflowName)
// 			if err != nil {
// 				log.Printf("[Workflow] Error getting status: %v", err)
// 				continue
// 			}

// 			log.Printf("[Workflow] Status: %s", status)

// 			switch status {
// 			case "Succeeded":
// 				log.Printf("[Workflow] Workflow completed successfully")
// 				return nil
// 			case "Failed", "Error":
// 				return fmt.Errorf("workflow failed with status: %s", status)
// 			case "Running":
// 				// Continue monitoring
// 				continue
// 			}
// 		}
// 	}
// }

// func (wm *WorkflowManager) getWorkflowStatus(ctx context.Context, workflowName string) (string, error) {
// 	// TODO: Implement actual Argo workflow status check
// 	// This would use the Argo Workflows API

// 	// For now, return mock status
// 	return "Running", nil
// }

// // DeleteWorkflow cleans up a completed workflow
// func (wm *WorkflowManager) DeleteWorkflow(ctx context.Context, workflowName string) error {
// 	if wm.k8sClient == nil {
// 		return fmt.Errorf("k8s client not initialized")
// 	}

// 	// Delete workflow using dynamic client
// 	log.Printf("[Workflow] Deleting workflow: %s", workflowName)

// 	// TODO: Implement actual workflow deletion

// 	return nil
// }
