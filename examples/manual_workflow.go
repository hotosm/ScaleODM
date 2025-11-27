// Example: Manual workflow creation via API
// This example demonstrates how to create an ODM workflow using the ScaleODM API,
// then poll for completion. This is the recommended way to interact with ScaleODM.
//
// Usage:
//
//	# Recommended: Use Justfile command (loads .env automatically)
//	just run-example
//
//	# Or run directly (requires environment variables to be set)
//	go run examples/manual_workflow.go
//
// Environment variables (required):
//
//	SCALEODM_API_URL - API base URL (default: http://localhost:31100)
//	SCALEODM_S3_ACCESS_KEY - S3 access key (required)
//	SCALEODM_S3_SECRET_KEY - S3 secret key (required)
//	SCALEODM_S3_STS_ROLE_ARN - STS role ARN (optional, for temporary credentials)
//	SCALEODM_S3_STS_ENDPOINT - STS endpoint (optional, defaults to https://sts.us-east-1.amazonaws.com)
//
// If SCALEODM_S3_STS_ROLE_ARN is set, temporary STS credentials will be generated
// from the provided credentials. Otherwise, the provided credentials are used directly.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hotosm/scaleodm/app/api"
)

const (
	defaultAPIURL = "http://localhost:31100"
	pollInterval  = 10 * time.Second
	maxWaitTime   = 60 * time.Minute
)

type ODMOption struct {
	Name  string      `json:"name"`
	Value interface{} `json:"value"`
}

func main() {
	// Get API URL from environment
	apiURL := os.Getenv("SCALEODM_API_URL")
	if apiURL == "" {
		apiURL = defaultAPIURL
	}

	// Define parameters - using test S3 paths from drone-tm-public bucket
	readS3Path := "s3://drone-tm-public/dtm-data/test/"
	writeS3Path := "s3://drone-tm-public/dtm-data/test/output/"

	// Convert options array to JSON string (as required by API)
	optionsArray := []ODMOption{
		{Name: "fast-orthophoto", Value: true},
	}
	optionsJSON, err := json.Marshal(optionsArray)
	if err != nil {
		log.Fatalf("Failed to marshal options: %v", err)
	}

	// Create task request using the shared struct from API package
	taskReq := &api.TaskNewRequest{
		Name:              "test-fast-orthophoto",
		ReadS3Path:        readS3Path,
		WriteS3Path:       writeS3Path,
		Options:           string(optionsJSON), // JSON string, e.g., "[{\"name\":\"fast-orthophoto\",\"value\":true}]"
		SkipPostProcessing: false,
		Webhook:           "",
		ZipURL:            "",
		S3Region:          "us-east-1",
		// S3AccessKeyID / S3SecretAccessKey / S3SessionToken are filled from env below
		// DateCreated left as zero so the server uses its current time
	}

	// Add credentials if available
	if accessKey := os.Getenv("SCALEODM_S3_ACCESS_KEY"); accessKey != "" {
		taskReq.S3AccessKeyID = accessKey
	}
	if secretKey := os.Getenv("SCALEODM_S3_SECRET_KEY"); secretKey != "" {
		taskReq.S3SecretAccessKey = secretKey
	}
	if sessionToken := os.Getenv("SCALEODM_S3_SESSION_TOKEN"); sessionToken != "" {
		taskReq.S3SessionToken = sessionToken
	}

	fmt.Println("🚀 Creating ODM task via API...")
	fmt.Printf("   API URL: %s\n", apiURL)
	fmt.Printf("   Read S3 Path: %s\n", readS3Path)
	fmt.Printf("   Write S3 Path: %s\n", writeS3Path)

	// Create task via API
	taskUUID, err := createTask(apiURL, taskReq)
	if err != nil {
		log.Fatalf("Failed to create task: %v", err)
	}

	fmt.Printf("✅ Task created: %s\n", taskUUID)
	fmt.Println("\n⏳ Polling for task completion...")
	fmt.Printf("   (This may take several minutes depending on image count and processing options)\n")
	fmt.Printf("   Polling every %v, max wait time: %v\n", pollInterval, maxWaitTime)

	// Poll for completion
	startTime := time.Now()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-time.After(maxWaitTime):
			log.Fatalf("Task timed out after %v", maxWaitTime)
		case <-ticker.C:
			info, err := getTaskInfo(apiURL, taskUUID)
			if err != nil {
				log.Printf("Warning: Failed to get task info: %v", err)
				continue
			}

			elapsed := time.Since(startTime)
			fmt.Printf("[%s] Status: %d, Progress: %d%%, Processing time: %dms\n",
				elapsed.Round(time.Second),
				info.Status,
				info.Progress,
				info.ProcessingTime,
			)

			// Check if complete
			if info.Status == 40 { // COMPLETED
				fmt.Printf("\n✅ Task completed successfully!\n")
				fmt.Printf("🎉 Final products should be available at:\n   %s\n", writeS3Path)
				return
			} else if info.Status == 30 { // FAILED
				fmt.Printf("\n❌ Task failed!\n")
				// Try to get output/logs
				output, err := getTaskOutput(apiURL, taskUUID)
				if err == nil && output != "" {
					fmt.Println("\n📋 Task output:")
					fmt.Println("==================================================================================")
					fmt.Println(output)
					fmt.Println("==================================================================================")
				}
				os.Exit(1)
			} else if info.Status == 50 { // CANCELED
				fmt.Printf("\n⚠️  Task was canceled\n")
				os.Exit(1)
			}
			// Continue polling for status 10 (QUEUED) or 20 (RUNNING)
		}
	}
}

func createTask(apiURL string, taskReq *api.TaskNewRequest) (string, error) {
	// Marshal request to JSON
	// Use a map to ensure all fields are included even if empty
	// Note: Options must be a JSON string (not array)
	jsonMap := map[string]interface{}{
		"name":               taskReq.Name,
		"options":            taskReq.Options, // This is already a JSON string
		"webhook":            taskReq.Webhook,
		"skipPostProcessing": taskReq.SkipPostProcessing,
		"zipurl":             taskReq.ZipURL,
		"readS3Path":         taskReq.ReadS3Path,
		"writeS3Path":        taskReq.WriteS3Path,
		"s3AccessKeyID":      taskReq.S3AccessKeyID,
		"s3SecretAccessKey":  taskReq.S3SecretAccessKey,
		"s3SessionToken":     taskReq.S3SessionToken,
		"s3Region":           taskReq.S3Region,
		"dateCreated":        taskReq.DateCreated,
	}
	jsonData, err := json.Marshal(jsonMap)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Debug: log the request being sent
	log.Printf("Sending request: %s", string(jsonData))

	req, err := http.NewRequest("POST", apiURL+"/task/new", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Debug: print response body
	if len(bodyBytes) > 0 {
		log.Printf("Task creation response: %s", string(bodyBytes))
	}

	var result api.TaskNewResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w (response: %s)", err, string(bodyBytes))
	}

	if result.Body.UUID == "" {
		return "", fmt.Errorf("API returned empty UUID (response: %s)", string(bodyBytes))
	}

	return result.Body.UUID, nil
}

func getTaskInfo(apiURL, uuid string) (*api.TaskInfo, error) {
	if uuid == "" {
		return nil, fmt.Errorf("task UUID is empty")
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/task/%s/info", apiURL, uuid), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Check if response is HTML (likely a 404 page)
		bodyStr := string(bodyBytes)
		if len(bodyStr) > 0 && bodyStr[0] == '<' {
			return nil, fmt.Errorf("API returned status %d with HTML response (endpoint may not exist): %s", resp.StatusCode, bodyStr[:min(200, len(bodyStr))])
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, bodyStr)
	}

	// Check if response is JSON
	if len(bodyBytes) == 0 || bodyBytes[0] != '{' {
		return nil, fmt.Errorf("API returned non-JSON response: %s", string(bodyBytes[:min(200, len(bodyBytes))]))
	}

	var result api.TaskInfo
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w (response: %s)", err, string(bodyBytes[:min(200, len(bodyBytes))]))
	}

	return &result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func getTaskOutput(apiURL, uuid string) (string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/task/%s/output", apiURL, uuid), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Body, nil
}
