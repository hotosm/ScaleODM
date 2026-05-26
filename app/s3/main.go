package s3

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/hotosm/scaleodm/app/config"
)

func GetS3Client() *minio.Client {
	client, err := GetS3ClientWithCredentials(
		config.AWS_S3_ENDPOINT,
		config.AWS_ACCESS_KEY_ID,
		config.AWS_SECRET_ACCESS_KEY,
		"",
	)
	if err != nil {
		log.Fatalln(err)
	}
	return client
}

// NormalizeEndpoint strips unsupported URL components while preserving the
// scheme (if provided) for rclone-compatible endpoint injection.
func NormalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("endpoint is empty")
	}

	if !strings.Contains(endpoint, "://") {
		if idx := strings.IndexAny(endpoint, "/?#"); idx != -1 {
			endpoint = endpoint[:idx]
		}
		return strings.TrimSuffix(endpoint, "/"), nil
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid endpoint host")
	}

	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host), nil
}

// GetS3ClientForEndpoint builds a client using server credentials and an
// explicit endpoint override.
func GetS3ClientForEndpoint(endpoint string) (*minio.Client, error) {
	return GetS3ClientWithCredentials(
		endpoint,
		config.AWS_ACCESS_KEY_ID,
		config.AWS_SECRET_ACCESS_KEY,
		"",
	)
}

// GetS3ClientWithCredentials builds an S3 client from explicit endpoint and credentials.
func GetS3ClientWithCredentials(endpoint, accessKey, secretKey, sessionToken string) (*minio.Client, error) {
	normalized, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	resolvedEndpoint, secure := resolveMinIOEndpoint(normalized)
	bucketLookup := bucketLookupForEndpoint(resolvedEndpoint)

	client, err := minio.New(resolvedEndpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, sessionToken),
		Secure:       secure,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return nil, err
	}

	return client, nil
}

// GetArgoArchiveLogClient builds the client used to read Argo's archived
// workflow logs. Uses the dedicated SCALEODM_ARGO_ARCHIVE_LOG_* credentials
// when set, otherwise the runtime AWS creds against the archive bucket.
// Returns ok=false when the bucket or credentials are missing so callers can
// skip the fallback.
func GetArgoArchiveLogClient() (client *minio.Client, bucket string, ok bool, err error) {
	bucket = strings.TrimSpace(config.SCALEODM_ARGO_ARCHIVE_LOG_BUCKET)
	if bucket == "" {
		return nil, "", false, nil
	}

	endpoint := strings.TrimSpace(config.SCALEODM_ARGO_ARCHIVE_LOG_ENDPOINT)
	if endpoint == "" {
		endpoint = config.AWS_S3_ENDPOINT
	}

	accessKey := strings.TrimSpace(config.SCALEODM_ARGO_ARCHIVE_LOG_ACCESS_KEY_ID)
	secretKey := strings.TrimSpace(config.SCALEODM_ARGO_ARCHIVE_LOG_SECRET_ACCESS_KEY)
	if accessKey == "" || secretKey == "" {
		accessKey = config.AWS_ACCESS_KEY_ID
		secretKey = config.AWS_SECRET_ACCESS_KEY
	}
	if accessKey == "" || secretKey == "" {
		return nil, bucket, false, nil
	}

	client, err = GetS3ClientWithCredentials(endpoint, accessKey, secretKey, "")
	if err != nil {
		return nil, bucket, false, err
	}
	return client, bucket, true, nil
}

func resolveMinIOEndpoint(endpoint string) (string, bool) {
	secure := true
	if strings.HasPrefix(endpoint, "http://") {
		secure = false
		endpoint = strings.TrimPrefix(endpoint, "http://")
	} else if strings.HasPrefix(endpoint, "https://") {
		endpoint = strings.TrimPrefix(endpoint, "https://")
	}
	return endpoint, secure
}

func bucketLookupForEndpoint(endpoint string) minio.BucketLookupType {
	host := endpoint
	if parsedHost, _, found := strings.Cut(endpoint, ":"); found {
		host = parsedHost
	}
	host = strings.ToLower(host)

	if host == "s3.amazonaws.com" || strings.HasSuffix(host, ".amazonaws.com") {
		return minio.BucketLookupAuto
	}

	return minio.BucketLookupPath
}

func parseS3Path(s3Path string) (string, string, error) {
	if !strings.HasPrefix(s3Path, "s3://") {
		return "", "", fmt.Errorf("invalid S3 path: %s", s3Path)
	}

	pathParts := strings.TrimPrefix(s3Path, "s3://")
	parts := strings.SplitN(pathParts, "/", 2)
	if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
		return "", "", fmt.Errorf("invalid S3 path format: %s", s3Path)
	}

	bucket := parts[0]
	prefix := ""
	if len(parts) > 1 {
		prefix = strings.TrimSuffix(parts[1], "/") + "/"
	}

	return bucket, prefix, nil
}

// GetArgoArchiveContainerLog reads one container's archived stdout for a
// workflow. Argo's key format is {namespace}/{workflow}/{pod}/{container}.log,
// so we list the workflow prefix and concatenate matching keys in sorted
// order - that yields retry pods chronologically.
func GetArgoArchiveContainerLog(ctx context.Context, client *minio.Client, bucket, namespace, workflowName, container string) (string, error) {
	if strings.TrimSpace(bucket) == "" {
		return "", ErrArgoArchiveBucketUnset
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(workflowName) == "" {
		return "", fmt.Errorf("namespace and workflowName must be non-empty")
	}
	if strings.TrimSpace(container) == "" {
		return "", fmt.Errorf("container must be non-empty")
	}

	prefix := fmt.Sprintf("%s/%s/", namespace, workflowName)
	suffix := "/" + container + ".log"

	var keys []string
	for obj := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return "", fmt.Errorf("failed to list archive logs: %w", obj.Err)
		}
		if !strings.HasSuffix(obj.Key, suffix) {
			continue
		}
		keys = append(keys, obj.Key)
	}

	if len(keys) == 0 {
		return "", ErrArgoArchiveLogsNotFound
	}

	sort.Strings(keys)

	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "=== %s ===\n", strings.TrimPrefix(key, prefix))

		stream, err := client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
		if err != nil {
			fmt.Fprintf(&out, "(failed to open: %v)\n", err)
			continue
		}
		if _, err := io.Copy(&out, stream); err != nil {
			fmt.Fprintf(&out, "(failed to read: %v)\n", err)
		}
		stream.Close()
		out.WriteString("\n")
	}

	return out.String(), nil
}

// ErrArgoArchiveBucketUnset means no archive bucket is configured.
var ErrArgoArchiveBucketUnset = fmt.Errorf("argo archive log bucket not configured")

// ErrArgoArchiveLogsNotFound means no archived objects exist for the workflow.
var ErrArgoArchiveLogsNotFound = fmt.Errorf("no archived logs found for workflow")

// ObjectExistsInS3Path checks if an exact object exists under writeS3Path.
// writeS3Path is the S3 path where files are stored (e.g., s3://bucket/path/)
// fileName is the exact object key suffix under the prefix.
func ObjectExistsInS3Path(ctx context.Context, client *minio.Client, writeS3Path, fileName string) (bool, error) {
	bucket, prefix, err := parseS3Path(writeS3Path)
	if err != nil {
		return false, err
	}

	objectKey := prefix + strings.TrimPrefix(fileName, "/")
	_, err = client.StatObject(ctx, bucket, objectKey, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}

	errResp := minio.ToErrorResponse(err)
	if errResp.Code == "NoSuchKey" || errResp.Code == "NoSuchObject" || errResp.StatusCode == 404 {
		return false, nil
	}

	return false, fmt.Errorf("failed to stat object %q: %w", objectKey, err)
}

// ListFilesInS3Path lists files in the S3 path.
// writeS3Path is the S3 path where files are stored (e.g., s3://bucket/path/)
// Returns a list of object names (without the prefix).
func ListFilesInS3Path(ctx context.Context, client *minio.Client, writeS3Path string) ([]string, error) {
	return ListFilesInS3PathWithLimit(ctx, client, writeS3Path, 0)
}

// ListFilesInS3PathWithLimit lists files in the S3 path with an optional cap.
// If limit <= 0, all listed files are returned.
func ListFilesInS3PathWithLimit(ctx context.Context, client *minio.Client, writeS3Path string, limit int) ([]string, error) {
	bucket, prefix, err := parseS3Path(writeS3Path)
	if err != nil {
		return nil, err
	}

	listOpts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
	}
	if limit > 0 {
		listOpts.MaxKeys = limit
	}

	objectCh := client.ListObjects(ctx, bucket, listOpts)

	var files []string
	for object := range objectCh {
		if object.Err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", object.Err)
		}

		fileName := strings.TrimPrefix(object.Key, prefix)
		if fileName == "" || strings.HasSuffix(fileName, "/") || strings.Contains(fileName, "/") {
			continue
		}
		if strings.HasPrefix(fileName, ".") {
			continue
		}

		files = append(files, fileName)
		if limit > 0 && len(files) >= limit {
			break
		}
	}

	return files, nil
}

func ListObjectsRecursiveInS3Path(ctx context.Context, client *minio.Client, s3Path string) ([]minio.ObjectInfo, error) {
	bucket, prefix, err := parseS3Path(s3Path)
	if err != nil {
		return nil, err
	}

	objectCh := client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	objects := make([]minio.ObjectInfo, 0)
	for object := range objectCh {
		if object.Err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", object.Err)
		}
		if object.Key == "" || strings.HasSuffix(object.Key, "/") {
			continue
		}
		objects = append(objects, object)
	}

	return objects, nil
}

func sanitizeZipEntryName(prefix, objectKey string) (string, bool) {
	rel := strings.TrimPrefix(objectKey, prefix)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", false
	}

	clean := path.Clean(rel)
	if clean == "." || clean == "" {
		return "", false
	}
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	if strings.HasPrefix(clean, "/") {
		return "", false
	}

	return clean, true
}

func StreamS3PathAsZip(ctx context.Context, client *minio.Client, s3Path string, writer io.Writer) (int, error) {
	bucket, prefix, err := parseS3Path(s3Path)
	if err != nil {
		return 0, err
	}

	objects, err := ListObjectsRecursiveInS3Path(ctx, client, s3Path)
	if err != nil {
		return 0, err
	}

	zipWriter := zip.NewWriter(writer)

	written := 0
	for _, object := range objects {
		entryName, ok := sanitizeZipEntryName(prefix, object.Key)
		if !ok {
			log.Printf("skipping unsafe zip entry key=%q", object.Key)
			continue
		}

		header := &zip.FileHeader{
			Name:   entryName,
			Method: zip.Deflate,
		}
		if !object.LastModified.IsZero() {
			header.Modified = object.LastModified
		}

		entryWriter, createErr := zipWriter.CreateHeader(header)
		if createErr != nil {
			return written, fmt.Errorf("failed to create zip entry for %q: %w", object.Key, createErr)
		}

		objReader, getErr := client.GetObject(ctx, bucket, object.Key, minio.GetObjectOptions{})
		if getErr != nil {
			return written, fmt.Errorf("failed to read object %q: %w", object.Key, getErr)
		}

		if _, copyErr := io.Copy(entryWriter, objReader); copyErr != nil {
			objReader.Close()
			return written, fmt.Errorf("failed to stream object %q into zip: %w", object.Key, copyErr)
		}
		if closeErr := objReader.Close(); closeErr != nil {
			return written, fmt.Errorf("failed to close object %q: %w", object.Key, closeErr)
		}

		written++
	}

	if written == 0 {
		return 0, ErrNoObjectsToZip
	}

	if closeErr := zipWriter.Close(); closeErr != nil {
		return written, fmt.Errorf("failed to finalize zip stream: %w", closeErr)
	}

	return written, nil
}

var ErrNoObjectsToZip = errors.New("no objects to zip")

// ProbeS3Path checks that an S3 path is reachable by issuing a bounded list request.
// It is intended for readiness probes and avoids scanning large prefixes.
func ProbeS3Path(ctx context.Context, client *minio.Client, s3Path string) error {
	bucket, prefix, err := parseS3Path(s3Path)
	if err != nil {
		return err
	}

	objectCh := client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
		MaxKeys:   1,
	})

	for object := range objectCh {
		if object.Err != nil {
			return fmt.Errorf("failed to probe s3 path: %w", object.Err)
		}
		break
	}

	return nil
}

// CountImageStatsInS3Path counts supported image files and sums their object sizes under an S3 path recursively.
func CountImageStatsInS3Path(ctx context.Context, client *minio.Client, readS3Path string) (int, int64, error) {
	return CountImageStatsInS3PathWithExcludes(ctx, client, readS3Path, nil)
}

// CountImageStatsInS3PathWithExcludes is like CountImageStatsInS3Path but
// applies a best-effort filter that mirrors the rclone --filter-from set used
// by the download stage. It is intentionally approximate: rclone is the
// authoritative filter at workflow runtime, and a small mismatch here only
// affects pre-flight resource sizing, never final output correctness.
//
// Patterns supported (covers everything in DefaultProjectExcludes):
//   - "name/**" or "**/name/**"   → exclude any path with `name` as a directory segment
//   - "name" or "**/name"         → exclude exact basename match
//   - "*.ext"                     → exclude by extension
//
// Patterns this function doesn't recognise are ignored at the API layer; rclone
// will still apply them in-pod.
func CountImageStatsInS3PathWithExcludes(ctx context.Context, client *minio.Client, readS3Path string, excludePatterns []string) (int, int64, error) {
	bucket, prefix, err := parseS3Path(readS3Path)
	if err != nil {
		return 0, 0, err
	}

	objectCh := client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	allExcludes := make([]string, 0, len(alwaysExcludePatterns)+len(excludePatterns))
	allExcludes = append(allExcludes, alwaysExcludePatterns...)
	allExcludes = append(allExcludes, excludePatterns...)
	matcher := compileExcludeMatcher(allExcludes)
	return accumulateImageStatsFromObjectsWithExcludes(objectCh, prefix, matcher)
}

// CountImageFilesInS3Path counts image files under an S3 path recursively.
func CountImageFilesInS3Path(ctx context.Context, client *minio.Client, readS3Path string) (int, error) {
	count, _, err := CountImageStatsInS3Path(ctx, client, readS3Path)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func accumulateImageStatsFromObjects(objectCh <-chan minio.ObjectInfo) (int, int64, error) {
	return accumulateImageStatsFromObjectsWithExcludes(objectCh, "", excludeMatcher{})
}

func accumulateImageStatsFromObjectsWithExcludes(objectCh <-chan minio.ObjectInfo, prefix string, matcher excludeMatcher) (int, int64, error) {
	count := 0
	totalBytes := int64(0)
	for object := range objectCh {
		if object.Err != nil {
			return 0, 0, fmt.Errorf("failed to list objects: %w", object.Err)
		}
		if object.Key == "" || strings.HasSuffix(object.Key, "/") {
			continue
		}
		if !isSupportedImageKey(object.Key) {
			continue
		}
		if matcher.matches(object.Key, prefix) {
			continue
		}
		count++
		if object.Size > 0 {
			totalBytes += object.Size
		}
	}

	return count, totalBytes, nil
}

// excludeMatcher is a small, exact-match-only pattern matcher used for
// pre-flight image counting. It is not a full rclone filter implementation;
// see CountImageStatsInS3PathWithExcludes for the supported subset.
type excludeMatcher struct {
	dirs       map[string]struct{}
	basenames  map[string]struct{}
	extensions map[string]struct{}
}

func compileExcludeMatcher(patterns []string) excludeMatcher {
	m := excludeMatcher{
		dirs:       map[string]struct{}{},
		basenames:  map[string]struct{}{},
		extensions: map[string]struct{}{},
	}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		// Directory pattern: "name/**" or "**/name/**" → exclude segment "name"
		trimmed := strings.TrimPrefix(p, "**/")
		if strings.HasSuffix(trimmed, "/**") {
			name := strings.TrimSuffix(trimmed, "/**")
			if name != "" && !strings.ContainsAny(name, "*?[]/") {
				m.dirs[name] = struct{}{}
				continue
			}
		}
		// Trailing-slash directory form: "name/" → exclude segment "name"
		if strings.HasSuffix(trimmed, "/") {
			name := strings.TrimSuffix(trimmed, "/")
			if name != "" && !strings.ContainsAny(name, "*?[]/") {
				m.dirs[name] = struct{}{}
				continue
			}
		}
		// Extension: "*.ext"
		if strings.HasPrefix(trimmed, "*.") && !strings.ContainsAny(trimmed[2:], "*?[]/") {
			m.extensions[strings.ToLower(trimmed[1:])] = struct{}{}
			continue
		}
		// Plain basename: "name.ext" or "name"
		if !strings.ContainsAny(trimmed, "*?[]/") {
			m.basenames[trimmed] = struct{}{}
		}
	}
	return m
}

func (m excludeMatcher) matches(objectKey, prefix string) bool {
	rel := strings.TrimPrefix(objectKey, prefix)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return false
	}

	parts := strings.Split(rel, "/")
	if len(parts) > 1 {
		for _, segment := range parts[:len(parts)-1] {
			if _, ok := m.dirs[segment]; ok {
				return true
			}
		}
	}

	base := parts[len(parts)-1]
	if _, ok := m.basenames[base]; ok {
		return true
	}
	if len(m.extensions) > 0 {
		ext := strings.ToLower(filepath.Ext(base))
		if _, ok := m.extensions[ext]; ok {
			return true
		}
	}
	return false
}

func isSupportedImageKey(objectKey string) bool {
	ext := strings.ToLower(filepath.Ext(objectKey))
	switch ext {
	case ".jpg", ".jpeg", ".tif", ".tiff":
		return true
	default:
		return false
	}
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
