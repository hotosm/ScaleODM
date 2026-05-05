package ui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/hotosm/scaleodm/app/meta"
	"github.com/hotosm/scaleodm/app/s3"
	"github.com/hotosm/scaleodm/app/workflows"
)

const (
	defaultLimit = 25
	maxLimit     = 200
)

//go:embed templates/*.tmpl static/*
var embeddedFiles embed.FS

type Handler struct {
	metadataStore *meta.Store
	workflow      workflows.WorkflowClient
	readonly      bool
	version       string
	templates     *template.Template
	static        http.Handler
}

func NewHandler(metadataStore *meta.Store, workflow workflows.WorkflowClient, readonly bool, version string) (*Handler, error) {
	templates, err := template.ParseFS(
		embeddedFiles,
		"templates/layout.html.tmpl",
		"templates/tasks.html.tmpl",
		"templates/task_detail.html.tmpl",
	)
	if err != nil {
		return nil, err
	}

	staticFS, err := fs.Sub(embeddedFiles, "static")
	if err != nil {
		return nil, err
	}

	return &Handler{
		metadataStore: metadataStore,
		workflow:      workflow,
		readonly:      readonly,
		version:       version,
		templates:     templates,
		static:        http.FileServer(http.FS(staticFS)),
	}, nil
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /ui", h.withSecurityHeaders(http.HandlerFunc(h.handleTasksPage)))
	mux.Handle("GET /ui/tasks/{uuid}", h.withSecurityHeaders(http.HandlerFunc(h.handleTaskDetailPage)))

	mux.Handle("GET /ui/api/tasks", h.withSecurityHeaders(http.HandlerFunc(h.handleTasksJSON)))
	mux.Handle("GET /ui/api/tasks/{uuid}", h.withSecurityHeaders(http.HandlerFunc(h.handleTaskDetailJSON)))
	mux.Handle("GET /ui/api/tasks/{uuid}/output", h.withSecurityHeaders(http.HandlerFunc(h.handleTaskOutputJSON)))

	mux.Handle("GET /ui/static/", h.withSecurityHeaders(http.StripPrefix("/ui/static/", h.static)))
}

func (h *Handler) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) handleTasksPage(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	jobs, err := h.metadataStore.ListJobs(r.Context(), status, projectID, limit)
	if err != nil {
		http.Error(w, "failed to list jobs", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	tasks := make([]taskSummary, 0, len(jobs))
	for _, job := range jobs {
		tasks = append(tasks, toTaskSummary(job, now))
	}

	data := tasksPageData{
		Title:      "ScaleODM UI",
		ReadOnly:   h.readonly,
		Tasks:      tasks,
		Status:     status,
		ProjectID:  projectID,
		Limit:      limit,
		BannerText: "No authentication is enabled. Use this UI only on trusted internal networks.",
		Version:    h.version,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "tasks", data); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
	}
}

func (h *Handler) reconcileJobFromArgo(ctx context.Context, job *meta.JobMetadata) {
	wf, err := h.workflow.GetWorkflow(ctx, job.WorkflowName)
	if err != nil {
		return
	}
	liveStatus := meta.MapArgoPhaseToJobStatus(string(wf.Status.Phase))
	if strings.ToLower(strings.TrimSpace(job.JobStatus)) != liveStatus {
		if updateErr := h.metadataStore.UpdateJobStatus(ctx, job.WorkflowName, liveStatus, nil); updateErr != nil {
			log.Printf("UI reconcile: failed to sync status for %s: %v", job.WorkflowName, updateErr)
		}
		job.JobStatus = liveStatus
	}
}

func (h *Handler) handleTaskDetailPage(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimSpace(r.PathValue("uuid"))
	if uuid == "" {
		http.NotFound(w, r)
		return
	}

	job, err := h.metadataStore.GetJob(r.Context(), uuid)
	if err != nil {
		http.Error(w, "failed to load task", http.StatusInternalServerError)
		return
	}
	if job == nil {
		http.NotFound(w, r)
		return
	}

	h.reconcileJobFromArgo(r.Context(), job)

	data := taskDetailPageData{
		Title:      "Task " + uuid,
		ReadOnly:   h.readonly,
		Task:       toTaskDetail(job, time.Now().UTC()),
		BannerText: "No authentication is enabled. Use this UI only on trusted internal networks.",
		Version:    h.version,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "task_detail", data); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
	}
}

func (h *Handler) handleTasksJSON(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	jobs, err := h.metadataStore.ListJobs(r.Context(), status, projectID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list jobs"})
		return
	}

	now := time.Now().UTC()
	tasks := make([]taskSummary, 0, len(jobs))
	for _, job := range jobs {
		tasks = append(tasks, toTaskSummary(job, now))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tasks": tasks,
		"count": len(tasks),
	})
}

func (h *Handler) handleTaskDetailJSON(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimSpace(r.PathValue("uuid"))
	if uuid == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	job, err := h.metadataStore.GetJob(r.Context(), uuid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load task"})
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	h.reconcileJobFromArgo(r.Context(), job)

	writeJSON(w, http.StatusOK, toTaskDetail(job, time.Now().UTC()))
}

func (h *Handler) handleTaskOutputJSON(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimSpace(r.PathValue("uuid"))
	if uuid == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	line, err := parseLine(r.URL.Query().Get("line"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	job, err := h.metadataStore.GetJob(r.Context(), uuid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load task"})
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	output, err := h.loadTaskOutput(r.Context(), job)
	if err != nil {
		log.Printf("GET /ui/api/tasks/%s/output: failed to load logs: %v", uuid, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load task output"})
		return
	}

	lines := strings.Split(output, "\n")
	if line < len(lines) {
		lines = lines[line:]
	} else {
		lines = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uuid":   uuid,
		"line":   line,
		"output": lines,
		"text":   strings.Join(lines, "\n"),
	})
}

func (h *Handler) loadTaskOutput(ctx context.Context, job *meta.JobMetadata) (string, error) {
	if h.workflow == nil {
		return "", errors.New("workflow client not initialized")
	}

	if strings.TrimSpace(job.WriteS3Path) != "" {
		client, err := taskS3Client(job.Metadata)
		if err != nil {
			return "", err
		}

		if archivedLogs, err := s3.GetWorkflowLogsFromS3(ctx, client, job.WriteS3Path); err == nil {
			return archivedLogs, nil
		}

		var builder strings.Builder
		if err := h.workflow.GetWorkflowLogsWithS3Path(ctx, job.WorkflowName, job.WriteS3Path, client, &builder); err != nil {
			return "", err
		}
		return builder.String(), nil
	}

	var builder strings.Builder
	if err := h.workflow.GetWorkflowLogs(ctx, job.WorkflowName, &builder); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func taskS3Client(metadataJSON []byte) (*minio.Client, error) {
	metaMap := parseMetadataMap(metadataJSON)
	endpoint, _ := metaMap["s3_endpoint"].(string)
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return s3.GetS3Client(), nil
	}
	normalized, err := s3.NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return s3.GetS3ClientForEndpoint(normalized)
}

func parseMetadataMap(metadataJSON []byte) map[string]interface{} {
	if len(metadataJSON) == 0 {
		return map[string]interface{}{}
	}
	metaMap := map[string]interface{}{}
	if err := json.Unmarshal(metadataJSON, &metaMap); err != nil {
		return map[string]interface{}{}
	}
	return metaMap
}

func parseLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("invalid limit")
	}
	if value <= 0 {
		return 0, errors.New("limit must be greater than 0")
	}
	if value > maxLimit {
		return maxLimit, nil
	}
	return value, nil
}

func parseLine(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("invalid line")
	}
	if value < 0 {
		return 0, errors.New("line must be 0 or greater")
	}
	return value, nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}
