//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	agentVersion = "0.6.0"
	defaultAddr  = "0.0.0.0:19877"
	xiadanExe    = "xiadan.exe"
)

type KeepaliveState struct {
	mu          sync.Mutex
	Enabled     bool   `json:"enabled"`
	IntervalSec int    `json:"interval_sec"`
	LastPingAt  string `json:"last_ping_at,omitempty"`
	PingCount   int    `json:"ping_count"`
	LastStatus  string `json:"last_status,omitempty"`
}

var keepalive = &KeepaliveState{
	Enabled:     true,
	IntervalSec: 240, // 4 minutes during trading hours
}

func isTradingHours(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	hm := now.Hour()*100 + now.Minute()
	return (hm >= 915 && hm <= 1132) || (hm >= 1258 && hm <= 1502)
}

func startKeepaliveLoop() {
	go func() {
		var lastRun time.Time
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			keepalive.mu.Lock()
			enabled := keepalive.Enabled
			interval := time.Duration(keepalive.IntervalSec) * time.Second
			keepalive.mu.Unlock()

			if !enabled || interval < 30*time.Second {
				continue
			}
			if !isTradingHours(now) {
				continue
			}
			if !lastRun.IsZero() && now.Sub(lastRun) < interval {
				continue
			}
			if riskMgr.Config().Halted {
				continue
			}
			pid := findProcessPID(xiadanExe)
			mainWin, ok := findMainXiadanWindow(pid)
			if !ok {
				continue
			}
			if len(findVisibleDialogs(pid)) > 0 {
				keepalive.mu.Lock()
				keepalive.LastStatus = "paused_dialog_active"
				keepalive.mu.Unlock()
				continue
			}
			// Silent refresh via button 32790 (no foreground focus stealing)
			if refreshBtn, okR := findVisibleCtrlByIDAndClass(mainWin.Hwnd, 32790, "Button"); okR {
				clickButton(refreshBtn.Hwnd)
				lastRun = now
				keepalive.mu.Lock()
				keepalive.LastPingAt = now.Format(time.RFC3339)
				keepalive.PingCount++
				keepalive.LastStatus = "ok_btn_32790"
				keepalive.mu.Unlock()
			}
		}
	}()
}

func (k *KeepaliveState) Snapshot() map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	return map[string]any{
		"enabled":      k.Enabled,
		"interval_sec": k.IntervalSec,
		"last_ping_at": k.LastPingAt,
		"ping_count":   k.PingCount,
		"last_status":  k.LastStatus,
	}
}

var contractNoRe = regexp.MustCompile(`合同编号[：:]\s*([0-9A-Za-z_-]+)`)

func extractContractNo(texts []string) string {
	for _, t := range texts {
		if m := contractNoRe.FindStringSubmatch(t); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

func resolveOrderNav(orderType, side string) (navKey, normSide, expectHint string, err error) {
	ot := strings.ToLower(strings.TrimSpace(orderType))
	if ot == "" {
		ot = strings.ToLower(strings.TrimSpace(side))
	}
	switch ot {
	case "buy", "collateral_buy":
		return "F1", "buy", "买入", nil
	case "sell", "collateral_sell":
		return "F2", "sell", "卖出", nil
	case "margin_buy":
		return "F7", "buy", "融资买入", nil
	case "repay_by_sell":
		return "F8", "sell", "卖券还款", nil
	default:
		return "", "", "", fmt.Errorf("invalid order_type/side: %q (use buy|sell|collateral_buy|collateral_sell|margin_buy|repay_by_sell)", ot)
	}
}

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
		"keepalive":      keepalive.Snapshot(),
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

// GET /grid?tab=F3|F4 — Navigate to F3 (open orders) or F4 (positions) and parse CVirtualGridCtrl via clipboard.
func handleGrid(w http.ResponseWriter, r *http.Request) {
	tab := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("tab")))
	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	if tab != "" {
		if err := sendNavKey(mainWin.Hwnd, tab); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	panelTitle := ""
	if pNode, ok := findVisibleCtrlByIDAndClass(mainWin.Hwnd, 1478, "Static"); ok {
		panelTitle = pNode.Text
	}
	tsv, dlgs, err := copyGridTSV(pid, mainWin.Hwnd)
	if err != nil {
		var snaps []DialogSnapshot
		for _, d := range dlgs {
			s, _ := inspectDialog(d)
			snaps = append(snaps, s)
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":          false,
			"err":         err.Error(),
			"panel_title": panelTitle,
			"dialogs":     snaps,
		})
		return
	}
	headers, rows := parseTSVRows(tsv)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"tab":         tab,
		"panel_title": panelTitle,
		"headers":     headers,
		"count":       len(rows),
		"rows":        rows,
	})
}

type SubmitOrderReq struct {
	OrderType         string `json:"order_type"`          // collateral_buy|collateral_sell|margin_buy|repay_by_sell (or side: buy|sell)
	Side              string `json:"side"`                // legacy alias: "buy" or "sell"
	Code              string `json:"code"`                // 6 digits
	Price             string `json:"price"`               // e.g. "11.07"
	Qty               string `json:"qty"`                 // e.g. "100"
	SkipNav           bool   `json:"skip_nav"`            // stay on current panel
	ExpectedPanel     string `json:"expected_panel"`      // optional verify against id=1478
	ConfirmLive       bool   `json:"confirm_live"`        // MUST be true to actually click submit
	AutoConfirmDialog bool   `json:"auto_confirm_dialog"` // auto-click Yes/OK on standard order confirm & result popups
	IdempotencyKey    string `json:"idempotency_key"`
}

func handleOrderSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "err": "POST required"})
		return
	}
	var req SubmitOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "invalid json: " + err.Error()})
		return
	}
	if !req.ConfirmLive {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "err": "confirm_live=true required for live order submission"})
		return
	}
	navKey, normSide, expectHint, err := resolveOrderNav(req.OrderType, req.Side)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	req.Price = strings.TrimSpace(req.Price)
	req.Qty = strings.TrimSpace(req.Qty)
	if len(req.Code) != 6 || req.Price == "" || req.Qty == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "6-digit code, price, and qty are all required"})
		return
	}

	priceVal, _ := strconv.ParseFloat(req.Price, 64)
	qtyVal, _ := strconv.Atoi(req.Qty)
	if err := riskMgr.CheckPrefill(normSide, req.Code, priceVal, qtyVal, req.IdempotencyKey); err != nil {
		riskMgr.LogAudit("order_submit_blocked", false, req, err.Error())
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
		var snaps []DialogSnapshot
		for _, d := range dialogs {
			s, _ := inspectDialog(d)
			snaps = append(snaps, s)
		}
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "err": "blocking dialog active before submit", "dialogs": snaps})
		return
	}

	if !req.SkipNav {
		_ = sendNavKey(mainWin.Hwnd, navKey)
		time.Sleep(220 * time.Millisecond)
	}

	panelTitle := ""
	if pNode, ok := findVisibleCtrlByIDAndClass(mainWin.Hwnd, 1478, "Static"); ok {
		panelTitle = pNode.Text
	}
	wantPanel := req.ExpectedPanel
	if wantPanel == "" && !req.SkipNav && expectHint != "" {
		wantPanel = expectHint
	}
	if wantPanel != "" && !strings.Contains(panelTitle, wantPanel) {
		errMsg := fmt.Sprintf("panel mismatch: got %q, expected %q", panelTitle, wantPanel)
		riskMgr.LogAudit("order_submit_panel_mismatch", false, req, errMsg)
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "err": errMsg, "panel_title": panelTitle})
		return
	}

	codeEdit, okC := findVisibleEditByCtrlID(mainWin.Hwnd, 1032)
	priceEdit, okP := findVisibleEditByCtrlID(mainWin.Hwnd, 1033)
	qtyEdit, okQ := findVisibleEditByCtrlID(mainWin.Hwnd, 1034)
	submitBtn, okB := findVisibleCtrlByIDAndClass(mainWin.Hwnd, 1006, "Button")
	if !okC || !okP || !okQ || !okB {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "err": "missing order form controls (1032/1033/1034/1006)"})
		return
	}

	setWindowText(codeEdit.Hwnd, req.Code)
	time.Sleep(320 * time.Millisecond)
	setWindowText(priceEdit.Hwnd, req.Price)
	time.Sleep(100 * time.Millisecond)
	setWindowText(qtyEdit.Hwnd, req.Qty)
	time.Sleep(100 * time.Millisecond)

	rbCode := getWindowText(codeEdit.Hwnd)
	rbPrice := getWindowText(priceEdit.Hwnd)
	rbQty := getWindowText(qtyEdit.Hwnd)
	if rbCode != req.Code || rbQty != req.Qty {
		errMsg := fmt.Sprintf("readback mismatch: code=%q price=%q qty=%q", rbCode, rbPrice, rbQty)
		riskMgr.LogAudit("order_submit_readback_fail", false, req, errMsg)
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "err": errMsg})
		return
	}

	clickButton(submitBtn.Hwnd)

	var dialogLog []map[string]any
	contractNo := ""
	for step := 0; step < 5; step++ {
		// Poll briefly up to 900ms for each popup/receipt dialog
		var dlgs []WinNode
		for wait := 0; wait < 9; wait++ {
			time.Sleep(100 * time.Millisecond)
			dlgs = findVisibleDialogs(pid)
			if len(dlgs) > 0 {
				break
			}
		}
		if len(dlgs) == 0 {
			break
		}
		snap, _ := inspectDialog(dlgs[0])
		if cn := extractContractNo(snap.Texts); cn != "" {
			contractNo = cn
		}
		joined := strings.Join(snap.Texts, " ")
		if strings.Contains(joined, "验证码") {
			dialogLog = append(dialogLog, map[string]any{"dialog": snap, "action": "stopped_captcha"})
			break
		}
		if req.AutoConfirmDialog {
			_, act := dismissDialog(dlgs[0], true)
			dialogLog = append(dialogLog, map[string]any{"dialog": snap, "action": act})
			if contractNo != "" {
				break
			}
		} else {
			dialogLog = append(dialogLog, map[string]any{"dialog": snap, "action": "left_open_for_manual"})
			break
		}
	}

	riskMgr.LogAudit("order_submit", true, map[string]any{"req": req, "panel": panelTitle, "contract_no": contractNo, "dialogs": dialogLog}, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"panel_title": panelTitle,
		"contract_no": contractNo,
		"readback":    map[string]string{"code": rbCode, "price": rbPrice, "qty": rbQty},
		"dialogs":     dialogLog,
	})
}

type CancelOrderReq struct {
	Action            string `json:"action"` // "cancel_all" | "cancel_buy" | "cancel_sell"
	AutoConfirmDialog bool   `json:"auto_confirm_dialog"`
}

func handleOrderCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "err": "POST required"})
		return
	}
	var req CancelOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "invalid json: " + err.Error()})
		return
	}
	var btnID int32
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "cancel_all", "":
		btnID = 30001
	case "cancel_buy":
		btnID = 30002
	case "cancel_sell":
		btnID = 30003
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "action must be cancel_all|cancel_buy|cancel_sell"})
		return
	}

	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	_ = sendNavKey(mainWin.Hwnd, "F3")
	time.Sleep(220 * time.Millisecond)

	btn, okB := findVisibleCtrlByIDAndClass(mainWin.Hwnd, btnID, "Button")
	if !okB {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "err": fmt.Sprintf("cancel button id=%d not found on F3", btnID)})
		return
	}
	clickButton(btn.Hwnd)

	var dialogLog []map[string]any
	contractNo := ""
	for step := 0; step < 4; step++ {
		var dlgs []WinNode
		for wait := 0; wait < 9; wait++ {
			time.Sleep(100 * time.Millisecond)
			dlgs = findVisibleDialogs(pid)
			if len(dlgs) > 0 {
				break
			}
		}
		if len(dlgs) == 0 {
			break
		}
		snap, _ := inspectDialog(dlgs[0])
		if cn := extractContractNo(snap.Texts); cn != "" {
			contractNo = cn
		}
		joined := strings.Join(snap.Texts, " ")
		if strings.Contains(joined, "验证码") {
			dialogLog = append(dialogLog, map[string]any{"dialog": snap, "action": "stopped_captcha"})
			break
		}
		if req.AutoConfirmDialog {
			_, act := dismissDialog(dlgs[0], true)
			dialogLog = append(dialogLog, map[string]any{"dialog": snap, "action": act})
			if contractNo != "" {
				break
			}
		} else {
			dialogLog = append(dialogLog, map[string]any{"dialog": snap, "action": "left_open_for_manual"})
			break
		}
	}
	riskMgr.LogAudit("order_cancel", true, map[string]any{"req": req, "contract_no": contractNo, "dialogs": dialogLog}, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": req.Action, "contract_no": contractNo, "dialogs": dialogLog})
}

// GET /stock/capacity?order_type=collateral_buy|margin_buy|collateral_sell|repay_by_sell&code=601857&price=11.07
// Zero-captcha read-only query of buyable/sellable shares and margin metadata, then resets the form.
func handleStockCapacity(w http.ResponseWriter, r *http.Request) {
	orderType := r.URL.Query().Get("order_type")
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	price := strings.TrimSpace(r.URL.Query().Get("price"))
	if len(code) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "6-digit code required"})
		return
	}
	navKey, _, _, err := resolveOrderNav(orderType, "buy")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}

	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	_ = sendNavKey(mainWin.Hwnd, navKey)
	time.Sleep(220 * time.Millisecond)

	codeEdit, okC := findVisibleEditByCtrlID(mainWin.Hwnd, 1032)
	if !okC {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "err": "code edit (1032) not found"})
		return
	}
	setWindowText(codeEdit.Hwnd, code)
	time.Sleep(350 * time.Millisecond)
	if price != "" {
		if priceEdit, okP := findVisibleEditByCtrlID(mainWin.Hwnd, 1033); okP {
			setWindowText(priceEdit.Hwnd, price)
			time.Sleep(180 * time.Millisecond)
		}
	}

	readField := func(id int32, cls string) string {
		if n, ok := findLeftmostCtrlByIDAndClass(mainWin.Hwnd, id, cls); ok {
			return n.Text
		}
		return ""
	}
	resp := map[string]any{
		"ok":                  true,
		"order_type":          orderType,
		"panel_title":         readField(1478, "Static"),
		"code":                code,
		"name":                readField(1036, "Static"),
		"price":               readField(1033, "Edit"),
		"max_qty":             readField(1018, "Static"),
		"maintenance_ratio":   readField(3004, "Edit"),
		"conversion_rate":     readField(3401, "Edit"),
		"concentration_group": readField(3402, "Edit"),
		"is_margin_target":    readField(3404, "Edit"),
	}

	// Clean up form via 重填 (1007)
	if resetBtn, okR := findVisibleCtrlByIDAndClass(mainWin.Hwnd, 1007, "Button"); okR {
		clickButton(resetBtn.Hwnd)
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleDialogDismiss(w http.ResponseWriter, r *http.Request) {
	accept := r.URL.Query().Get("accept") != "0"
	pid := findProcessPID(xiadanExe)
	dlgs := findVisibleDialogs(pid)
	if len(dlgs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": 0})
		return
	}
	snap, act := dismissDialog(dlgs[0], accept)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dialog": snap, "action": act})
}

// GET /screenshot?target=grid|main&tab=F3|F4&maximize=1 — Read-only PNG capture for Mac-side Vision OCR (zero clipboard/captcha).
func handleScreenshot(w http.ResponseWriter, r *http.Request) {
	target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	if target == "" {
		target = "grid"
	}
	tab := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("tab")))
	wantMax := r.URL.Query().Get("maximize") != "0"

	pid := findProcessPID(xiadanExe)
	mainWin, ok := findMainXiadanWindow(pid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "xiadan window not found"})
		return
	}
	if wantMax {
		maximizeWindow(mainWin.Hwnd)
		time.Sleep(180 * time.Millisecond)
	}
	if tab != "" {
		if err := sendNavKey(mainWin.Hwnd, tab); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
			return
		}
		time.Sleep(250 * time.Millisecond)
	}

	var hwnd uintptr = mainWin.Hwnd
	if target == "grid" {
		if grid, okG := findVisibleCtrlByIDAndClass(mainWin.Hwnd, 1047, "CVirtualGridCtrl"); okG {
			hwnd = grid.Hwnd
		} else {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "CVirtualGridCtrl (1047) not found"})
			return
		}
	}

	pngBytes, pw, ph, err := captureWindowPNG(hwnd)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Image-Width", strconv.Itoa(pw))
	w.Header().Set("X-Image-Height", strconv.Itoa(ph))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
}

func handleKeepalive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	keepalive.mu.Lock()
	if v := strings.TrimSpace(q.Get("enabled")); v != "" {
		keepalive.Enabled = (v == "1" || strings.EqualFold(v, "true"))
	}
	if v := strings.TrimSpace(q.Get("interval_sec")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 30 && n <= 3600 {
			keepalive.IntervalSec = n
		}
	}
	keepalive.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keepalive": keepalive.Snapshot()})
}

func main() {
	addr := strings.TrimSpace(os.Getenv("TRADE_AGENT_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}
	startKeepaliveLoop()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", withAuth(handleHealth))
	mux.HandleFunc("/keepalive", withAuth(handleKeepalive))
	mux.HandleFunc("/probe", withAuth(handleProbe))
	mux.HandleFunc("/dialogs", withAuth(handleDialogs))
	mux.HandleFunc("/dialog/dismiss", withAuth(handleDialogDismiss))
	mux.HandleFunc("/read/texts", withAuth(handleReadTexts))
	mux.HandleFunc("/grid", withAuth(handleGrid))
	mux.HandleFunc("/screenshot", withAuth(handleScreenshot))
	mux.HandleFunc("/stock/capacity", withAuth(handleStockCapacity))
	mux.HandleFunc("/focus", withAuth(handleFocus))
	mux.HandleFunc("/nav", withAuth(handleNav))
	mux.HandleFunc("/prefill", withAuth(handlePrefill))
	mux.HandleFunc("/order/submit", withAuth(handleOrderSubmit))
	mux.HandleFunc("/order/cancel", withAuth(handleOrderCancel))
	mux.HandleFunc("/risk/halt", withAuth(handleRiskHalt))
	mux.HandleFunc("/risk/resume", withAuth(handleRiskResume))

	_ = http.ListenAndServe(addr, mux)
}
