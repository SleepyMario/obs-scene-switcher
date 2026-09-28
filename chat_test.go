package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPhoneChatUsesStreamchatOverlayFeed(t *testing.T) {
	controller := newSubtitleController(subtitleConfig{APIURL: "https://streamchat.invalid", Password: "secret"})
	controller.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_, password, ok := request.BasicAuth()
		if request.URL.Path != "/api/overlay/chat" || !ok || password != "secret" {
			t.Fatalf("unexpected request: %s auth=%v", request.URL, ok)
		}
		body := `{"recent_chat":[{"id":"1","platform":"twitch","timestamp":"2026-09-28T00:00:00Z","author_display_name":"Viewer","text":"hello","event_type":"message"}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})}
	app := &application{subtitles: controller}
	request := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	response := httptest.NewRecorder()
	app.chatFeed(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"author_display_name":"Viewer"`)) {
		t.Fatalf("chat response=%d %s", response.Code, response.Body.String())
	}
}
