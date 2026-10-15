package backup

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

func (e *Engine) download(w http.ResponseWriter, r *http.Request) error {
	var job Job
	for _, item := range e.State().Jobs {
		if item.ID == r.URL.Query().Get("job") {
			job = item
		}
	}
	if job.Status != "succeeded" || job.Destination == "" {
		return errors.New("Completed restoration not found")
	}
	path := r.URL.Query().Get("path")
	if !fs.ValidPath(path) {
		return errors.New("Choose a restored file")
	}
	root, err := os.OpenRoot(job.Destination)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Choose a regular file")
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return nil
}
