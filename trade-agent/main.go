//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	agentVersion = "0.1.0"
	defaultAddr  = "0.0.0.0:19877"
	xiadanExe    = "xiadan.exe"
)

var (
	startTime = time.Now()
	riskMgr   = NewRiskManager()
	authToken = strings.TrimSpace(os.Getenv("TRADE_AGENT_TOKEN"))
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if authToken != "" {
			got := strings.TrimSpace(r.Header.Get("X-Trade-Token"))
			if got == "" {
				got = strings.TrimSpace(r.URL.Query().Get("token"))
			}
			if got != authToken {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "err": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	dialogs := findVisibleDialogs(pid)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"version":        agentVersion,
		"uptime_sec":     int(time.Since(startTime).Seconds()),
		"xiadan_pid":     pid,
		"xiadan_online":  ok,
		"xiadan_title":   mainWin.Text,
		"xiadan_class":   mainWin.Class,
		"active_dialogs": len(dialogs),
		"risk":           riskMgr.Config(),
	})
}

func handleProbe(w http.ResponseWriter, r *http.Request) {
	pid := findProcessPID(xiadanExe)
	if pid == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan.exe not running"})
		return
	}
	includeHidden := r.URL.Query().Get("all") == "1"
	tops := findTopWindowsByPID(pid, includeHidden)
	mainWin, hasMain := findMainXiadanWindow(pid)
	var children []WinNode
	if hasMain {
		children = enumChildNodes(mainWin.Hwnd, includeHidden)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"pid":         pid,
		"main_window": mainWin,
		"top_windows": tops,
		"child_count": len(children),
		"children":    children,
	})
}

func handleDialogs(w http.ResponseWriter, r *http.Request) {
	pid := findProcessPID(xiadanExe)
	dialogs := findVisibleDialogs(pid)
	type dlgDetail struct {
		Window   WinNode   `json:"window"`
		Children []WinNode `json:"children"`
	}
	out := make([]dlgDetail, 0, len(dialogs))
	for _, d := range dialogs {
		out = append(out, dlgDetail{
			Window:   d,
			Children: enumChildNodes(d.Hwnd, false),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"count":   len(out),
		"dialogs": out,
	})
}

func handleReadTexts(w http.ResponseWriter, r *http.Request) {
	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	children := enumChildNodes(mainWin.Hwnd, false)
	var items []WinNode
	for _, ch := range children {
		if ch.Text != "" && (strings.EqualFold(ch.Class, "Static") || strings.EqualFold(ch.Class, "Edit") || strings.EqualFold(ch.Class, "Button")) {
			items = append(items, ch)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"count": len(items),
		"items": items,
	})
}

func handleFocus(w http.ResponseWriter, r *http.Request) {
	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	focusWindow(mainWin.Hwnd)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hwnd": mainWin.Hwnd, "title": mainWin.Text})
}

func handleNav(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	if err := sendNavKey(mainWin.Hwnd, key); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	riskMgr.LogAudit("nav", true, map[string]any{"key": key}, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": strings.ToUpper(key)})
}

type PrefillReq struct {
	Side           string `json:"side"`             // "buy" or "sell"
	Code           string `json:"code"`             // 6-digit A-share code
	Price          string `json:"price"`            // e.g. "8.99" (optional)
	Qty            string `json:"qty"`              // e.g. "100" (optional)
	CodeCtrlID     int32  `json:"code_ctrl_id"`     // default 1032
	PriceCtrlID    int32  `json:"price_ctrl_id"`    // default 1033
	QtyCtrlID      int32  `json:"qty_ctrl_id"`      // default 1034
	IdempotencyKey string `json:"idempotency_key"`
}

// POST /prefill — Semi-automatic SAFE form prefill ONLY (NEVER clicks Buy/Sell/Confirm).
func handlePrefill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "err": "POST required"})
		return
	}
	var req PrefillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "invalid json: " + err.Error()})
		return
	}
	req.Side = strings.ToLower(strings.TrimSpace(req.Side))
	req.Code = strings.TrimSpace(req.Code)
	req.Price = strings.TrimSpace(req.Price)
	req.Qty = strings.TrimSpace(req.Qty)
	if req.Side != "buy" && req.Side != "sell" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "side must be buy or sell"})
		return
	}
	if len(req.Code) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "code must be 6 digits"})
		return
	}
	if req.CodeCtrlID == 0 {
		req.CodeCtrlID = 1032
	}
	if req.PriceCtrlID == 0 {
		req.PriceCtrlID = 1033
	}
	if req.QtyCtrlID == 0 {
		req.QtyCtrlID = 1034
	}

	priceVal, _ := strconv.ParseFloat(req.Price, 64)
	qtyVal, _ := strconv.Atoi(req.Qty)
	if err := riskMgr.CheckPrefill(req.Side, req.Code, priceVal, qtyVal, req.IdempotencyKey); err != nil {
		riskMgr.LogAudit("prefill_blocked", false, req, err.Error())
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "err": err.Error()})
		return
	}

	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	if dialogs := findVisibleDialogs(pid); len(dialogs) > 0 {
		errMsg := fmt.Sprintf("blocking dialog detected (%d active); please resolve manually first", len(dialogs))
		riskMgr.LogAudit("prefill_dialog_block", false, req, errMsg)
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "err": errMsg, "dialogs": dialogs})
		return
	}

	navKey := "F1"
	if req.Side == "sell" {
		navKey = "F2"
	}
	_ = sendNavKey(mainWin.Hwnd, navKey)
	time.Sleep(180 * time.Millisecond)

	codeEdit, okC := findVisibleEditByCtrlID(mainWin.Hwnd, req.CodeCtrlID)
	if !okC {
		errMsg := fmt.Sprintf("code Edit ctrl_id=%d not found on active panel; inspect /probe", req.CodeCtrlID)
		riskMgr.LogAudit("prefill_missing_ctrl", false, req, errMsg)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "err": errMsg})
		return
	}
	setWindowText(codeEdit.Hwnd, req.Code)
	time.Sleep(250 * time.Millisecond)

	readback := map[string]string{
		"code": getWindowText(codeEdit.Hwnd),
	}

	if req.Price != "" {
		if priceEdit, okP := findVisibleEditByCtrlID(mainWin.Hwnd, req.PriceCtrlID); okP {
			setWindowText(priceEdit.Hwnd, req.Price)
			time.Sleep(80 * time.Millisecond)
			readback["price"] = getWindowText(priceEdit.Hwnd)
		} else {
			readback["price_err"] = fmt.Sprintf("ctrl_id=%d not found", req.PriceCtrlID)
		}
	}
	if req.Qty != "" {
		if qtyEdit, okQ := findVisibleEditByCtrlID(mainWin.Hwnd, req.QtyCtrlID); okQ {
			setWindowText(qtyEdit.Hwnd, req.Qty)
			time.Sleep(80 * time.Millisecond)
			readback["qty"] = getWindowText(qtyEdit.Hwnd)
		} else {
			readback["qty_err"] = fmt.Sprintf("ctrl_id=%d not found", req.QtyCtrlID)
		}
	}

	riskMgr.LogAudit("prefill", true, map[string]any{"req": req, "readback": readback}, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"mode":     "prefill_only",
		"note":     "已填入输入框，未触发下单按钮，请人工核对后手动确认",
		"readback": readback,
	})
}

func handleRiskHalt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "risk": riskMgr.SetHalted(true)})
}

func handleRiskResume(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "risk": riskMgr.SetHalted(false)})
}

func main() {
	addr := strings.TrimSpace(os.Getenv("TRADE_AGENT_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", withAuth(handleHealth))
	mux.HandleFunc("/probe", withAuth(handleProbe))
	mux.HandleFunc("/dialogs", withAuth(handleDialogs))
	mux.HandleFunc("/read/texts", withAuth(handleReadTexts))
	mux.HandleFunc("/focus", withAuth(handleFocus))
	mux.HandleFunc("/nav", withAuth(handleNav))
	mux.HandleFunc("/prefill", withAuth(handlePrefill))
	mux.HandleFunc("/risk/halt", withAuth(handleRiskHalt))
	mux.HandleFunc("/risk/resume", withAuth(handleRiskResume))

	_ = http.ListenAndServe(addr, mux)
}
