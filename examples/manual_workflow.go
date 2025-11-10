package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/hotosm/scaleodm/app/workflows"
)

func main() {
	ctx := context.Background()

	// Create client
	client, err := workflows.NewClient("/home/coder/.kube/config", "argo")
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// 1: Create and submit a workflow
	fmt.Println("Creating ODM workflow...")
	
	// Define parameters
	odmProjectID := "my-project-123"
	readS3Path := "s3://drone-tm-public/dtm-data/projects/a93e99f5-5aab-4316-b6f8-0acd56975df3/0c6e7cf3-e58f-4664-8a13-fa27dcdbb7ad/images/"
	writeS3Path := "s3://drone-tm-public/dtm-data/projects/a93e99f5-5aab-4316-b6f8-0acd56975df3/0c6e7cf3-e58f-4664-8a13-fa27dcdbb7ad/"
	odmFlags := []string{"--fast-orthophoto"}
	
	config := workflows.NewDefaultODMConfig(odmProjectID, readS3Path, writeS3Path, odmFlags)
	
	// Optionally customize other config values
	config.S3Region = "us-east-1"
	config.ODMImage = "opendronemap/odm:latest"

	wf, err := client.CreateODMWorkflow(ctx, config)
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}
	fmt.Printf("Workflow created: %s\n", wf.Name)

	// 2: Watch workflow until completion
	fmt.Println("Watching workflow...")
	completedWf, err := client.WatchWorkflow(ctx, wf.Name)
	if err != nil {
		log.Fatalf("Failed to watch workflow: %v", err)
	}
	fmt.Printf("Workflow completed with phase: %s\n", completedWf.Status.Phase)

	// 3: Get workflow status
	phase, message, err := client.GetWorkflowStatus(ctx, wf.Name)
	if err != nil {
		log.Fatalf("Failed to get workflow status: %v", err)
	}
	fmt.Printf("Workflow phase: %s, message: %s\n", phase, message)

	// 4: Get workflow logs
	fmt.Println("\nRetrieving workflow logs...")
	err = client.GetWorkflowLogs(ctx, wf.Name, os.Stdout)
	if err != nil {
		log.Fatalf("Failed to get workflow logs: %v", err)
	}

	// 5: Check if workflow is complete
	isComplete, err := client.IsWorkflowComplete(ctx, wf.Name)
	if err != nil {
		log.Fatalf("Failed to check workflow completion: %v", err)
	}
	fmt.Printf("Workflow complete: %v\n", isComplete)

	// 6: Delete workflow (optional, uncomment if needed)
	// fmt.Println("Deleting workflow...")
	// err = client.DeleteWorkflow(ctx, wf.Name)
	// if err != nil {
	// 	log.Fatalf("Failed to delete workflow: %v", err)
	// }
	// fmt.Println("Workflow deleted")
}

// // Example: Create workflow and poll for completion
// func CreateAndWaitForWorkflow() {
// 	ctx := context.Background()
	
// 	client, err := workflows.NewClient("/home/coder/.kube/config", "argo")
// 	if err != nil {
// 		log.Fatalf("Failed to create client: %v", err)
// 	}

// 	// Create workflow
// 	config := workflows.NewDefaultODMConfig("my-project")
// 	wf, err := client.CreateODMWorkflow(ctx, config)
// 	if err != nil {
// 		log.Fatalf("Failed to create workflow: %v", err)
// 	}

// 	fmt.Printf("Workflow %s created, waiting for completion...\n", wf.Name)

// 	// Poll for completion
// 	ticker := time.NewTicker(10 * time.Second)
// 	defer ticker.Stop()

// 	timeout := time.After(30 * time.Minute)

// 	for {
// 		select {
// 		case <-timeout:
// 			log.Fatal("Workflow timed out")
// 		case <-ticker.C:
// 			phase, message, err := client.GetWorkflowStatus(ctx, wf.Name)
// 			if err != nil {
// 				log.Printf("Error checking status: %v", err)
// 				continue
// 			}

// 			fmt.Printf("Status: %s - %s\n", phase, message)

// 			if phase == "Succeeded" {
// 				fmt.Println("Workflow succeeded!")
				
// 				// Get final logs
// 				fmt.Println("\nFinal logs:")
// 				err = client.GetWorkflowLogs(ctx, wf.Name, os.Stdout)
// 				if err != nil {
// 					log.Printf("Warning: failed to get logs: %v", err)
// 				}
// 				return
// 			} else if phase == "Failed" || phase == "Error" {
// 				fmt.Printf("Workflow failed with phase: %s\n", phase)
				
// 				// Get error logs
// 				fmt.Println("\nError logs:")
// 				err = client.GetWorkflowLogs(ctx, wf.Name, os.Stdout)
// 				if err != nil {
// 					log.Printf("Warning: failed to get logs: %v", err)
// 				}
// 				return
// 			}
// 		}
// 	}
// }

// // Example: Stream logs in real-time while workflow runs
// func StreamLogsWhileRunning() {
// 	ctx := context.Background()
	
// 	client, err := workflows.NewClient("/home/coder/.kube/config", "argo")
// 	if err != nil {
// 		log.Fatalf("Failed to create client: %v", err)
// 	}

// 	// Create workflow
// 	config := workflows.NewDefaultODMConfig("streaming-project")
// 	wf, err := client.CreateODMWorkflow(ctx, config)
// 	if err != nil {
// 		log.Fatalf("Failed to create workflow: %v", err)
// 	}

// 	fmt.Printf("Workflow %s created\n", wf.Name)

// 	// Start goroutine to watch workflow
// 	done := make(chan bool)
// 	go func() {
// 		completedWf, err := client.WatchWorkflow(ctx, wf.Name)
// 		if err != nil {
// 			log.Printf("Watch error: %v", err)
// 		} else {
// 			fmt.Printf("\nWorkflow completed with status: %s\n", completedWf.Status.Phase)
// 		}
// 		done <- true
// 	}()

// 	// Periodically fetch and display logs
// 	ticker := time.NewTicker(30 * time.Second)
// 	defer ticker.Stop()

// 	for {
// 		select {
// 		case <-done:
// 			// Fetch final logs
// 			fmt.Println("\n=== FINAL LOGS ===")
// 			err = client.GetWorkflowLogs(ctx, wf.Name, os.Stdout)
// 			if err != nil {
// 				log.Printf("Failed to get final logs: %v", err)
// 			}
// 			return
// 		case <-ticker.C:
// 			fmt.Println("\n=== CURRENT LOGS ===")
// 			err = client.GetWorkflowLogs(ctx, wf.Name, os.Stdout)
// 			if err != nil {
// 				log.Printf("Failed to get logs: %v", err)
// 			}
// 		}
// 	}
// }
