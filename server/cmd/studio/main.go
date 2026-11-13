// Command studio is the Gapi Voice Agent Studio: one process that serves the
// web interface, its API and an MCP endpoint, and places the campaigns' calls.
//
//	STUDIO_DATABASE_URL  postgres://...            (required)
//	STUDIO_SECRET        seals tokens in the database, at least 16 characters; or
//	STUDIO_SECRET_FILE   a file that holds it, made with a random one on the first start
//	STUDIO_GAPI_TOKEN    a Gapi project token: the Studio opens straight into that project,
//	                     in the browser and for an assistant over MCP; without it the token
//	                     is pasted in the browser, and sent as a bearer token to /mcp
//	STUDIO_GAPI_URL      https://console.gapi.uz   (the Gapi API the calls go through)
//	STUDIO_LISTEN        127.0.0.1:8090
//	STUDIO_WEB_DIR       the built web interface (web/dist); empty = API only
//	STUDIO_SECURE_COOKIES=1  behind https
//
// `studio mcp` speaks MCP on standard input and output instead, for an
// assistant that starts its servers as commands; it needs STUDIO_GAPI_TOKEN
// and a `studio` running beside it on the same database, which is the one
// that follows the calls.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Islomov49/gapi-voice-studio/server/internal/analyze"
	"github.com/Islomov49/gapi-voice-studio/server/internal/api"
	"github.com/Islomov49/gapi-voice-studio/server/internal/db"
	"github.com/Islomov49/gapi-voice-studio/server/internal/dialer"
	"github.com/Islomov49/gapi-voice-studio/server/internal/gapi"
	"github.com/Islomov49/gapi-voice-studio/server/internal/seal"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	stdio := len(os.Args) > 1 && os.Args[1] == "mcp"
	// standard output is the protocol's in that mode: the log goes beside it
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	secret, err := secretFrom(os.Getenv("STUDIO_SECRET"), os.Getenv("STUDIO_SECRET_FILE"))
	if err != nil {
		slog.Error("STUDIO_SECRET_FILE", "err", err)
		os.Exit(2)
	}
	sealer, err := seal.New(secret)
	if err != nil {
		slog.Error("STUDIO_SECRET", "err", err)
		os.Exit(2)
	}
	pool, err := db.Open(ctx, os.Getenv("STUDIO_DATABASE_URL"))
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(2)
	}
	defer pool.Close()

	client := gapi.New(env("STUDIO_GAPI_URL", "https://console.gapi.uz"))
	engine := &dialer.Engine{DB: pool, Gapi: client, Seal: sealer, Now: time.Now, Analyzer: analyze.Baseline{}}
	server := &api.Server{DB: pool, Gapi: client, Seal: sealer, Now: time.Now, Engine: engine, SecureCookies: os.Getenv("STUDIO_SECURE_COOKIES") == "1",
		DefaultToken: os.Getenv("STUDIO_GAPI_TOKEN"), Version: version}
	if dir := os.Getenv("STUDIO_WEB_DIR"); dir != "" {
		server.Web = spa(dir)
	}
	if stdio {
		if err := server.ServeMCP(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
			slog.Error("mcp", "err", err)
			os.Exit(1)
		}
		return
	}

	go engine.Run(ctx, 2*time.Second)

	srv := &http.Server{Addr: env("STUDIO_LISTEN", "127.0.0.1:8090"), Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	slog.Info("studio listening", "addr", srv.Addr, "gapi", client.Base)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http", "err", err)
		os.Exit(1)
	}
}

// secretFrom is the sealing secret: given, or kept in a file that the first
// start fills with a random one.
func secretFrom(given, file string) (string, error) {
	if given != "" || file == "" {
		return given, nil
	}
	if data, err := os.ReadFile(file); err == nil && len(strings.TrimSpace(string(data))) >= 16 {
		return strings.TrimSpace(string(data)), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(raw)
	return secret, os.WriteFile(file, []byte(secret+"\n"), 0o600)
}

// spa serves the built interface: a file if there is one, index.html for
// every other path, since the interface routes in the browser.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
