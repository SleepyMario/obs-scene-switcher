package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const jpegDataPrefix = "data:image/jpeg;base64,"

func decodeScreenshotData(value string) ([]byte, error) {
	if !strings.HasPrefix(value, jpegDataPrefix) {
		return nil, errors.New("OBS returned an unsupported screenshot format")
	}
	image, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, jpegDataPrefix))
	if err != nil {
		return nil, errors.New("OBS returned an invalid screenshot")
	}
	if len(image) == 0 || len(image) > 4*1024*1024 {
		return nil, errors.New("OBS returned an invalid screenshot size")
	}
	if len(image) < 3 || image[0] != 0xff || image[1] != 0xd8 || image[2] != 0xff {
		return nil, errors.New("OBS returned invalid JPEG data")
	}
	return image, nil
}

func (a *application) programPreview(w http.ResponseWriter, r *http.Request) {
	image, err := a.obs.programScreenshot(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":  "Program preview is unavailable",
			"detail": err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(image)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image)
}
