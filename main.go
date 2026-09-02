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
	"os/exec"
	"os/signal"
	"path/filepath"
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
	obsURL := flag.String("obs-url", "ws://10.77.0.2:4455", "OBS WebSocket URL")
	obsConfig := flag.String("obs-config", os.ExpandEnv("$HOME/.config/obs-studio/plugin_config/obs-websocket/config.json"), "OBS WebSocket config containing the local password")
	subtitleAPI := flag.String("subtitle-api", os.Getenv("STREAMCHAT_SUBTITLE_API_URL"), "Streamchat bot API base URL")
	subtitlePasswordEnv := flag.String("subtitle-password-env", "STREAMCHAT_BOT_GUI_PASSWORD", "environment variable containing the Streamchat bot GUI password")
	subtitleSource := flag.String("subtitle-source", os.Getenv("STREAMCHAT_SUBTITLE_SOURCE"), "stable PipeWire/Pulse microphone source")
	subtitleOriginal := flag.String("subtitle-original-output", os.ExpandEnv("$HOME/.cache/language-subtitles/original.txt"), "OBS original-language text file")
	subtitleEnglish := flag.String("subtitle-english-output", os.ExpandEnv("$HOME/.cache/language-subtitles/english.txt"), "OBS English text file")
	subtitleChinese := flag.String("subtitle-chinese-output", os.ExpandEnv("$HOME/.cache/language-subtitles/chinese.txt"), "OBS Simplified Chinese text file")
	subtitleCombined := flag.String("subtitle-combined-output", os.ExpandEnv("$HOME/.cache/language-subtitles/current.txt"), "existing combined OBS subtitle text file")
	subtitleModePath := flag.String("subtitle-mode-path", os.ExpandEnv("$HOME/.config/obs-scene-switcher/subtitle-mode"), "persistent local/remote subtitle selection")
	subtitleLocalCommand := flag.String("subtitle-local-command", os.ExpandEnv("$HOME/bin/subtitles"), "installed local subtitle controller")
	flag.Parse()

	if err := wireGuardOnly(*listen); err != nil {
		log.Fatal(err)
	}
	app := &application{obs: &obsClient{url: *obsURL, configPath: *obsConfig, timeout: 4 * time.Second}}
	app.subtitles = newSubtitleController(subtitleConfig{APIURL: *subtitleAPI, Password: os.Getenv(*subtitlePasswordEnv), Source: *subtitleSource, OriginalOutput: *subtitleOriginal, EnglishOutput: *subtitleEnglish, ChineseOutput: *subtitleChinese, CombinedOutput: *subtitleCombined, ModePath: *subtitleModePath, LocalCommand: *subtitleLocalCommand})
	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("GET /api/scenes", app.getScenes)
	mux.HandleFunc("POST /api/scenes/{scene}", app.switchScene)
	mux.HandleFunc("POST /api/irl/input/{state}", app.setIRLInputState)
	mux.HandleFunc("GET /api/subtitles", app.subtitleStatus)
	mux.HandleFunc("POST /api/subtitles/start", app.subtitleStart)
	mux.HandleFunc("POST /api/subtitles/stop", app.subtitleStop)
	mux.HandleFunc("POST /api/subtitles/mode/{mode}", app.subtitleMode)
	mux.HandleFunc("GET /api/platforms", app.platformStatus)
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
	APIURL, Password, Source, OriginalOutput, EnglishOutput, ChineseOutput, CombinedOutput string
	ModePath, LocalCommand                                                                 string
}

type subtitleController struct {
	cfg     subtitleConfig
	http    *http.Client
	mu      sync.Mutex
	state   string
	message string
	mode    string
	active  string
	started time.Time
	remote  map[string]any
	cancel  context.CancelFunc
	local   func(context.Context, string) error
}

type subtitleLease struct {
	State     string `json:"state"`
	WorkerURL string `json:"worker_url"`
	Token     string `json:"token"`
}

type platformIndicator struct {
	Live        bool   `json:"live"`
	Available   bool   `json:"available"`
	Title       string `json:"title,omitempty"`
	Category    string `json:"category,omitempty"`
	ViewerCount int    `json:"viewer_count,omitempty"`
}

func newSubtitleController(cfg subtitleConfig) *subtitleController {
	mode := "remote"
	if saved, err := os.ReadFile(cfg.ModePath); err == nil && validSubtitleMode(strings.TrimSpace(string(saved))) {
		mode = strings.TrimSpace(string(saved))
	}
	controller := &subtitleController{cfg: cfg, http: &http.Client{Timeout: 35 * time.Second}, state: "idle", message: "Subtitles are off", mode: mode}
	controller.local = func(ctx context.Context, action string) error {
		if cfg.LocalCommand == "" {
			return errors.New("local subtitle command is not configured")
		}
		if _, err := os.Stat(cfg.LocalCommand); err != nil {
			return fmt.Errorf("local subtitles are unavailable: %w", err)
		}
		output, err := exec.CommandContext(ctx, cfg.LocalCommand, action).CombinedOutput()
		if err != nil {
			return fmt.Errorf("local subtitles %s: %s: %w", action, strings.TrimSpace(string(output)), err)
		}
		return nil
	}
	return controller
}

func validSubtitleMode(mode string) bool { return mode == "local" || mode == "remote" }

func saveSubtitleMode(path, mode string) error {
	if path == "" || !validSubtitleMode(mode) {
		return errors.New("invalid subtitle mode")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".subtitle-mode-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.WriteString(mode + "\n")
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
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

func (s *subtitleController) platforms(ctx context.Context) (map[string]platformIndicator, error) {
	platforms := map[string]platformIndicator{"twitch": {}, "kick": {}, "youtube": {}}
	if s.cfg.APIURL == "" || s.cfg.Password == "" {
		return platforms, errors.New("Streamchat VPS status is not configured")
	}
	var state struct {
		Channels map[string]platformIndicator `json:"channels"`
		Channel  struct {
			Platform string `json:"platform"`
			platformIndicator
		} `json:"channel"`
	}
	if err := s.request(ctx, http.MethodGet, "/api/state", &state); err != nil {
		return platforms, err
	}
	for name := range platforms {
		if status, ok := state.Channels[name]; ok {
			platforms[name] = status
		}
	}
	// Keep one-version deployment compatibility with older Streamchat servers,
	// which exposed only the selected canonical channel.
	if len(state.Channels) == 0 && state.Channel.Platform != "" {
		if _, ok := platforms[state.Channel.Platform]; ok {
			platforms[state.Channel.Platform] = state.Channel.platformIndicator
		}
	}
	return platforms, nil
}

func (s *subtitleController) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"state": s.state, "message": s.message, "mode": s.mode, "active_mode": s.active, "started_at": s.started, "remote": s.remote, "remote_configured": s.cfg.APIURL != "" && s.cfg.Password != "", "source_configured": s.cfg.Source != "", "local_available": s.cfg.LocalCommand != ""}
}

func (s *subtitleController) start() error {
	s.mu.Lock()
	if s.state == "starting" || s.state == "running" || s.state == "stopping" {
		s.mu.Unlock()
		return nil
	}
	mode := s.mode
	if mode == "remote" && s.cfg.APIURL == "" {
		s.mu.Unlock()
		return errors.New("remote subtitles are not configured yet")
	}
	if mode == "remote" && s.cfg.Source == "" {
		s.mu.Unlock()
		return errors.New("subtitle microphone source is not configured")
	}
	if mode == "local" {
		s.state, s.message, s.active = "starting", "Starting the local subtitle engine", "local"
		s.mu.Unlock()
		if err := s.local(context.Background(), "start"); err != nil {
			s.mu.Lock()
			s.state, s.message, s.active, s.started = "error", err.Error(), "", time.Time{}
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		s.state, s.message, s.started = "running", "Local subtitles are live", time.Now().UTC()
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.state, s.message, s.active, s.started = cancel, "starting", "Requesting a temporary GPU worker", "remote", time.Now().UTC()
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
				s.cancel, s.remote, s.active, s.started = nil, nil, "", time.Time{}
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
	s.state, s.message, s.cancel, s.active, s.started = "error", err.Error(), nil, "", time.Time{}
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
	if s.state == "idle" {
		s.mu.Unlock()
		clearSubtitleOutputs(s.cfg)
		return nil
	}
	active := s.active
	cancel := s.cancel
	s.state, s.message = "stopping", "Stopping subtitles"
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var err error
	if active == "local" {
		err = s.local(ctx, "off")
	} else if active == "remote" {
		err = s.remoteStop(ctx)
	}
	s.mu.Lock()
	s.cancel, s.remote, s.active, s.started = nil, nil, "", time.Time{}
	if err != nil {
		s.state, s.message = "error", err.Error()
	} else {
		s.state, s.message = "idle", "Subtitles are off"
	}
	s.mu.Unlock()
	clearSubtitleOutputs(s.cfg)
	return err
}

func (s *subtitleController) setMode(ctx context.Context, mode string) error {
	if !validSubtitleMode(mode) {
		return errors.New("subtitle mode must be local or remote")
	}
	if err := s.stop(ctx); err != nil {
		return err
	}
	if err := saveSubtitleMode(s.cfg.ModePath, mode); err != nil {
		return err
	}
	s.mu.Lock()
	s.mode, s.state, s.message = mode, "idle", "Subtitles are off"
	s.mu.Unlock()
	return nil
}

func (a *application) subtitleStatus(w http.ResponseWriter, r *http.Request) {
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

func (a *application) subtitleMode(w http.ResponseWriter, r *http.Request) {
	if err := a.subtitles.setMode(r.Context(), strings.TrimSpace(r.PathValue("mode"))); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.subtitles.snapshot())
}

func (a *application) platformStatus(w http.ResponseWriter, r *http.Request) {
	platforms, err := a.subtitles.platforms(r.Context())
	response := map[string]any{"platforms": platforms}
	if err != nil {
		response["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, response)
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

func irlInputVisible(state string) (bool, error) {
	switch state {
	case "live":
		return true, nil
	case "offline":
		return false, nil
	default:
		return false, fmt.Errorf("invalid IRL input state %q", state)
	}
}

func (a *application) setIRLInputState(w http.ResponseWriter, r *http.Request) {
	visible, err := irlInputVisible(strings.TrimSpace(r.PathValue("state")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := a.obs.setSceneItemEnabled(r.Context(), "IRL - VPS", "VPS-MMTX", visible); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "IRL source visibility failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": r.PathValue("state"), "visible": visible})
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
