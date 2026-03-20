package s3

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/hotosm/scaleodm/app/config"
)

func GetS3Client() *minio.Client {
	endpoint := config.SCALEODM_S3_ENDPOINT
	accessKey := config.SCALEODM_S3_ACCESS_KEY
	secretKey := config.SCALEODM_S3_SECRET_KEY

	// Determine if we should use secure connection
	// For AWS S3, always use secure. For custom endpoints, check if it's https
	secure := true
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		// No protocol specified, assume secure for AWS, but allow override via config
		secure = true
	} else if strings.HasPrefix(endpoint, "http://") {
		secure = false
		endpoint = strings.TrimPrefix(endpoint, "http://")
	} else if strings.HasPrefix(endpoint, "https://") {
		secure = true
		endpoint = strings.TrimPrefix(endpoint, "https://")
	}

	// MinIO client doesn't allow paths, query parameters, or fragments in endpoint
	// Endpoint should be just host:port or host
	// Strip any path, query, or fragment components
	if idx := strings.IndexAny(endpoint, "/?#"); idx != -1 {
		endpoint = endpoint[:idx]
	}

	// Remove trailing slash if present (shouldn't happen after above, but be safe)
	endpoint = strings.TrimSuffix(endpoint, "/")

	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		log.Fatalln(err)
	}

	return minioClient
}

// GetWorkflowLogsFromS3 fetches workflow logs from S3
// writeS3Path is the S3 path where logs are stored (e.g., s3://bucket/path/)
// Returns the log content as a string
func GetWorkflowLogsFromS3(ctx context.Context, client *minio.Client, writeS3Path string) (string, error) {
	// Parse S3 path: s3://bucket/path -> bucket and path
	if !strings.HasPrefix(writeS3Path, "s3://") {
		return "", fmt.Errorf("invalid S3 path: %s", writeS3Path)
	}

	pathParts := strings.TrimPrefix(writeS3Path, "s3://")
	parts := strings.SplitN(pathParts, "/", 2)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid S3 path format: %s", writeS3Path)
	}

	bucket := parts[0]
	prefix := ""
	if len(parts) > 1 {
		prefix = strings.TrimSuffix(parts[1], "/") + "/"
	}

	logObjectKey := prefix + ".workflow-logs.txt"

	// Get object from S3
	obj, err := client.GetObject(ctx, bucket, logObjectKey, minio.GetObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get log object from S3: %w", err)
	}
	defer obj.Close()

	// Read object content
	content, err := io.ReadAll(obj)
	if err != nil {
		return "", fmt.Errorf("failed to read log object: %w", err)
	}

	return string(content), nil
}

// ListFilesInS3Path lists files in the S3 path
// writeS3Path is the S3 path where files are stored (e.g., s3://bucket/path/)
// Returns a list of object names (without the prefix)
func ListFilesInS3Path(ctx context.Context, client *minio.Client, writeS3Path string) ([]string, error) {
	// Parse S3 path: s3://bucket/path -> bucket and path
	if !strings.HasPrefix(writeS3Path, "s3://") {
		return nil, fmt.Errorf("invalid S3 path: %s", writeS3Path)
	}

	pathParts := strings.TrimPrefix(writeS3Path, "s3://")
	parts := strings.SplitN(pathParts, "/", 2)
	if len(parts) < 1 {
		return nil, fmt.Errorf("invalid S3 path format: %s", writeS3Path)
	}

	bucket := parts[0]
	prefix := ""
	if len(parts) > 1 {
		prefix = strings.TrimSuffix(parts[1], "/") + "/"
	}

	// List objects with the prefix
	objectCh := client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
	})

	var files []string
	for object := range objectCh {
		if object.Err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", object.Err)
		}
		// Remove the prefix from the object key to get just the filename
		fileName := strings.TrimPrefix(object.Key, prefix)
		// Skip hidden files and directories
		if fileName != "" && !strings.HasPrefix(fileName, ".") {
			files = append(files, fileName)
		}
	}

	return files, nil
}

// GeneratePresignedURL generates a pre-signed URL for downloading a file from S3
// writeS3Path is the S3 path where files are stored (e.g., s3://bucket/path/)
// fileName is the name of the file to download (e.g., "all.zip", "orthophoto.tif")
// expiry is how long the URL should be valid (defaults to 1 hour if 0)
// Returns the pre-signed URL as a string
func GeneratePresignedURL(ctx context.Context, client *minio.Client, writeS3Path, fileName string, expiry time.Duration) (string, error) {
	// Parse S3 path: s3://bucket/path -> bucket and path
	if !strings.HasPrefix(writeS3Path, "s3://") {
		return "", fmt.Errorf("invalid S3 path: %s", writeS3Path)
	}

	pathParts := strings.TrimPrefix(writeS3Path, "s3://")
	parts := strings.SplitN(pathParts, "/", 2)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid S3 path format: %s", writeS3Path)
	}

	bucket := parts[0]
	prefix := ""
	if len(parts) > 1 {
		prefix = strings.TrimSuffix(parts[1], "/") + "/"
	}

	objectKey := prefix + fileName

	// Default expiry to 1 hour if not specified
	if expiry == 0 {
		expiry = 1 * time.Hour
	}

	// Generate pre-signed URL
	presignedURL, err := client.PresignedGetObject(ctx, bucket, objectKey, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("failed to generate pre-signed URL for %s: %w", objectKey, err)
	}

	return presignedURL.String(), nil
}
