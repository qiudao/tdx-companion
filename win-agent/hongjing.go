//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type hjMessage struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Action  string          `json:"action,omitempty"`
	From    string          `json:"from"`
	To      string          `json:"to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func hjPlatformAPI() string {
	if v := strings.TrimSpace(os.Getenv("HONGJING_PLATFORM_API")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:19801"
}

func startHongjingRegisterLoop(pluginName, version string, port int) {
	go func() {
		client := &http.Client{Timeout: 4 * time.Second}
		regBody, _ := json.Marshal(map[string]any{
			"name":    pluginName,
			"version": version,
			"port":    port,
		})
		for {
			resp, err := client.Post(hjPlatformAPI()+"/register", "application/json", bytes.NewReader(regBody))
			if err == nil {
				_ = resp.Body.Close()
				time.Sleep(30 * time.Second)
				continue
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

func sendHongjingResponse(req hjMessage, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"ok":false,"err":"marshal response failed"}`)
	}
	respMsg := hjMessage{
		ID:      req.ID,
		Type:    "response",
		Action:  req.Action,
		From:    req.To,
		To:      req.From,
		Payload: raw,
	}
	body, _ := json.Marshal(respMsg)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(hjPlatformAPI()+"/send", "application/json", bytes.NewReader(body))
	if err == nil {
		_ = resp.Body.Close()
	}
}

func localTdxHTTP(method, path string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1:19876"+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func handleHongjingMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var msg hjMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})

	go dispatchTdxAction(msg)
}

func dispatchTdxAction(msg hjMessage) {
	p := map[string]any{}
	if len(msg.Payload) > 0 {
		_ = json.Unmarshal(msg.Payload, &p)
	}
	strParam := func(k string) string {
		if v, ok := p[k]; ok {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}

	method := http.MethodGet
	var path string
	var body []byte

	switch msg.Action {
	case "tdx.health":
		path = "/health"
	case "tdx.status":
		path = "/status"
	case "tdx.switch":
		path = "/switch?key=" + url.QueryEscape(strParam("key"))
	case "tdx.bars":
		q := url.Values{}
		if v := strParam("key"); v != "" {
			q.Set("key", v)
		}
		if v := strParam("period"); v != "" {
			q.Set("period", v)
		}
		if v := strParam("limit"); v != "" {
			q.Set("limit", v)
		}
		path = "/data/bars?" + q.Encode()
	case "tdx.blocks":
		path = "/data/blocks"
	case "tdx.blk.write":
		method = http.MethodPost
		path = "/blk?name=" + url.QueryEscape(strParam("name"))
		body = []byte(strParam("content"))
	default:
		sendHongjingResponse(msg, map[string]any{"ok": false, "err": "unknown tdx action: " + msg.Action})
		return
	}

	st, raw, err := localTdxHTTP(method, path, body)
	if err != nil {
		sendHongjingResponse(msg, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		sendHongjingResponse(msg, parsed)
	} else {
		sendHongjingResponse(msg, map[string]any{"ok": st < 400, "status": st, "body": string(raw)})
	}
}
