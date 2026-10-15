package backup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/PaNasMs/module-sdk/external"
)

func cloudPath(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 512 && path.Clean(s) == s && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\x00\r\n:\\")
}
func token(ctx context.Context, r Repository) (external.Access, error) {
	access, err := external.New().Token(ctx, r.Grant, r.Owner)
	if err != nil {
		return access, err
	}
	expected := map[string]string{"drive": "https://www.googleapis.com/auth/drive", "dropbox": "account_info.read files.metadata.read files.content.read files.content.write", "github": "repo"}[r.Provider]
	if expected == "" || access.Scope != expected {
		return access, errors.New("Reconnect this repository with the required provider permissions")
	}
	return access, nil
}
func (e *Engine) cloudConfig(ctx context.Context, r Repository) (string, func(), error) {
	access, err := token(ctx, r)
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	secretBytes := make([]byte, 32)
	if _, err = rand.Read(secretBytes); err != nil {
		listener.Close()
		return "", nil, err
	}
	secret := hex.EncodeToString(secretBytes)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.Body = http.MaxBytesReader(w, req.Body, 4096)
		if req.Method != "POST" || req.ParseForm() != nil || req.Form.Get("grant_type") != "refresh_token" || subtle.ConstantTimeCompare([]byte(req.Form.Get("refresh_token")), []byte(secret)) != 1 {
			http.Error(w, "Forbidden", 403)
			return
		}
		call, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		next, err := token(call, r)
		if err != nil {
			http.Error(w, "Authorization unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": next.AccessToken, "token_type": "Bearer", "expires_in": int(time.Until(next.ExpiresAt).Seconds()), "refresh_token": secret})
	})
	file, err := os.CreateTemp(e.root, ".cloud-")
	if err != nil {
		listener.Close()
		return "", nil, err
	}
	cleanup := func() { _ = server.Close(); _ = os.Remove(file.Name()) }
	b, _ := json.Marshal(map[string]any{"access_token": access.AccessToken, "token_type": "Bearer", "expiry": access.ExpiresAt, "refresh_token": secret})
	_, err = file.WriteString("[cloud]\ntype = " + r.Provider + "\ntoken_url = http://" + listener.Addr().String() + "/\ntoken = " + string(b) + "\n")
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		listener.Close()
		cleanup()
		return "", nil, err
	}
	go server.Serve(listener)
	return file.Name(), cleanup, nil
}
