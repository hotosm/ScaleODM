package workflows

import (
	"context"
	"fmt"
	"os"

	"github.com/hotosm/scaleodm/app/s3"
)

// TestStandardWorkflow creates and monitors a standard ODM workflow
// This is used for testing the workflow creation directly
func TestStandardWorkflow(ctx context.Context, client *Client) error {
	// Define parameters - using test S3 paths from drone-tm-public bucket
	odmProjectID := "test-fast-orthophoto"
	readS3Path := "s3://drone-tm-public/dtm-data/projects/a93e99f5-5aab-4316-b6f8-0acd56975df3/0c6e7cf3-e58f-4664-8a13-fa27dcdbb7ad/images/"
	writeS3Path := "s3://drone-tm-public/dtm-data/projects/a93e99f5-5aab-4316-b6f8-0acd56975df3/0c6e7cf3-e58f-4664-8a13-fa27dcdbb7ad/output/"
	odmFlags := []string{"--fast-orthophoto"}
	s3Region := "us-east-1"

	// Create workflow config
	config := NewDefaultODMConfig(odmProjectID, readS3Path, writeS3Path, odmFlags)
	config.S3Region = s3Region

	// Handle S3 credentials - always required
	// Get credentials from environment variables (SCALEODM_S3_ACCESS_KEY, etc.)
	envCreds, err := s3.GetS3JobCreds(s3Region)
	if err != nil {
		return fmt.Errorf("failed to get credentials from environment: %w", err)
	}
	if envCreds == nil {
		return fmt.Errorf("S3 credentials are required. Configure SCALEODM_S3_ACCESS_KEY and SCALEODM_S3_SECRET_KEY environment variables")
	}
	config.S3Credentials = envCreds
	fmt.Println("Using S3 credentials from environment variables")

	fmt.Printf("Read S3 Path: %s\n", readS3Path)
	fmt.Printf("Write S3 Path: %s\n", writeS3Path)
	fmt.Printf("ODM Flags: %v\n", odmFlags)

	wf, err := client.CreateODMWorkflow(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create workflow: %w", err)
	}
	fmt.Printf("Workflow created: %s\n", wf.Name)

	// Watch workflow until completion
	fmt.Println("\nWatching workflow until completion...")
	fmt.Println("   (This may take several minutes depending on image count and processing options)")
	completedWf, err := client.WatchWorkflow(ctx, wf.Name)
	if err != nil {
		return fmt.Errorf("failed to watch workflow: %w", err)
	}
	fmt.Printf("\nWorkflow completed with phase: %s\n", completedWf.Status.Phase)

	// Get workflow status
	phase, message, err := client.GetWorkflowStatus(ctx, wf.Name)
	if err != nil {
		return fmt.Errorf("failed to get workflow status: %w", err)
	}
	fmt.Printf("Workflow phase: %s", phase)
	if message != "" {
		fmt.Printf(", message: %s", message)
	}
	fmt.Println()

	// Get workflow logs
	fmt.Println("\nRetrieving workflow logs...")
	fmt.Println("==================================================================================")
	err = client.GetWorkflowLogs(ctx, wf.Name, os.Stdout)
	if err != nil {
		return fmt.Errorf("failed to get workflow logs: %w", err)
	}
	fmt.Println("==================================================================================")

	// Check if workflow is complete
	isComplete, err := client.IsWorkflowComplete(ctx, wf.Name)
	if err != nil {
		return fmt.Errorf("failed to check workflow completion: %w", err)
	}
	fmt.Printf("\nWorkflow complete: %v\n", isComplete)

	switch phase {
	case "Succeeded":
		fmt.Printf("\nSuccess! Final products should be available at:\n   %s\n", writeS3Path)
		return nil
	case "Failed", "Error":
		return fmt.Errorf("workflow failed with phase: %s", phase)
	default:
		return fmt.Errorf("workflow ended with unexpected phase: %s", phase)
	}
}
