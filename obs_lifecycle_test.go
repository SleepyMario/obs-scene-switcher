package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestOBSIdentityInsideControllerSandbox(t *testing.T) {
	for _, tc := range []struct {
		exe       string
		err       error
		comm, cmd string
		want      bool
	}{
		{"/usr/bin/obs", nil, "obs\n", "/usr/bin/obs\x00", true},
		{"", os.ErrPermission, "obs\n", "/usr/bin/obs\x00--scene\x00Starting Soon", true},
		{"", os.ErrPermission, "obs\n", "obs\x00", true},
		{"", os.ErrPermission, "other\n", "/usr/bin/obs\x00", false},
		{"", os.ErrPermission, "obs\n", "/tmp/obs\x00", false},
		{"/usr/bin/other", nil, "obs\n", "obs\x00", false},
		{"", os.ErrNotExist, "obs\n", "obs\x00", false},
	} {
		if identifiesOBS(tc.exe, tc.err, tc.comm, tc.cmd) != tc.want {
			t.Fatalf("identity mismatch: %+v", tc)
		}
	}
}

func lifecycleRequest(c *obsLifecycle, action string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://10.77.0.2:8798/api/obs/"+action, nil)
	r.SetPathValue("action", action)
	r.Header.Set("X-OBS-Control", "1")
	w := httptest.NewRecorder()
	c.action(w, r)
	return w
}

func outputRequest(c *obsLifecycle, kind, action string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://10.77.0.2:8798/api/outputs/"+kind+"/"+action, nil)
	r.SetPathValue("kind", kind)
	r.SetPathValue("action", action)
	r.Header.Set("X-OBS-Control", "1")
	w := httptest.NewRecorder()
	c.outputAction(w, r)
	return w
}

func idleLifecycle() *obsLifecycle {
	return &obsLifecycle{
		pids:   func() ([]int, error) { return nil, nil },
		launch: func(context.Context) error { return nil },
		close:  func(int) error { return nil },
		request: func(_ context.Context, kind string, _ any) (json.RawMessage, error) {
			if kind == "GetOutputList" {
				return json.RawMessage(`{"outputs":[]}`), nil
			}
			return json.RawMessage(`{"outputActive":false}`), nil
		},
	}
}

func TestOBSStartDoesNotTouchExistingSession(t *testing.T) {
	c := idleLifecycle()
	c.pids = func() ([]int, error) { return []int{123}, nil }
	c.launch = func(context.Context) error { t.Fatal("launched second OBS"); return nil }
	c.request = func(context.Context, string, any) (json.RawMessage, error) {
		t.Fatal("modified existing OBS")
		return nil, nil
	}
	if w := lifecycleRequest(c, "start"); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
}

func TestOBSConcurrentStartOnlyLaunchesOnce(t *testing.T) {
	c := idleLifecycle()
	launches := 0
	c.launch = func(context.Context) error { launches++; return nil }
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); lifecycleRequest(c, "start") }()
	}
	wg.Wait()
	if launches != 1 {
		t.Fatalf("launched %d times", launches)
	}
}

func TestOBSLaunchFailureCanBeRetried(t *testing.T) {
	c := idleLifecycle()
	c.launch = func(context.Context) error { return errors.New("no desktop") }
	if w := lifecycleRequest(c, "start"); w.Code != http.StatusConflict {
		t.Fatal(w.Code)
	}
	c.launch = func(context.Context) error { return nil }
	if w := lifecycleRequest(c, "start"); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
}

func TestOBSStopRequiresAllOutputsKnownIdle(t *testing.T) {
	for _, blocked := range []string{"GetStreamStatus", "GetRecordStatus", "GetOutputList", "GetVirtualCamStatus", "unavailable", "malformed"} {
		t.Run(blocked, func(t *testing.T) {
			c := idleLifecycle()
			c.pids = func() ([]int, error) { return []int{123}, nil }
			c.close = func(int) error { t.Fatal("closed OBS despite unknown/active output"); return nil }
			c.request = func(_ context.Context, kind string, _ any) (json.RawMessage, error) {
				if blocked == "unavailable" {
					return nil, errors.New("offline")
				}
				if blocked == "malformed" {
					return json.RawMessage(`{}`), nil
				}
				if kind == blocked {
					if kind == "GetOutputList" {
						return json.RawMessage(`{"outputs":[{"outputActive":false},{"outputActive":true}]}`), nil
					}
					return json.RawMessage(`{"outputActive":true}`), nil
				}
				if kind == "GetOutputList" {
					return json.RawMessage(`{"outputs":[]}`), nil
				}
				return json.RawMessage(`{"outputActive":false}`), nil
			}
			if w := lifecycleRequest(c, "stop"); w.Code != http.StatusConflict {
				t.Fatal(w.Code)
			}
		})
	}
}

func TestOBSStopOnlySignalsSingleIdleProcessOnce(t *testing.T) {
	c := idleLifecycle()
	c.pids = func() ([]int, error) { return []int{123}, nil }
	closed := 0
	c.close = func(pid int) error {
		if pid != 123 {
			t.Fatal(pid)
		}
		closed++
		return nil
	}
	for i := 0; i < 2; i++ {
		if w := lifecycleRequest(c, "stop"); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
	}
	if closed != 1 {
		t.Fatal(closed)
	}
	c = idleLifecycle()
	c.pids = func() ([]int, error) { return []int{123, 456}, nil }
	c.close = func(int) error { t.Fatal("closed ambiguous instance"); return nil }
	if w := lifecycleRequest(c, "stop"); w.Code != http.StatusConflict {
		t.Fatal(w.Code)
	}
}

func TestOBSActionRejectsForeignOriginAndForms(t *testing.T) {
	for _, tc := range []struct {
		origin, header string
		allowed        bool
	}{
		{"http://10.77.0.2:8798", "1", true}, {"", "1", true},
		{"https://untrusted.example", "1", false}, {"null", "1", false}, {"", "", false},
	} {
		r := httptest.NewRequest("POST", "http://10.77.0.2:8798/api/obs/start", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-OBS-Control", tc.header)
		if sameOriginAction(r) != tc.allowed {
			t.Fatalf("unexpected permission for %+v", tc)
		}
	}
}

func TestOBSOutputActionsRequireRunningOBSAndCurrentState(t *testing.T) {
	c := idleLifecycle()
	if w := outputRequest(c, "stream", "start"); w.Code != http.StatusConflict {
		t.Fatalf("closed OBS status=%d body=%s", w.Code, w.Body.String())
	}
	c.pids = func() ([]int, error) { return []int{123}, nil }
	var requests []string
	c.request = func(_ context.Context, kind string, _ any) (json.RawMessage, error) {
		requests = append(requests, kind)
		if kind == "GetStreamStatus" {
			return json.RawMessage(`{"outputActive":false}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	if w := outputRequest(c, "stream", "start"); w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(requests) != 2 || requests[0] != "GetStreamStatus" || requests[1] != "StartStream" {
		t.Fatalf("requests=%v", requests)
	}
}

func TestOBSOutputActionIsIdempotent(t *testing.T) {
	c := idleLifecycle()
	c.pids = func() ([]int, error) { return []int{123}, nil }
	c.request = func(_ context.Context, kind string, _ any) (json.RawMessage, error) {
		if kind != "GetRecordStatus" {
			t.Fatalf("unexpected mutation request %s", kind)
		}
		return json.RawMessage(`{"outputActive":true,"outputPaused":false}`), nil
	}
	if w := outputRequest(c, "record", "start"); w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestOBSOutputStatusReportsStreamAndRecording(t *testing.T) {
	c := idleLifecycle()
	c.pids = func() ([]int, error) { return []int{123}, nil }
	c.request = func(_ context.Context, kind string, _ any) (json.RawMessage, error) {
		if kind == "GetStreamStatus" {
			return json.RawMessage(`{"outputActive":true}`), nil
		}
		return json.RawMessage(`{"outputActive":true,"outputPaused":true}`), nil
	}
	r := httptest.NewRequest("GET", "http://10.77.0.2:8798/api/outputs", nil)
	w := httptest.NewRecorder()
	c.outputStatus(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"streaming":true`) || !strings.Contains(w.Body.String(), `"recording_paused":true`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPhoneLaunchRequiresExactStartingScene(t *testing.T) {
	for _, tc := range []struct {
		data  string
		valid bool
	}{
		{`{"name":"Main","scene_order":[{"name":"Starting Soon"}]}`, true},
		{`{"name":"Main","scene_order":[{"name":"Gaming - Capture Card"}]}`, false},
		{`{"name":"Other","scene_order":[{"name":"Starting Soon"}]}`, false},
		{`{`, false},
	} {
		if (validateStartingScene([]byte(tc.data)) == nil) != tc.valid {
			t.Fatal(tc.data)
		}
	}
}
