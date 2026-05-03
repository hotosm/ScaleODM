package api

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"time"
)

const taskNewErrorLogBodyLimit = 4096

func withTaskNewErrorLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/task/new" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &responseLogRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
			bodyLimit:      taskNewErrorLogBodyLimit,
		}

		next.ServeHTTP(rec, r)

		if rec.statusCode < http.StatusBadRequest {
			return
		}

		responseBody := strings.TrimSpace(rec.body.String())
		if rec.bodyTruncated {
			responseBody += "... [truncated]"
		}
		if responseBody == "" {
			log.Printf("POST /task/new: request failed status=%d duration=%s remote=%q", rec.statusCode, time.Since(start), r.RemoteAddr)
			return
		}
		log.Printf("POST /task/new: request failed status=%d duration=%s remote=%q response=%q", rec.statusCode, time.Since(start), r.RemoteAddr, responseBody)
	})
}

type responseLogRecorder struct {
	http.ResponseWriter
	statusCode    int
	bodyLimit     int
	body          bytes.Buffer
	bodyTruncated bool
	wroteHeader   bool
}

func (r *responseLogRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseLogRecorder) Write(body []byte) (int, error) {
	r.wroteHeader = true
	if r.body.Len() < r.bodyLimit {
		remaining := r.bodyLimit - r.body.Len()
		if len(body) > remaining {
			_, _ = r.body.Write(body[:remaining])
			r.bodyTruncated = true
		} else {
			_, _ = r.body.Write(body)
		}
	} else if len(body) > 0 {
		r.bodyTruncated = true
	}

	return r.ResponseWriter.Write(body)
}

func (r *responseLogRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
