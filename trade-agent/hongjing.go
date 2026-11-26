//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
				// Re-register every 30s in case platform.exe restarts
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

func hjCheckAllowed(msg hjMessage, p map[string]any) error {
	if allowed := strings.TrimSpace(os.Getenv("TRADE_ALLOWED_FROM")); allowed != "" {
		ok := false
		for _, a := range strings.Split(allowed, ",") {
			if strings.EqualFold(strings.TrimSpace(a), msg.From) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("controller %q not in TRADE_ALLOWED_FROM", msg.From)
		}
	}
	if authToken != "" {
		tok, _ := p["token"].(string)
		if strings.TrimSpace(tok) != authToken {
			return fmt.Errorf("unauthorized: invalid token")
		}
	}
	return nil
}

func localHTTP(method, path string, body []byte) (int, []byte, http.Header, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1:19877"+path, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authToken != "" {
		req.Header.Set("X-Trade-Token", authToken)
	}
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header, err
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	go dispatchTradeAction(msg)
}

func dispatchTradeAction(msg hjMessage) {
	p := map[string]any{}
	if len(msg.Payload) > 0 {
		_ = json.Unmarshal(msg.Payload, &p)
	}
	if err := hjCheckAllowed(msg, p); err != nil {
		sendHongjingResponse(msg, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	strParam := func(k string) string {
		if v, ok := p[k]; ok {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}

	var (
		method  = http.MethodGet
		path    string
		reqBody []byte
		isPNG   bool
	)

	switch msg.Action {
	case "trade.health":
		path = "/health"
	case "trade.probe":
		path = "/probe"
		if strParam("all") == "1" || strParam("all") == "true" {
			path += "?all=1"
		}
	case "trade.dialogs":
		path = "/dialogs"
	case "trade.dialog.dismiss":
		method = http.MethodPost
		path = "/dialog/dismiss"
		if strParam("accept") == "0" || strParam("accept") == "false" {
			path += "?accept=0"
		}
	case "trade.read.texts":
		path = "/read/texts"
	case "trade.focus":
		method = http.MethodPost
		path = "/focus"
	case "trade.nav":
		method = http.MethodPost
		path = "/nav?key=" + url.QueryEscape(strParam("key"))
	case "trade.capacity":
		q := url.Values{}
		if v := strParam("order_type"); v != "" {
			q.Set("order_type", v)
		}
		if v := strParam("code"); v != "" {
			q.Set("code", v)
		}
		if v := strParam("price"); v != "" {
			q.Set("price", v)
		}
		path = "/stock/capacity?" + q.Encode()
	case "trade.prefill":
		method = http.MethodPost
		path = "/prefill"
		reqBody = msg.Payload
	case "trade.submit":
		method = http.MethodPost
		path = "/order/submit"
		reqBody = msg.Payload
	case "trade.cancel":
		method = http.MethodPost
		path = "/order/cancel"
		reqBody = msg.Payload
	case "trade.risk.halt":
		method = http.MethodPost
		path = "/risk/halt"
	case "trade.risk.resume":
		method = http.MethodPost
		path = "/risk/resume"
	case "trade.keepalive":
		method = http.MethodPost
		q := url.Values{}
		if v := strParam("enabled"); v != "" {
			q.Set("enabled", v)
		}
		if v := strParam("interval_sec"); v != "" {
			q.Set("interval_sec", v)
		}
		path = "/keepalive?" + q.Encode()
	case "trade.screenshot":
		q := url.Values{}
		if v := strParam("target"); v != "" {
			q.Set("target", v)
		}
		if v := strParam("tab"); v != "" {
			q.Set("tab", v)
		}
		if v := strParam("maximize"); v != "" {
			q.Set("maximize", v)
		}
		path = "/screenshot?" + q.Encode()
		isPNG = true
	case "trade.gs.health":
		path = "/gs/health"
	case "trade.gs.nav":
		method = http.MethodPost
		tab := strParam("tab")
		if tab == "" {
			tab = strParam("key")
		}
		path = "/gs/nav?tab=" + url.QueryEscape(tab)
	case "trade.gs.prefill":
		method = http.MethodPost
		path = "/gs/prefill"
		reqBody = msg.Payload
	case "trade.gs.submit":
		method = http.MethodPost
		path = "/gs/submit"
		reqBody = msg.Payload
	case "trade.gs.cancel":
		method = http.MethodPost
		path = "/gs/cancel"
		reqBody = msg.Payload
	case "trade.gs.dialogs":
		path = "/gs/dialogs"
	case "trade.gs.dialog.dismiss":
		method = http.MethodPost
		path = "/gs/dialog/dismiss"
		if strParam("accept") == "0" || strParam("accept") == "false" {
			path += "?accept=0"
		}
	case "trade.gs.screenshot":
		q := url.Values{}
		if v := strParam("tab"); v != "" {
			q.Set("tab", v)
		}
		path = "/gs/screenshot?" + q.Encode()
		isPNG = true
	case "trade.gtja.health":
		path = "/gtja/health"
	case "trade.gtja.nav":
		method = http.MethodPost
		tab := strParam("tab")
		if tab == "" {
			tab = strParam("key")
		}
		path = "/gtja/nav?tab=" + url.QueryEscape(tab)
	case "trade.gtja.prefill":
		method = http.MethodPost
		path = "/gtja/prefill"
		reqBody = msg.Payload
	case "trade.gtja.submit":
		method = http.MethodPost
		path = "/gtja/submit"
		reqBody = msg.Payload
	case "trade.gtja.cancel":
		method = http.MethodPost
		path = "/gtja/cancel"
		reqBody = msg.Payload
	case "trade.gtja.screenshot":
		path = "/gtja/screenshot"
		isPNG = true
	default:
		sendHongjingResponse(msg, map[string]any{"ok": false, "err": "unknown trade action: " + msg.Action})
		return
	}

	st, raw, hdr, err := localHTTP(method, path, reqBody)
	if err != nil {
		sendHongjingResponse(msg, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	if isPNG && st < 400 {
		wPx, _ := strconv.Atoi(hdr.Get("X-Image-Width"))
		hPx, _ := strconv.Atoi(hdr.Get("X-Image-Height"))
		sendHongjingResponse(msg, map[string]any{
			"ok":         true,
			"width":      wPx,
			"height":     hPx,
			"png_base64": base64.StdEncoding.EncodeToString(raw),
		})
		return
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		sendHongjingResponse(msg, parsed)
	} else {
		sendHongjingResponse(msg, map[string]any{"ok": st < 400, "status": st, "body": string(raw)})
	}
}
