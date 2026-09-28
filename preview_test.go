package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeScreenshotDataAcceptsJPEG(t *testing.T) {
	want := []byte{0xff, 0xd8, 0xff, 0xd9}
	got, err := decodeScreenshotData(jpegDataPrefix + base64.StdEncoding.EncodeToString(want))
	if err != nil || string(got) != string(want) {
		t.Fatalf("image=%x err=%v", got, err)
	}
}

func TestDecodeScreenshotDataRejectsUnsupportedInvalidAndOversizedImages(t *testing.T) {
	values := []string{
		"data:image/png;base64,AAAA",
		jpegDataPrefix + "%%%",
		jpegDataPrefix + base64.StdEncoding.EncodeToString([]byte("not a jpeg")),
		jpegDataPrefix + base64.StdEncoding.EncodeToString(make([]byte, 4*1024*1024+1)),
	}
	for _, value := range values {
		if _, err := decodeScreenshotData(value); err == nil {
			t.Fatalf("accepted invalid screenshot")
		}
	}
}

func TestEmbeddedPhonePageIncludesProgramPreview(t *testing.T) {
	html, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"page-viewport", "data-page=\"controls\"", "data-page=\"preview\"", "preview-panel", "program-preview", "preview-placeholder", "chat-feed", "chat-status"} {
		if !strings.Contains(string(html), expected) {
			t.Fatalf("phone page is missing %q", expected)
		}
	}
}
