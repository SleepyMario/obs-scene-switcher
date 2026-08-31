package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type application struct {
	obs *obsClient
}

func main() {
	listen := flag.String("listen", "10.77.0.2:8798", "WireGuard address to serve")
	obsURL := flag.String("obs-url", "ws://127.0.0.1:4455", "OBS WebSocket URL")
	obsConfig := flag.String("obs-config", os.ExpandEnv("$HOME/.config/obs-studio/plugin_config/obs-websocket/config.json"), "OBS WebSocket config containing the local password")
	flag.Parse()

	if err := wireGuardOnly(*listen); err != nil {
		log.Fatal(err)
	}
	app := &application{obs: &obsClient{url: *obsURL, configPath: *obsConfig, timeout: 4 * time.Second}}
	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("GET /api/scenes", app.getScenes)
	mux.HandleFunc("POST /api/scenes/{scene}", app.switchScene)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	server := &http.Server{
		Addr:              *listen,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("OBS scene switcher listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func wireGuardOnly(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsPrivate() || !strings.HasPrefix(host, "10.77.0.") {
		return fmt.Errorf("refusing non-WireGuard listen address %q; expected 10.77.0.x", host)
	}
	return nil
}

func (a *application) getScenes(w http.ResponseWriter, r *http.Request) {
	scenes, current, err := a.obs.scenes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "OBS is unavailable", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenes": scenes, "current": current})
}

func (a *application) switchScene(w http.ResponseWriter, r *http.Request) {
	scene := strings.TrimSpace(r.PathValue("scene"))
	if scene == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scene name is required"})
		return
	}
	if err := a.obs.switchScene(r.Context(), scene); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "scene switch failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "current": scene})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
