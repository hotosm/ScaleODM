// Expose env var config vars, with defaults

package config

import (
	"cmp"
	"log"
	"os"
)

var SCALEODM_DATABASE_URL = cmp.Or(
	os.Getenv("SCALEODM_DATABASE_URL"),
	"",
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
