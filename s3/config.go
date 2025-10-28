package s3

import (
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/hotosm/scaleodm/config"
)

// Get S3 credentials to send to a job
func GetS3JobCreds() (string, string) {
	if config.SCALEODM_S3_STS_ROLE_ARN != "" {
		return getS3TempCreds()
	}
	return config.SCALEODM_S3_ACCESS_KEY, config.SCALEODM_S3_SECRET_KEY
}

func GetS3Client() *minio.Client {
	endpoint := config.SCALEODM_S3_ENDPOINT
	accessKey := config.SCALEODM_S3_ACCESS_KEY
	secretKey := config.SCALEODM_S3_SECRET_KEY

	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: true,
	})
	if err != nil {
		log.Fatalln(err)
	}

	return minioClient
}

// Get temp credentials via STS
func getS3TempCreds() (string, string) {
	sts_endpoint := config.SCALEODM_S3_STS_ENDPOINT
	accessKey := config.SCALEODM_S3_ACCESS_KEY
	secretKey := config.SCALEODM_S3_SECRET_KEY

	// IAM user/role to assume
	roleARN := config.SCALEODM_S3_STS_ROLE_ARN

	// Generate unique session name for parallel jobs
	sessionName := "odm-job-" + uuid.New().String()

	// Prepare STS AssumeRole options
	opts := credentials.STSAssumeRoleOptions{
		RoleARN:         roleARN,
		RoleSessionName: sessionName, // unique per job
		DurationSeconds: 86400,       // 24 hour
		AccessKey:       accessKey,
		SecretKey:       secretKey,
	}

	// Create temporary credentials
	// NOTE: even if sts_endpoint is empty here, the default for minio-go is
	// https://sts.amazonaws.com, so above we only check SCALEODM_S3_STS_ROLE_ARN
	stsCreds, err := credentials.NewSTSAssumeRole(sts_endpoint, opts)
	if err != nil {
		log.Fatalln(err)
	}

	// Retrieve credentials using MinIO-specific CredContext
	credCtx := &credentials.CredContext{}
	credsValue, err := stsCreds.GetWithContext(credCtx)
	if err != nil {
		log.Fatalln(err)
	}

	log.Println("Temporary S3 creds generated, expiry:", credsValue.Expiration.Format(time.RFC3339))

	return credsValue.AccessKeyID, credsValue.SecretAccessKey
}
