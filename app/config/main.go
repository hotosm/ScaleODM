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

// K8S_NAMESPACE is the namespace where ScaleODM and its workflows run.
// Defaults to "default". In Helm deployments this is set to the release namespace.
var K8S_NAMESPACE = cmp.Or(
	os.Getenv("K8S_NAMESPACE"),
	"default",
)

var SCALEODM_S3_ENDPOINT = cmp.Or(
	os.Getenv("SCALEODM_S3_ENDPOINT"),
	"s3.amazonaws.com",
)
var SCALEODM_S3_ACCESS_KEY = cmp.Or(
	os.Getenv("SCALEODM_S3_ACCESS_KEY"),
	"",
)
var SCALEODM_S3_SECRET_KEY = cmp.Or(
	os.Getenv("SCALEODM_S3_SECRET_KEY"),
	"",
)

// SCALEODM_S3_SECRET_NAME is the name of the Kubernetes Secret (in the same
// namespace as ScaleODM) that contains the S3 credentials. Workflow containers
// reference this secret via secretKeyRef so that credentials are never inlined
// in the Argo Workflow spec. The secret must contain the keys:
//   - AWS_ACCESS_KEY_ID
//   - AWS_SECRET_ACCESS_KEY
//   - AWS_DEFAULT_REGION
var SCALEODM_S3_SECRET_NAME = cmp.Or(
	os.Getenv("SCALEODM_S3_SECRET_NAME"),
	"scaleodm-s3-creds",
)

var ENQUEUE_TEST_JOBS = cmp.Or(
	os.Getenv("ENQUEUE_TEST_JOBS"),
	"false",
)

func ValidateEnv() {
	required := []struct {
		val  string
		name string
	}{
		{SCALEODM_DATABASE_URL, "SCALEODM_DATABASE_URL"},
		{SCALEODM_S3_ENDPOINT, "SCALEODM_S3_ENDPOINT"},
		{SCALEODM_S3_ACCESS_KEY, "SCALEODM_S3_ACCESS_KEY"},
		{SCALEODM_S3_SECRET_KEY, "SCALEODM_S3_SECRET_KEY"},
	}

	for _, envVar := range required {
		if envVar.val == "" {
			log.Fatalf("%s is required", envVar.name)
		}
	}
}
