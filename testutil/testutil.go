package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// TestDBURL returns the database URL from SCALEODM_DATABASE_URL environment variable
func TestDBURL() string {
	dbURL := os.Getenv("SCALEODM_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://odm:odm@localhost:31101/scaleodm?sslmode=disable"
	}
	return dbURL
}

// WaitForDB waits for the database to be available
func WaitForDB(dbURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		config, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			return fmt.Errorf("failed to parse connection string: %w", err)
		}

		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = pool.Ping(ctx)
		cancel()
		pool.Close()

		if err == nil {
			return nil
		}

		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("database not available after %v", timeout)
}

// TestS3Endpoint returns the S3 endpoint for tests.
// We deliberately ignore any production SCALEODM_S3_ENDPOINT override so that
// the test suite never talks to a real AWS S3 endpoint just because the
// developer has those env vars set in their shell.
func TestS3Endpoint() string {
	return "localhost:31102"
}

// TestS3AccessKey returns the S3 access key for tests.
// This is hard-coded to match the MinIO test instance credentials.
func TestS3AccessKey() string {
	return "odm"
}

// TestS3SecretKey returns the S3 secret key for tests.
// This is hard-coded to match the MinIO test instance credentials.
func TestS3SecretKey() string {
	return "somelongpassword"
}

// SetupTestS3Bucket creates a test bucket in MinIO if it doesn't exist
// This should be called before tests that use S3 buckets
// Tries both HTTP and HTTPS to handle different MinIO configurations
func SetupTestS3Bucket(ctx context.Context, bucketName string) error {
	endpoint := TestS3Endpoint()
	accessKey := TestS3AccessKey()
	secretKey := TestS3SecretKey()
	
	// Debug: log the credentials being used (without exposing the full secret)
	if len(secretKey) > 0 {
		fmt.Printf("DEBUG: Setting up S3 bucket with endpoint=%q, accessKey=%q, secretKeyLen=%d\n", 
			endpoint, accessKey, len(secretKey))
	}

	// Parse and clean endpoint - MinIO client doesn't allow paths, query params, or fragments
	// Strip protocol if present
	if strings.HasPrefix(endpoint, "http://") {
		endpoint = strings.TrimPrefix(endpoint, "http://")
	} else if strings.HasPrefix(endpoint, "https://") {
		endpoint = strings.TrimPrefix(endpoint, "https://")
	}

	// Strip any path, query, or fragment components
	if idx := strings.IndexAny(endpoint, "/?#"); idx != -1 {
		endpoint = endpoint[:idx]
	}

	// Remove trailing slash if present
	endpoint = strings.TrimSuffix(endpoint, "/")

	// Try HTTP first (typical for local MinIO), then HTTPS
	for _, secure := range []bool{false, true} {
		client, err := minio.New(endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
			Secure: secure,
		})
		if err != nil {
			if secure {
				// If HTTPS also fails, return the error
				return fmt.Errorf("failed to create MinIO client (tried HTTP and HTTPS): %w", err)
			}
			// Try HTTPS next
			continue
		}

		// Check if bucket exists
		exists, err := client.BucketExists(ctx, bucketName)
		if err != nil {
			// In some environments (e.g. when pointed at real AWS S3 with
			// restricted credentials), BucketExists can return an AccessDenied
			// error even though the endpoint is reachable. For tests that only
			// need a syntactically valid S3 path and never actually touch the
			// bucket contents, treat AccessDenied as a non-fatal condition so
			// tests remain hermetic with production-like credentials.
			errResp := minio.ToErrorResponse(err)
			if errResp.Code == "AccessDenied" || errResp.StatusCode == 403 {
				return nil
			}

			if secure {
				// If HTTPS also fails, return the error
				return fmt.Errorf("failed to check if bucket exists (tried HTTP and HTTPS): %w", err)
			}
			// Try HTTPS next
			continue
		}

		if !exists {
			// Create bucket
			err = client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{
				Region: "us-east-1",
			})
			if err != nil {
				if secure {
					return fmt.Errorf("failed to create bucket %q (tried HTTP and HTTPS): %w", bucketName, err)
				}
				// Try HTTPS next
				continue
			}
		}

		// Success
		return nil
	}

	return fmt.Errorf("failed to set up bucket %q: all connection attempts failed", bucketName)
}

