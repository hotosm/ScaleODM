package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

const formCompatBodyLimit = 1 << 20

// pyodm posts these routes as form data; Huma only decodes JSON, so convert.
var formCompatRoutes = map[string]bool{
	"/task/cancel":  true,
	"/task/remove":  true,
	"/task/restart": true,
}

func withFormCompat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !formCompatRoutes[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			next.ServeHTTP(w, r)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, formCompatBodyLimit)
		if err := r.ParseForm(); err != nil {
			http.Error(w, `{"error":"Invalid form body"}`, http.StatusBadRequest)
			return
		}

		fields := make(map[string]string, len(r.PostForm))
		for key := range r.PostForm {
			fields[key] = r.PostForm.Get(key)
		}
		body, _ := json.Marshal(fields)

		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
