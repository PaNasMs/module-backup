package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/PaNasMs/module-sdk/auth"
	"io"
	"net/http"
	"strings"
	"time"
)

func response(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("Invalid request")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("Invalid request")
	}
	return nil
}
func (e *Engine) Handler(allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, authErr := auth.Lookup(r.URL.Query().Get("user"), allowed)
		if authErr != nil {
			response(w, 403, map[string]string{"error": "Administrator access required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		var value any
		var err error
		status := 200
		switch {
		case r.Method == "GET" && r.URL.Path == "/download":
			if err = e.download(w, r); err != nil {
				response(w, 400, map[string]string{"error": err.Error()})
			}
			return

		case r.Method == "GET" && r.URL.Path == "/state":
			value = e.State()
		case r.Method == "POST" && r.URL.Path == "/workspace":
			var body struct {
				Path string `json:"path"`
			}
			if err = decode(w, r, &body); err == nil {
				value, err = e.SetWorkspace(body.Path)
			}
		case r.Method == "GET" && r.URL.Path == "/containers":
			value, err = e.Containers(ctx)
		case r.Method == "POST" && r.URL.Path == "/repositories":
			var body RepositoryRequest
			if err = decode(w, r, &body); err == nil {
				body.Owner = identity.Username
				value, err = e.AddRepository(ctx, body)
			}
		case r.Method == "POST" && r.URL.Path == "/plans":
			var body Plan
			if err = decode(w, r, &body); err == nil {
				value, err = e.SavePlan(body)
			}
		case r.Method == "POST" && r.URL.Path == "/jobs":
			var body JobRequest
			if err = decode(w, r, &body); err == nil {
				value, err = e.Start(body)
				status = 202
			}
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/cancel/"):
			err = e.Cancel(strings.TrimPrefix(r.URL.Path, "/cancel/"))
			value = map[string]bool{"requested": err == nil}
		case r.Method == "GET" && r.URL.Path == "/snapshots":
			var raw []byte
			raw, err = e.query(ctx, r.URL.Query().Get("repository"), "snapshots")
			if err == nil {
				err = json.Unmarshal(raw, &value)
			}
		case r.Method == "GET" && r.URL.Path == "/files":
			snapshot := r.URL.Query().Get("snapshot")
			if !snapshotID(snapshot) {
				err = errors.New("Invalid snapshot")
				break
			}
			path := r.URL.Query().Get("path")
			if path == "" {
				path = "/"
			}
			if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") {
				err = errors.New("Invalid snapshot path")
				break
			}
			var raw []byte
			raw, err = e.query(ctx, r.URL.Query().Get("repository"), "ls", snapshot, "--", path)
			if err == nil {
				nodes := []json.RawMessage{}
				for _, line := range strings.Split(string(raw), "\n") {
					var node struct {
						Type string `json:"type"`
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(line), &node) == nil && node.Path != "" {
						nodes = append(nodes, json.RawMessage(line))
					}
				}
				value = nodes
			}
		default:
			response(w, 404, map[string]string{"error": "Not found"})
			return
		}
		if err != nil {
			response(w, 400, map[string]string{"error": err.Error()})
			return
		}
		response(w, status, value)
	})
}
