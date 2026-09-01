package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWireGuardOnly(t *testing.T) {
	for _, address := range []string{"10.77.0.2:8798", "10.77.0.1:9000"} {
		if err := wireGuardOnly(address); err != nil {
			t.Fatalf("expected %s to pass: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8798", "192.168.0.124:8798", "127.0.0.1:8798"} {
		if err := wireGuardOnly(address); err == nil {
			t.Fatalf("expected %s to be rejected", address)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestPlatformStatusUsesAuthenticatedStreamchatState(t *testing.T) {
	controller := newSubtitleController(subtitleConfig{APIURL: "https://streamchat.invalid", Password: "secret"})
	controller.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_, password, ok := request.BasicAuth()
		if request.URL.Path != "/api/state" || !ok || password != "secret" {
			t.Fatalf("unexpected request: %s auth=%v", request.URL, ok)
		}
		body := `{"stream":{"channels":{"twitch":{"live":true,"available":true,"viewer_count":4},"kick":{"live":false,"available":true},"youtube":{"live":true,"available":true}}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}
	platforms, err := controller.platforms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !platforms["twitch"].Live || platforms["kick"].Live || !platforms["youtube"].Available || platforms["twitch"].ViewerCount != 4 {
		t.Fatalf("platforms=%+v", platforms)
	}
}

func TestSubtitleModeIsPrivateAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller", "subtitle-mode")
	if err := saveSubtitleMode(path, "local"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "local\n" {
		t.Fatalf("mode=%q err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode file permissions=%v err=%v", info.Mode().Perm(), err)
	}
	controller := newSubtitleController(subtitleConfig{ModePath: path})
	if controller.mode != "local" {
		t.Fatalf("loaded mode=%q", controller.mode)
	}
}

func TestLocalModeUsesInstalledControllerAndStopsBeforeModeChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subtitle-mode")
	controller := newSubtitleController(subtitleConfig{ModePath: path, LocalCommand: "/unused"})
	controller.mode = "local"
	var actions []string
	controller.local = func(_ context.Context, action string) error { actions = append(actions, action); return nil }
	if err := controller.start(); err != nil {
		t.Fatal(err)
	}
	if controller.snapshot()["state"] != "running" {
		t.Fatalf("snapshot=%v", controller.snapshot())
	}
	if started, ok := controller.snapshot()["started_at"].(time.Time); !ok || started.IsZero() {
		t.Fatalf("missing running uptime origin: %v", controller.snapshot())
	}
	if err := controller.setMode(context.Background(), "remote"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"start", "off"}) {
		t.Fatalf("actions=%v", actions)
	}
	if controller.snapshot()["mode"] != "remote" || controller.snapshot()["state"] != "idle" {
		t.Fatalf("snapshot=%v", controller.snapshot())
	}
}

func TestSubtitleOutputIsPrivateAndAtomicallyReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions", "original.txt")
	if err := atomicText(path, "你好"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "你好\n" {
		t.Fatalf("caption=%q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if err := atomicText(path, ""); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("caption was not cleared: %q %v", data, err)
	}
}

func TestCombineCaptionSuppressesDuplicateLanguageLines(t *testing.T) {
	tests := []struct {
		name, original, english, chinese, want string
	}{
		{"three languages", "Goedemorgen", "Good morning", "早上好", "Goedemorgen\nGood morning\n早上好"},
		{"English original", "Good morning", "", "早上好", "Good morning\n早上好"},
		{"Chinese original", "早上好", "Good morning", "", "早上好\nGood morning"},
		{"defensive duplicate", "Good morning", "Good morning", "早上好", "Good morning\n早上好"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := combineCaption(test.original, test.english, test.chinese); got != test.want {
				t.Fatalf("caption=%q want=%q", got, test.want)
			}
		})
	}
}

func TestOBSAuthentication(t *testing.T) {
	got := obsAuthentication("password", "salt", "challenge")
	const want = "zTM5ki6L2vVvBQiTG9ckH1Lh64AbnCf6XZ226UmnkIA="
	if got != want {
		t.Fatalf("authentication mismatch: got %q want %q", got, want)
	}
}
