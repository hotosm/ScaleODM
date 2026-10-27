// Output asset discovery and downloads for finished tasks.

package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/minio/minio-go/v7"

	"github.com/hotosm/scaleodm/app/s3"
)

const (
	taskAssetsDefaultAdditionalLimit = 100
	taskAssetsMaxAdditionalLimit     = 1000
)

type taskPrimaryAssetDefinition struct {
	ID     string
	Assets []string
}

// taskAssetAliasCandidates lets /download/{alias} resolve to the first output path that exists.
var taskAssetAliasCandidates = map[string][]string{
	"orthophoto": {"odm_orthophoto/odm_orthophoto.tif", "orthophoto.tif"},
	"dsm":        {"odm_dem/dsm.tif", "dsm.tif"},
	"dtm":        {"odm_dem/dtm.tif", "dtm.tif"},
	"point_cloud": {
		"odm_georeferencing/odm_georeferenced_model.laz",
		"odm_georeferencing/odm_georeferenced_model.las",
		"odm_georeferencing/odm_georeferenced_model.ply",
		"georeferenced_model.laz",
		"georeferenced_model.las",
		"georeferenced_model.ply",
		"point_cloud.laz",
		"point_cloud.ply",
	},
}

var taskPrimaryAssetDefinitions = []taskPrimaryAssetDefinition{
	{ID: "all_zip", Assets: []string{"all.zip"}},
	{ID: "orthophoto", Assets: taskAssetAliasCandidates["orthophoto"]},
	{ID: "dsm", Assets: taskAssetAliasCandidates["dsm"]},
	{ID: "dtm", Assets: taskAssetAliasCandidates["dtm"]},
	{ID: "point_cloud", Assets: taskAssetAliasCandidates["point_cloud"]},
}

func taskAssetDownloadURL(uuid, asset string) string {
	return fmt.Sprintf("/task/%s/download/%s", uuid, asset)
}

func resolveAssetAliasCandidate(ctx context.Context, client *minio.Client, writeS3Path, requestedAsset string) (string, bool, error) {
	candidates, ok := taskAssetAliasCandidates[requestedAsset]
	if !ok {
		return "", false, nil
	}

	for _, candidate := range candidates {
		exists, err := s3.ObjectExistsInS3Path(ctx, client, writeS3Path, candidate)
		if err != nil {
			return "", false, err
		}
		if exists {
			return candidate, true, nil
		}
	}

	return "", true, nil
}

func clampTaskAdditionalLimit(limit int) int {
	if limit <= 0 {
		return taskAssetsDefaultAdditionalLimit
	}
	if limit > taskAssetsMaxAdditionalLimit {
		return taskAssetsMaxAdditionalLimit
	}
	return limit
}

type taskAssetsInput struct {
	UUID              string `path:"uuid" doc:"UUID of the task"`
	Token             string `query:"token" doc:"Authentication token (optional)"`
	IncludeAdditional bool   `query:"includeAdditional" default:"false" doc:"Include additional discovered files"`
	AdditionalLimit   int    `query:"additionalLimit" default:"100" doc:"Maximum number of additional files to return (clamped to 1000)"`
}

func (a *API) getTaskAssets(ctx context.Context, input *taskAssetsInput) (*TaskAssetsResponse, error) {
	log.Printf("GET /task/%s/assets: token_provided=%t includeAdditional=%t additionalLimit=%d", input.UUID, input.Token != "", input.IncludeAdditional, input.AdditionalLimit)

	job, err := a.metadataStore.GetJob(ctx, input.UUID)
	if err != nil {
		log.Printf("GET /task/%s/assets: failed to retrieve task metadata: %v", input.UUID, err)
		return nil, huma.NewError(500, "Failed to retrieve task metadata", err)
	}
	if job == nil {
		log.Printf("GET /task/%s/assets: task not found", input.UUID)
		return nil, huma.NewError(404, "Task not found")
	}
	if job.WriteS3Path == "" {
		log.Printf("GET /task/%s/assets: write S3 path not available", input.UUID)
		return nil, huma.NewError(400, "Write S3 path not available for this task")
	}

	metadata := parseTaskMetadata(job.Metadata)
	s3Client, err := metadata.s3Client()
	if err != nil {
		log.Printf("GET /task/%s/assets: failed to initialize S3 client endpoint=%q: %v", input.UUID, metadata.s3Endpoint(), err)
		return nil, huma.NewError(500, "Failed to initialize S3 client", err)
	}

	response := &TaskAssetsResponse{}
	response.Body.Primary = make([]TaskAssetsPrimaryItem, 0, len(taskPrimaryAssetDefinitions))
	representedAssets := map[string]struct{}{}
	for _, definition := range taskPrimaryAssetDefinitions {
		item := TaskAssetsPrimaryItem{ID: definition.ID, Asset: definition.Assets[0]}
		for _, candidate := range definition.Assets {
			exists, err := s3.ObjectExistsInS3Path(ctx, s3Client, job.WriteS3Path, candidate)
			if err != nil {
				log.Printf("GET /task/%s/assets: failed checking candidate asset=%q: %v", input.UUID, candidate, err)
				return nil, huma.NewError(500, "Failed to query task assets", err)
			}
			if exists {
				item.Asset = candidate
				item.Exists = true
				item.DownloadURL = taskAssetDownloadURL(input.UUID, candidate)
				representedAssets[candidate] = struct{}{}
				break
			}
		}
		response.Body.Primary = append(response.Body.Primary, item)
	}

	if input.IncludeAdditional {
		limit := clampTaskAdditionalLimit(input.AdditionalLimit)
		listedAssets, err := s3.ListFilesInS3PathWithLimit(ctx, s3Client, job.WriteS3Path, limit)
		if err != nil {
			log.Printf("GET /task/%s/assets: failed listing additional assets: %v", input.UUID, err)
			return nil, huma.NewError(500, "Failed to query task assets", err)
		}
		additional := make([]TaskAssetsAdditionalItem, 0, len(listedAssets))
		for _, asset := range listedAssets {
			if _, represented := representedAssets[asset]; represented {
				continue
			}
			additional = append(additional, TaskAssetsAdditionalItem{Asset: asset, DownloadURL: taskAssetDownloadURL(input.UUID, asset)})
		}
		response.Body.Additional = additional
	}

	log.Printf("GET /task/%s/assets: returning primary=%d additional=%d includeAdditional=%t", input.UUID, len(response.Body.Primary), len(response.Body.Additional), input.IncludeAdditional)
	return response, nil
}

// downloadTaskAsset serves GET /task/{uuid}/download/{asset}. It is a raw
// handler because Huma's structured responses can't express HTTP redirects.
func (a *API) downloadTaskAsset(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	asset := r.PathValue("asset")
	requestedAsset := asset
	log.Printf("GET /task/%s/download/%s", uuid, asset)
	notFound := func() {
		http.Error(w, fmt.Sprintf(`{"error":"File not found: %s"}`, requestedAsset), http.StatusNotFound)
	}

	job, err := a.metadataStore.GetJob(r.Context(), uuid)
	if err != nil {
		log.Printf("GET /task/%s/download/%s: failed to retrieve metadata: %v", uuid, asset, err)
		http.Error(w, `{"error":"Failed to retrieve task metadata"}`, http.StatusInternalServerError)
		return
	}
	if job == nil {
		log.Printf("GET /task/%s/download/%s: task not found", uuid, asset)
		http.Error(w, `{"error":"Task not found"}`, http.StatusNotFound)
		return
	}
	if job.WriteS3Path == "" {
		log.Printf("GET /task/%s/download/%s: write S3 path not available", uuid, asset)
		http.Error(w, `{"error":"Write S3 path not available for this task"}`, http.StatusBadRequest)
		return
	}

	metadata := parseTaskMetadata(job.Metadata)
	s3Client, err := metadata.s3Client()
	if err != nil {
		log.Printf("GET /task/%s/download/%s: failed to initialize S3 client endpoint=%q: %v", uuid, asset, metadata.s3Endpoint(), err)
		http.Error(w, `{"error":"Failed to initialize S3 client"}`, http.StatusInternalServerError)
		return
	}

	resolvedAsset, isAlias, err := resolveAssetAliasCandidate(r.Context(), s3Client, job.WriteS3Path, requestedAsset)
	switch {
	case err != nil:
		log.Printf("GET /task/%s/download/%s: failed to resolve asset alias: %v", uuid, asset, err)
		http.Error(w, `{"error":"Failed to resolve asset alias"}`, http.StatusInternalServerError)
		return
	case isAlias && resolvedAsset == "":
		log.Printf("GET /task/%s/download/%s: alias requested but no asset found", uuid, asset)
		notFound()
		return
	case isAlias:
		asset = resolvedAsset
	}

	assetExists, err := s3.ObjectExistsInS3Path(r.Context(), s3Client, job.WriteS3Path, asset)
	if err != nil {
		log.Printf("GET /task/%s/download/%s: failed to check asset existence: %v", uuid, requestedAsset, err)
		http.Error(w, `{"error":"Failed to query task asset"}`, http.StatusInternalServerError)
		return
	}
	if assetExists {
		presignedURL, err := s3.GeneratePresignedURL(r.Context(), s3Client, job.WriteS3Path, asset, 1*time.Hour)
		if err != nil {
			log.Printf("GET /task/%s/download/%s: failed to generate pre-signed URL: %v", uuid, requestedAsset, err)
			notFound()
			return
		}
		log.Printf("GET /task/%s/download/%s: redirecting to pre-signed URL for key=%q (expires in 1 hour)", uuid, requestedAsset, asset)
		http.Redirect(w, r, presignedURL, http.StatusFound)
		return
	}

	if requestedAsset != "all.zip" {
		log.Printf("GET /task/%s/download/%s: asset not found", uuid, requestedAsset)
		notFound()
		return
	}

	var zipBuffer bytes.Buffer
	written, err := s3.StreamS3PathAsZip(r.Context(), s3Client, job.WriteS3Path, &zipBuffer)
	if err == s3.ErrNoObjectsToZip {
		log.Printf("GET /task/%s/download/%s: no output objects found for synthetic all.zip", uuid, requestedAsset)
		notFound()
		return
	}
	if err != nil {
		log.Printf("GET /task/%s/download/%s: failed to stream synthetic all.zip: %v", uuid, requestedAsset, err)
		http.Error(w, `{"error":"Failed to stream task output"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="all.zip"`)
	if _, err := zipBuffer.WriteTo(w); err != nil {
		log.Printf("GET /task/%s/download/%s: failed to write synthetic all.zip response: %v", uuid, requestedAsset, err)
		return
	}
	log.Printf("GET /task/%s/download/%s: streamed synthetic all.zip with %d entries", uuid, requestedAsset, written)
}
