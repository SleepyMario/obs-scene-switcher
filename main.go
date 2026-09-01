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
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type application struct {
	obs       *obsClient
	subtitles *subtitleController
}

func main() {
	listen := flag.String("listen", "10.77.0.2:8798", "WireGuard address to serve")
	obsURL := flag.String("obs-url", "ws://127.0.0.1:4455", "OBS WebSocket URL")
	obsConfig := flag.String("obs-config", os.ExpandEnv("$HOME/.config/obs-studio/plugin_config/obs-websocket/config.json"), "OBS WebSocket config containing the local password")
	subtitleAPI := flag.String("subtitle-api", os.Getenv("STREAMCHAT_SUBTITLE_API_URL"), "Streamchat bot API base URL")
	subtitlePasswordEnv := flag.String("subtitle-password-env", "STREAMCHAT_BOT_GUI_PASSWORD", "environment variable containing the Streamchat bot GUI password")
	subtitleSource := flag.String("subtitle-source", os.Getenv("STREAMCHAT_SUBTITLE_SOURCE"), "stable PipeWire/Pulse microphone source")
	subtitleOriginal := flag.String("subtitle-original-output", os.ExpandEnv("$HOME/.cache/language-subtitles/original.txt"), "OBS original-language text file")
	subtitleEnglish := flag.String("subtitle-english-output", os.ExpandEnv("$HOME/.cache/language-subtitles/english.txt"), "OBS English text file")
	subtitleCombined := flag.String("subtitle-combined-output", os.ExpandEnv("$HOME/.cache/language-subtitles/current.txt"), "existing two-line OBS subtitle text file")
	flag.Parse()

	if err := wireGuardOnly(*listen); err != nil {
		log.Fatal(err)
	}
	app := &application{obs: &obsClient{url: *obsURL, configPath: *obsConfig, timeout: 4 * time.Second}}
	if *subtitleAPI != "" {
		app.subtitles = newSubtitleController(subtitleConfig{APIURL: *subtitleAPI, Password: os.Getenv(*subtitlePasswordEnv), Source: *subtitleSource, OriginalOutput: *subtitleOriginal, EnglishOutput: *subtitleEnglish, CombinedOutput: *subtitleCombined})
	}
	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("GET /api/scenes", app.getScenes)
	mux.HandleFunc("POST /api/scenes/{scene}", app.switchScene)
	mux.HandleFunc("GET /api/subtitles", app.subtitleStatus)
	mux.HandleFunc("POST /api/subtitles/start", app.subtitleStart)
	mux.HandleFunc("POST /api/subtitles/stop", app.subtitleStop)
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
		if app.subtitles != nil {
			_ = app.subtitles.stop(context.Background())
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("OBS scene switcher listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type subtitleConfig struct {
	APIURL, Password, Source, OriginalOutput, EnglishOutput, CombinedOutput string
}

type subtitleController struct {
	cfg     subtitleConfig
	http    *http.Client
	mu      sync.Mutex
	state   string
	message string
	remote  map[string]any
	cancel  context.CancelFunc
}

type subtitleLease struct {
	State     string `json:"state"`
	WorkerURL string `json:"worker_url"`
	Token     string `json:"token"`
}

func newSubtitleController(cfg subtitleConfig) *subtitleController {
	return &subtitleController{cfg: cfg, http: &http.Client{Timeout: 35 * time.Second}, state: "idle"}
}

func (s *subtitleController) request(ctx context.Context, method, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.APIURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if s.cfg.Password != "" {
		req.SetBasicAuth("streamchat", s.cfg.Password)
	}
	response, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem map[string]any
		_ = json.NewDecoder(response.Body).Decode(&problem)
		return fmt.Errorf("%v", problem["error"])
	}
	if result != nil {
		return json.NewDecoder(response.Body).Decode(result)
	}
	return nil
}

func (s *subtitleController) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"state": s.state, "message": s.message, "remote": s.remote, "source_configured": s.cfg.Source != ""}
}

func (s *subtitleController) start() error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	if s.cfg.Source == "" {
		s.mu.Unlock()
		return errors.New("subtitle microphone source is not configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.state, s.message = cancel, "starting", "Requesting a temporary GPU worker"
	s.mu.Unlock()
	go s.run(ctx)
	return nil
}

func (s *subtitleController) run(ctx context.Context) {
	var lease subtitleLease
	if err := s.request(ctx, http.MethodPost, "/api/subtitles/start", &lease); err != nil {
		s.fail(err)
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for lease.State != "ready" {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var remote map[string]any
			if err := s.request(ctx, http.MethodPost, "/api/subtitles/heartbeat", &remote); err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			s.remote = remote
			if state, ok := remote["state"].(string); ok {
				lease.State = state
			}
			s.message = "GPU model is loading"
			s.mu.Unlock()
		}
	}
	if lease.Token == "" || lease.WorkerURL == "" {
		var refreshed subtitleLease
		if err := s.request(ctx, http.MethodGet, "/api/subtitles/lease", &refreshed); err != nil {
			s.fail(err)
			return
		}
		lease.WorkerURL, lease.Token = refreshed.WorkerURL, refreshed.Token
	}
	s.mu.Lock()
	s.state, s.message = "running", "Microphone subtitles are live"
	s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- runSubtitleSender(ctx, s.cfg, lease.WorkerURL, lease.Token) }()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				s.fail(fmt.Errorf("subtitle sender stopped: %w", err))
				return
			}
			if ctx.Err() == nil {
				stopErr := s.remoteStop(context.Background())
				s.mu.Lock()
				s.cancel, s.remote = nil, nil
				if stopErr != nil {
					s.state, s.message = "error", stopErr.Error()
				} else {
					s.state, s.message = "idle", "GPU subtitle worker is off"
				}
				s.mu.Unlock()
				clearSubtitleOutputs(s.cfg)
			}
			return
		case <-heartbeat.C:
			var remote map[string]any
			if err := s.request(ctx, http.MethodPost, "/api/subtitles/heartbeat", &remote); err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			s.remote = remote
			s.mu.Unlock()
		}
	}
}

func (s *subtitleController) fail(err error) {
	s.mu.Lock()
	s.state, s.message, s.cancel = "error", err.Error(), nil
	s.mu.Unlock()
	clearSubtitleOutputs(s.cfg)
	_ = s.remoteStop(context.Background())
}

func (s *subtitleController) remoteStop(ctx context.Context) error {
	stopCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	return s.request(stopCtx, http.MethodPost, "/api/subtitles/stop", nil)
}

func (s *subtitleController) stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.state, s.message = "stopping", "Stopping microphone and deleting GPU worker"
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	err := s.remoteStop(ctx)
	s.mu.Lock()
	s.cancel, s.remote = nil, nil
	if err != nil {
		s.state, s.message = "error", err.Error()
	} else {
		s.state, s.message = "idle", "GPU subtitle worker is off"
	}
	s.mu.Unlock()
	clearSubtitleOutputs(s.cfg)
	return err
}

func (a *application) subtitleStatus(w http.ResponseWriter, r *http.Request) {
	if a.subtitles == nil {
		writeJSON(w, 200, map[string]any{"state": "disabled", "message": "GPU subtitles are not configured"})
		return
	}
	writeJSON(w, 200, a.subtitles.snapshot())
}
func (a *application) subtitleStart(w http.ResponseWriter, r *http.Request) {
	if a.subtitles == nil {
		writeJSON(w, 409, map[string]any{"error": "GPU subtitles are not configured"})
		return
	}
	if err := a.subtitles.start(); err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 202, a.subtitles.snapshot())
}
func (a *application) subtitleStop(w http.ResponseWriter, r *http.Request) {
	if a.subtitles == nil {
		writeJSON(w, 200, map[string]any{"state": "disabled"})
		return
	}
	if err := a.subtitles.stop(r.Context()); err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, a.subtitles.snapshot())
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
