package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const phoneOBSUnit = "obs-phone.service"

type obsLifecycle struct {
	mu        sync.Mutex
	pids      func() ([]int, error)
	launch    func(context.Context) error
	close     func(int) error
	request   func(context.Context, string, any) (json.RawMessage, error)
	lastStart time.Time
	lastStop  time.Time
}

func newOBSLifecycle(obs *obsClient) *obsLifecycle {
	return &obsLifecycle{
		pids:    localOBSPIDs,
		request: obs.request,
		launch: func(ctx context.Context) error {
			// The user manager launches OBS outside this controller's read-only
			// filesystem sandbox, with the logged-in desktop environment.
			if err := exec.CommandContext(ctx, "systemctl", "--user", "start", phoneOBSUnit).Run(); err != nil {
				return errors.New("could not launch OBS; check the desktop session and obs-phone.service journal")
			}
			return nil
		},
		close: func(pid int) error {
			process, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			defer process.Release()
			// Revalidate after obtaining the process handle. SIGINT uses OBS's
			// normal save-and-close path; never escalate to a forced kill.
			pids, err := localOBSPIDs()
			if err != nil {
				return err
			}
			for _, current := range pids {
				if current == pid {
					return process.Signal(os.Interrupt)
				}
			}
			return errors.New("OBS process changed; refresh before trying again")
		},
	}
}

func localOBSPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Getuid() {
			continue
		}
		executable, err := os.Readlink(filepath.Join(path, "exe"))
		comm, _ := os.ReadFile(filepath.Join(path, "comm"))
		cmdline, _ := os.ReadFile(filepath.Join(path, "cmdline"))
		if identifiesOBS(executable, err, string(comm), string(cmdline)) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func identifiesOBS(executable string, exeErr error, comm, cmdline string) bool {
	if exeErr == nil {
		return strings.TrimSuffix(executable, " (deleted)") == "/usr/bin/obs"
	}
	// The controller's existing sandbox can deny /proc/PID/exe for a GUI
	// process outside its mount namespace. UID was already checked above;
	// require BOTH OBS's process name and exact executable argv[0] here.
	if !errors.Is(exeErr, os.ErrPermission) {
		return false
	}
	argv0 := strings.SplitN(cmdline, "\x00", 2)[0]
	return strings.TrimSpace(comm) == "obs" && (argv0 == "/usr/bin/obs" || argv0 == "obs")
}

func checkOBSLaunch() error {
	pids, err := localOBSPIDs()
	if err != nil {
		return err
	}
	if len(pids) != 0 {
		return errors.New("OBS is already open; leaving its scene unchanged")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(home, ".config/obs-studio/basic/scenes/Main.json"))
	if err != nil {
		return fmt.Errorf("read Main scene collection: %w", err)
	}
	return validateStartingScene(data)
}

func validateStartingScene(data []byte) error {
	var collection struct {
		Name   string `json:"name"`
		Scenes []struct {
			Name string `json:"name"`
		} `json:"scene_order"`
	}
	if err := json.Unmarshal(data, &collection); err != nil {
		return err
	}
	if collection.Name == "Main" {
		for _, scene := range collection.Scenes {
			if scene.Name == "Starting Soon" {
				return nil
			}
		}
	}
	return errors.New("Main collection must contain Starting Soon before phone launch")
}

func (c *obsLifecycle) status(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pids, err := c.pids()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cannot check OBS process"})
		return
	}
	state, message := "stopped", "OBS is closed. Opens in Starting Soon."
	if len(pids) > 0 {
		state, message = "running", "OBS is open."
		if time.Since(c.lastStop) < 30*time.Second {
			state, message = "stopping", "Closing OBS…"
		}
	} else if time.Since(c.lastStart) < 15*time.Second {
		state, message = "starting", "Opening OBS in Starting Soon…"
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "message": message})
}

func sameOriginAction(r *http.Request) bool {
	// Reject cross-site forms/fetches, while allowing same-origin phone JS and
	// explicit local API callers. A custom header also forces CORS preflight.
	if r.Header.Get("X-OBS-Control") != "1" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
	}
	return true
}

func (c *obsLifecycle) action(w http.ResponseWriter, r *http.Request) {
	if !sameOriginAction(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "use the phone controller to start or stop OBS"})
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	pids, err := c.pids()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cannot check OBS process"})
		return
	}
	message := ""
	switch r.PathValue("action") {
	case "start":
		if len(pids) > 0 {
			message = "OBS is already open; its current scene was left unchanged."
		} else if time.Since(c.lastStart) < 15*time.Second {
			message = "OBS is already starting."
		} else if err = c.launch(ctx); err == nil {
			c.lastStart, c.lastStop = time.Now(), time.Time{}
			message = "Opening OBS in Starting Soon…"
		}
	case "stop":
		if len(pids) == 0 {
			message = "OBS is already closed."
		} else if len(pids) != 1 {
			err = errors.New("multiple OBS instances found; close the intended one on the laptop")
		} else if time.Since(c.lastStop) < 30*time.Second {
			message = "OBS is already closing."
		} else if err = c.checkIdle(ctx); err == nil {
			err = c.close(pids[0])
			if err == nil {
				c.lastStop, c.lastStart = time.Now(), time.Time{}
				message = "Asked OBS to save and close."
			}
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown OBS action"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message})
}

func (c *obsLifecycle) checkIdle(ctx context.Context) error {
	for _, kind := range []string{"GetStreamStatus", "GetRecordStatus", "GetVirtualCamStatus"} {
		data, err := c.request(ctx, kind, nil)
		if err != nil {
			return errors.New("cannot verify OBS is idle; stop refused")
		}
		var state struct {
			Active *bool `json:"outputActive"`
		}
		if json.Unmarshal(data, &state) != nil || state.Active == nil {
			return errors.New("unknown OBS output state; stop refused")
		}
		if *state.Active {
			return errors.New("OBS has an active stream, recording, replay buffer or virtual camera; stop those first")
		}
	}
	// Enumerating outputs also covers replay buffers and plugin outputs.
	// Asking GetReplayBufferStatus directly fails when it is not configured.
	data, err := c.request(ctx, "GetOutputList", nil)
	if err != nil {
		return errors.New("cannot verify all OBS outputs are idle; stop refused")
	}
	var list struct {
		Outputs *[]struct {
			Active *bool `json:"outputActive"`
		} `json:"outputs"`
	}
	if json.Unmarshal(data, &list) != nil || list.Outputs == nil {
		return errors.New("unknown OBS output list; stop refused")
	}
	for _, output := range *list.Outputs {
		if output.Active == nil {
			return errors.New("unknown OBS output state; stop refused")
		}
		if *output.Active {
			return errors.New("OBS has an active output; stop it before closing")
		}
	}
	return nil
}
