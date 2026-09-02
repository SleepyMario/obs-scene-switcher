package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

type obsClient struct {
	url        string
	configPath string
	timeout    time.Duration
}

type obsMessage struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type obsHello struct {
	RPCVersion     int `json:"rpcVersion"`
	Authentication *struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication,omitempty"`
}

type obsResponse struct {
	RequestType   string `json:"requestType"`
	RequestID     string `json:"requestId"`
	RequestStatus struct {
		Result  bool   `json:"result"`
		Code    int    `json:"code"`
		Comment string `json:"comment,omitempty"`
	} `json:"requestStatus"`
	ResponseData json.RawMessage `json:"responseData,omitempty"`
}

type obsConfig struct {
	Password string `json:"server_password"`
}

func (c *obsClient) scenes(ctx context.Context) ([]string, string, error) {
	response, err := c.request(ctx, "GetSceneList", nil)
	if err != nil {
		return nil, "", err
	}
	var data struct {
		CurrentProgramSceneName string `json:"currentProgramSceneName"`
		Scenes                  []struct {
			Name string `json:"sceneName"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(response, &data); err != nil {
		return nil, "", fmt.Errorf("decode scene list: %w", err)
	}
	scenes := make([]string, 0, len(data.Scenes))
	for i := len(data.Scenes) - 1; i >= 0; i-- {
		scenes = append(scenes, data.Scenes[i].Name)
	}
	return scenes, data.CurrentProgramSceneName, nil
}

func (c *obsClient) switchScene(ctx context.Context, scene string) error {
	_, err := c.request(ctx, "SetCurrentProgramScene", map[string]string{"sceneName": scene})
	return err
}

func (c *obsClient) setSceneItemEnabled(ctx context.Context, scene, source string, enabled bool) error {
	response, err := c.request(ctx, "GetSceneItemId", map[string]string{
		"sceneName":  scene,
		"sourceName": source,
	})
	if err != nil {
		return err
	}
	var item struct {
		ID int `json:"sceneItemId"`
	}
	if err := json.Unmarshal(response, &item); err != nil {
		return fmt.Errorf("decode OBS scene item: %w", err)
	}
	if item.ID == 0 {
		return fmt.Errorf("OBS scene item %q was not found in %q", source, scene)
	}
	_, err = c.request(ctx, "SetSceneItemEnabled", map[string]any{
		"sceneName":        scene,
		"sceneItemId":      item.ID,
		"sceneItemEnabled": enabled,
	})
	return err
}

func (c *obsClient) request(parent context.Context, requestType string, data any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: c.timeout}
	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to OBS: %w", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(c.timeout))
	_ = conn.SetWriteDeadline(time.Now().Add(c.timeout))

	var message obsMessage
	if err := conn.ReadJSON(&message); err != nil || message.Op != 0 {
		return nil, fmt.Errorf("read OBS hello: %w", err)
	}
	var hello obsHello
	if err := json.Unmarshal(message.D, &hello); err != nil {
		return nil, fmt.Errorf("decode OBS hello: %w", err)
	}
	identify := map[string]any{"rpcVersion": 1, "eventSubscriptions": 1}
	if hello.Authentication != nil {
		password, err := c.password()
		if err != nil {
			return nil, err
		}
		identify["authentication"] = obsAuthentication(password, hello.Authentication.Salt, hello.Authentication.Challenge)
	}
	if err := conn.WriteJSON(map[string]any{"op": 1, "d": identify}); err != nil {
		return nil, fmt.Errorf("identify to OBS: %w", err)
	}
	if err := conn.ReadJSON(&message); err != nil || message.Op != 2 {
		return nil, fmt.Errorf("OBS authentication failed: %w", err)
	}

	requestID := fmt.Sprintf("phone-%d", time.Now().UnixNano())
	request := map[string]any{"requestType": requestType, "requestId": requestID}
	if data != nil {
		request["requestData"] = data
	}
	if err := conn.WriteJSON(map[string]any{"op": 6, "d": request}); err != nil {
		return nil, fmt.Errorf("send OBS request: %w", err)
	}
	for {
		if err := conn.ReadJSON(&message); err != nil {
			return nil, fmt.Errorf("read OBS response: %w", err)
		}
		if message.Op != 7 {
			continue
		}
		var response obsResponse
		if err := json.Unmarshal(message.D, &response); err != nil {
			return nil, fmt.Errorf("decode OBS response: %w", err)
		}
		if response.RequestID != requestID {
			continue
		}
		if !response.RequestStatus.Result {
			return nil, fmt.Errorf("OBS rejected %s (%d): %s", requestType, response.RequestStatus.Code, response.RequestStatus.Comment)
		}
		return response.ResponseData, nil
	}
}

func (c *obsClient) password() (string, error) {
	b, err := os.ReadFile(c.configPath)
	if err != nil {
		return "", fmt.Errorf("read OBS configuration: %w", err)
	}
	var cfg obsConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("decode OBS configuration: %w", err)
	}
	if cfg.Password == "" {
		return "", fmt.Errorf("OBS WebSocket password is empty")
	}
	return cfg.Password, nil
}

func obsAuthentication(password, salt, challenge string) string {
	secretHash := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(secretHash[:])
	authHash := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(authHash[:])
}
