// Expose env var config vars, with defaults

package config

import (
	"cmp"
	"log"
	"os"
)

var SCALEODM_ODM_IMAGE = cmp.Or(
	os.Getenv("SCALEODM_ODM_IMAGE"),
	"docker.io/opendronemap/odm:3.5.6",
)

var SCALEODM_DATABASE_URL = cmp.Or(
	os.Getenv("SCALEODM_DATABASE_URL"),
	"",
)

var KUBECONFIG_PATH = cmp.Or(
	os.Getenv("KUBECONFIG_PATH"),
	"", // leave empty if in-cluster
)
var K8S_NAMESPACE = cmp.Or(
	os.Getenv("K8S_NAMESPACE"),
	"argo",
)

// Note this S3 user must have permissions
// to create temporary credentials for STS
var SCALEODM_S3_ENDPOINT = cmp.Or(
	os.Getenv("SCALEODM_S3_ENDPOINT"),
	"",
)
var SCALEODM_S3_ACCESS_KEY = cmp.Or(
	os.Getenv("SCALEODM_S3_ACCESS_KEY"),
	"",
)
var SCALEODM_S3_SECRET_KEY = cmp.Or(
	os.Getenv("SCALEODM_S3_SECRET_KEY"),
	"",
)
var SCALEODM_S3_STS_ENDPOINT = cmp.Or(
	os.Getenv("SCALEODM_S3_STS_ENDPOINT"),
	"",
)
var SCALEODM_S3_STS_ROLE_ARN = cmp.Or(
	os.Getenv("SCALEODM_S3_STS_ROLE_ARN"),
	"",
)

var ENQUEUE_TEST_JOBS = cmp.Or(
	os.Getenv("ENQUEUE_TEST_JOBS"),
	"false",
)

var SCALEODM_CLUSTER_URL = cmp.Or(
	os.Getenv("SCALEODM_CLUSTER_URL"),
	"http://localhost:31100",
)

func ValidateEnv() {
	required := []struct {
		val  string
		name string
	}{
		{SCALEODM_DATABASE_URL, "SCALEODM_DATABASE_URL"},
		{SCALEODM_S3_ENDPOINT, "SCALEODM_S3_ENDPOINT"},
	}

	for _, envVar := range required {
		if envVar.val == "" {
			log.Fatalf("%s is required", envVar.name)
		}
	}

	// S3 credentials are optional - can use public buckets or provide via API
	if SCALEODM_S3_ACCESS_KEY == "" || SCALEODM_S3_SECRET_KEY == "" {
		log.Println("Warning: SCALEODM_S3_ACCESS_KEY and SCALEODM_S3_SECRET_KEY not set. Public bucket access will be attempted if no credentials provided via API.")
	}
}
