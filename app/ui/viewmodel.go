package ui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hotosm/scaleodm/app/meta"
)

const (
	statusCodeQueued    = 10
	statusCodeRunning   = 20
	statusCodeFailed    = 30
	statusCodeCompleted = 40
	statusCodeCanceled  = 50
)

type taskSummary struct {
	UUID         string `json:"uuid"`
	ProjectID    string `json:"projectID"`
	Status       string `json:"status"`
	StatusLabel  string `json:"statusLabel"`
	StatusCode   int    `json:"statusCode"`
	Progress     int    `json:"progress"`
	CreatedAt    string `json:"createdAt"`
	StartedAt    string `json:"startedAt,omitempty"`
	CompletedAt  string `json:"completedAt,omitempty"`
	CreatedAtAgo string `json:"createdAtAgo"`
}

type taskOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type taskAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type taskDetail struct {
	Task    taskSummary  `json:"task"`
	ReadS3  string       `json:"readS3Path"`
	WriteS3 string       `json:"writeS3Path"`
	Region  string       `json:"s3Region"`
	Error   string       `json:"error,omitempty"`
	Options []taskOption `json:"options"`
	Assets  []taskAsset  `json:"assets"`
}

type tasksPageData struct {
	Title      string
	ReadOnly   bool
	Tasks      []taskSummary
	Status     string
	ProjectID  string
	Limit      int
	Page       int
	HasPrev    bool
	HasNext    bool
	PrevQuery  string
	NextQuery  string
	BannerText string
	Version    string
}

type taskDetailPageData struct {
	Title      string
	ReadOnly   bool
	Task       taskDetail
	BannerText string
	Version    string
}

// buildTasksQuery renders the /ui querystring for a given page while preserving
// the active status/project/limit filters, so pagination links keep the view.
func buildTasksQuery(status, projectID string, limit, page int) string {
	if page < 1 {
		page = 1
	}
	values := url.Values{}
	if status != "" {
		values.Set("status", status)
	}
	if projectID != "" {
		values.Set("projectID", projectID)
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	values.Set("page", strconv.Itoa(page))
	return "?" + values.Encode()
}

func toTaskSummary(job *meta.JobMetadata, now time.Time) taskSummary {
	statusCode, statusLabel, progress := mapStatus(job.JobStatus)

	summary := taskSummary{
		UUID:         job.WorkflowName,
		ProjectID:    job.ODMProjectID,
		Status:       strings.ToLower(strings.TrimSpace(job.JobStatus)),
		StatusLabel:  statusLabel,
		StatusCode:   statusCode,
		Progress:     progress,
		CreatedAt:    formatTime(job.CreatedAt),
		CreatedAtAgo: humanDuration(now.Sub(job.CreatedAt)),
	}
	if job.StartedAt != nil {
		summary.StartedAt = formatTime(*job.StartedAt)
	}
	if job.CompletedAt != nil {
		summary.CompletedAt = formatTime(*job.CompletedAt)
	}
	return summary
}

func toTaskDetail(job *meta.JobMetadata, now time.Time) taskDetail {
	summary := toTaskSummary(job, now)
	detail := taskDetail{
		Task:    summary,
		ReadS3:  job.ReadS3Path,
		WriteS3: job.WriteS3Path,
		Region:  job.S3Region,
		Options: parseOptions(job.ODMFlags),
		Assets:  completedAssets(job.WorkflowName, job.JobStatus, job.WriteS3Path),
	}
	if job.ErrorMessage != nil {
		detail.Error = *job.ErrorMessage
	}
	return detail
}

func mapStatus(status string) (code int, label string, progress int) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "claimed":
		return statusCodeQueued, "QUEUED", 0
	case "running":
		return statusCodeRunning, "RUNNING", 50
	case "completed":
		return statusCodeCompleted, "COMPLETED", 100
	case "failed":
		return statusCodeFailed, "FAILED", 0
	case "canceled":
		return statusCodeCanceled, "CANCELED", 0
	default:
		return statusCodeQueued, "QUEUED", 0
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func parseOptions(raw json.RawMessage) []taskOption {
	if len(raw) == 0 {
		return nil
	}
	var flags []string
	if err := json.Unmarshal(raw, &flags); err != nil {
		return nil
	}
	options := make([]taskOption, 0, len(flags))
	for _, flag := range flags {
		if !strings.HasPrefix(flag, "--") {
			// Bare value from old pair-format storage: attach to previous option.
			if len(options) > 0 {
				options[len(options)-1].Value = strings.TrimSpace(flag)
			}
			continue
		}
		normalized := strings.TrimSpace(strings.TrimPrefix(flag, "--"))
		if normalized == "" {
			continue
		}
		name := normalized
		value := "true"
		if key, rawValue, ok := strings.Cut(normalized, "="); ok {
			name = strings.TrimSpace(key)
			value = strings.TrimSpace(rawValue)
			if value == "" {
				value = "true"
			}
		}
		options = append(options, taskOption{Name: name, Value: value})
	}
	return options
}

// primaryAssetDef maps an output's display name to its download alias: the
// single path segment for /task/{uuid}/download/{asset} that the API resolves to
// the real object (synthesizing all.zip) at download time.
type primaryAssetDef struct {
	name  string
	alias string
}

var primaryAssetDefs = []primaryAssetDef{
	{name: "all.zip", alias: "all.zip"},
	{name: "orthophoto.tif", alias: "orthophoto"},
	{name: "dsm.tif", alias: "dsm"},
	{name: "dtm.tif", alias: "dtm"},
	{name: "point cloud", alias: "point_cloud"},
}

func taskDownloadURL(uuid, alias string) string {
	return path.Join("/task", url.PathEscape(uuid), "download", alias)
}

// completedAssets returns the download links for a finished task, or nil (JSON
// null) while it is still processing. The API validates each link at download
// time, so an output the run didn't produce simply 404s - no S3 probing here.
func completedAssets(uuid, status, writeS3Path string) []taskAsset {
	if strings.TrimSpace(writeS3Path) == "" || strings.ToLower(strings.TrimSpace(status)) != "completed" {
		return nil
	}
	assets := make([]taskAsset, 0, len(primaryAssetDefs))
	for _, def := range primaryAssetDefs {
		assets = append(assets, taskAsset{Name: def.name, URL: taskDownloadURL(uuid, def.alias)})
	}
	return assets
}
