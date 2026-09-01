package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const subtitleFrameBytes = 3200

func runSubtitleSender(ctx context.Context, cfg subtitleConfig, workerURL, token string) error {
	endpoint, err := url.Parse(strings.TrimRight(workerURL, "/") + "/v1/live")
	if err != nil {
		return err
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else if endpoint.Scheme == "http" {
		endpoint.Scheme = "ws"
	}
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		return fmt.Errorf("connect to subtitle worker: %w", err)
	}
	defer connection.Close()
	defer clearSubtitleOutputs(cfg)

	if err := connection.WriteJSON(map[string]any{"language": "auto", "speech_threshold": 420}); err != nil {
		return err
	}
	var ready map[string]any
	if err := connection.ReadJSON(&ready); err != nil {
		return err
	}
	if ready["type"] != "ready" {
		return fmt.Errorf("unexpected subtitle worker response: %v", ready)
	}

	ffmpeg := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "warning", "-f", "pulse", "-i", cfg.Source, "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "-")
	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		return err
	}
	ffmpeg.Stderr = os.Stderr
	if err := ffmpeg.Start(); err != nil {
		return fmt.Errorf("start microphone capture: %w", err)
	}
	defer func() {
		if ffmpeg.Process != nil {
			_ = ffmpeg.Process.Kill()
		}
		_ = ffmpeg.Wait()
	}()

	errorsCh := make(chan error, 2)
	var closeOnce sync.Once
	closeConnection := func() { closeOnce.Do(func() { _ = connection.Close() }) }
	go func() {
		reader := bufio.NewReaderSize(stdout, subtitleFrameBytes)
		buffer := make([]byte, subtitleFrameBytes)
		for {
			n, readErr := io.ReadFull(reader, buffer)
			if n > 0 {
				if writeErr := connection.WriteMessage(websocket.BinaryMessage, buffer[:n]); writeErr != nil {
					errorsCh <- writeErr
					return
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					_ = connection.WriteMessage(websocket.TextMessage, []byte("flush"))
					errorsCh <- nil
				} else {
					errorsCh <- readErr
				}
				return
			}
		}
	}()
	go func() {
		for {
			_, payload, readErr := connection.ReadMessage()
			if readErr != nil {
				errorsCh <- readErr
				return
			}
			var event struct{ Type, Original, English string }
			if json.Unmarshal(payload, &event) != nil {
				continue
			}
			if event.Type == "flushed" {
				errorsCh <- nil
				return
			}
			if event.Type != "caption" {
				continue
			}
			if err := atomicText(cfg.OriginalOutput, strings.TrimSpace(event.Original)); err != nil {
				errorsCh <- err
				return
			}
			english := strings.TrimSpace(event.English)
			if english == strings.TrimSpace(event.Original) {
				english = ""
			}
			if err := atomicText(cfg.EnglishOutput, english); err != nil {
				errorsCh <- err
				return
			}
			combined := strings.TrimSpace(event.Original)
			if english != "" {
				combined += "\n" + english
			}
			if err := atomicText(cfg.CombinedOutput, combined); err != nil {
				errorsCh <- err
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		closeConnection()
		return ctx.Err()
	case runErr := <-errorsCh:
		closeConnection()
		return runErr
	}
}

func atomicText(path, value string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".subtitle-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0600); err == nil {
		if value != "" {
			value += "\n"
		}
		_, err = temporary.WriteString(value)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func clearSubtitleOutputs(cfg subtitleConfig) {
	_ = atomicText(cfg.OriginalOutput, "")
	_ = atomicText(cfg.EnglishOutput, "")
	_ = atomicText(cfg.CombinedOutput, "")
}
