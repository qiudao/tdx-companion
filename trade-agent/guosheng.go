//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem    = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow    = user32.NewProc("GetDpiForWindow")
	procSetCursorPos       = user32.NewProc("SetCursorPos")
	procMouseEvent         = user32.NewProc("mouse_event")
	procBringWindowToTop   = user32.NewProc("BringWindowToTop")
	gsDPIOnce              sync.Once
)

const (
	vkMenu          = 0x12
	vkBack          = 0x08
	vkTab           = 0x09
	mouseeventfDown = 0x0002
	mouseeventfUp   = 0x0004
	tvmGetItemH     = 0x111C
)

func gsEnsureDPIAware() {
	gsDPIOnce.Do(func() {
		procSetProcessDPIAware.Call()
	})
}

type GuoshengWindow struct {
	PID       uint32
	MainHwnd  uintptr
	MainTitle string
	TradeDlg  uintptr
}

func findGuoshengTradeWindow() (GuoshengWindow, bool) {
	var found GuoshengWindow
	var cur uintptr
	for {
		h, _, _ := procFindWindowExW.Call(0, cur, 0, 0)
		if h == 0 {
			break
		}
		cur = h
		if !isVisible(h) {
			continue
		}
		cls := getClassName(h)
		if cls != "TdxW_MainFrame_Class" {
			continue
		}
		title := getWindowText(h)
		var pid uint32
		procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		// Locate embedded "通达信网上交易" #32770 panel
		var tradeDlg uintptr
		for _, n := range enumChildNodes(h, false) {
			if n.Visible && n.Class == "#32770" && strings.Contains(n.Text, "通达信网上交易") {
				tradeDlg = n.Hwnd
				break
			}
		}
		if tradeDlg != 0 {
			found = GuoshengWindow{
				PID:       pid,
				MainHwnd:  h,
				MainTitle: title,
				TradeDlg:  tradeDlg,
			}
			// Prefer Guosheng if multiple TdxW instances have trade panels
			if strings.Contains(title, "国盛") {
				return found, true
			}
		}
	}
	return found, found.TradeDlg != 0
}

func gsForceForeground(hwnd uintptr) {
	gsEnsureDPIAware()
	iconic, _, _ := procIsIconic.Call(hwnd)
	if iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
		time.Sleep(120 * time.Millisecond)
	}
	// Tap Alt key to satisfy Windows foreground-lock check
	procKeybdEvent.Call(vkMenu, 0, 0, 0)
	procKeybdEvent.Call(vkMenu, 0, keyeventfKeyUp, 0)
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	time.Sleep(180 * time.Millisecond)
}

func gsClickAt(x, y int32, dbl bool) {
	procSetCursorPos.Call(uintptr(x), uintptr(y))
	time.Sleep(40 * time.Millisecond)
	procMouseEvent.Call(mouseeventfDown, 0, 0, 0, 0)
	time.Sleep(25 * time.Millisecond)
	procMouseEvent.Call(mouseeventfUp, 0, 0, 0, 0)
	if dbl {
		time.Sleep(60 * time.Millisecond)
		procMouseEvent.Call(mouseeventfDown, 0, 0, 0, 0)
		time.Sleep(25 * time.Millisecond)
		procMouseEvent.Call(mouseeventfUp, 0, 0, 0, 0)
	}
	time.Sleep(350 * time.Millisecond)
}

func gsSendDigits(s string) {
	// Select all (Ctrl+A) then Backspace
	procKeybdEvent.Call(vkControl, 0, 0, 0)
	procKeybdEvent.Call('A', 0, 0, 0)
	procKeybdEvent.Call('A', 0, keyeventfKeyUp, 0)
	procKeybdEvent.Call(vkControl, 0, keyeventfKeyUp, 0)
	time.Sleep(30 * time.Millisecond)
	procKeybdEvent.Call(vkBack, 0, 0, 0)
	procKeybdEvent.Call(vkBack, 0, keyeventfKeyUp, 0)
	time.Sleep(40 * time.Millisecond)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			vk := uintptr(r)
			procKeybdEvent.Call(vk, 0, 0, 0)
			procKeybdEvent.Call(vk, 0, keyeventfKeyUp, 0)
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func gsNavRow(action string) (int, string, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "buy", "f1":
		return 0, "买入", nil
	case "sell", "f2":
		return 1, "卖出", nil
	case "market_buy":
		return 2, "市价买入", nil
	case "market_sell":
		return 3, "市价卖出", nil
	case "cancel", "f3":
		return 4, "撤单", nil
	case "query", "f4":
		return 5, "查询", nil
	default:
		return -1, "", fmt.Errorf("unsupported guosheng nav %q (use buy|sell|market_buy|market_sell|cancel|query)", action)
	}
}

func gsNavigate(gw GuoshengWindow, nav string) (string, error) {
	row, label, err := gsNavRow(nav)
	if err != nil {
		return "", err
	}
	gsForceForeground(gw.MainHwnd)
	tv, ok := findVisibleCtrlByIDAndClass(gw.TradeDlg, 59648, "SysTreeView32")
	if !ok {
		return "", fmt.Errorf("SysTreeView32 (59648) not found in guosheng trade panel")
	}
	itemH, _, _ := procSendMessageW.Call(tv.Hwnd, tvmGetItemH, 0, 0)
	if itemH == 0 {
		itemH = 18
	}
	sysDpi, _, _ := procGetDpiForSystem.Call()
	winDpi, _, _ := procGetDpiForWindow.Call(tv.Hwnd)
	scale := 1.0
	if sysDpi > 0 && winDpi > 0 {
		scale = float64(sysDpi) / float64(winDpi)
	} else if sysDpi > 0 {
		scale = float64(sysDpi) / 96.0
	}
	physH := float64(itemH) * scale
	r := getRect(tv.Hwnd)
	clickX := r.Left + int32(45*scale)
	clickY := r.Top + int32(float64(row)*physH+physH/2.0)
	gsClickAt(clickX, clickY, false)
	return label, nil
}

func gsReadForm(gw GuoshengWindow) map[string]string {
	res := map[string]string{}
	read := func(key string, id int32, cls string) {
		if n, ok := findVisibleCtrlByIDAndClass(gw.TradeDlg, id, cls); ok {
			res[key] = strings.TrimSpace(getWindowText(n.Hwnd))
		}
	}
	read("account", 12015, "ComboBox")
	read("stock_name", 2021, "Static")
	read("price", 12006, "Edit")
	read("qty", 12007, "Edit")
	read("avail_cash", 12028, "Static")
	read("max_qty", 2022, "Static")
	read("submit_button", 2010, "Button")
	read("refresh_button", 1140, "Button")
	return res
}

type gsOrderReq struct {
	Side          string  `json:"side"`
	Code          string  `json:"code"`
	Price         float64 `json:"price"`
	Qty           int     `json:"qty"`
	Confirm       bool    `json:"confirm"`
	ConfirmDialog bool    `json:"confirm_dialog"`
}

func gsDoPrefill(gw GuoshengWindow, req gsOrderReq) (map[string]any, error) {
	side := strings.ToLower(strings.TrimSpace(req.Side))
	if side == "" {
		side = "buy"
	}
	if side != "buy" && side != "sell" {
		return nil, fmt.Errorf("side must be buy or sell")
	}
	code := strings.TrimSpace(req.Code)
	if len(code) != 6 {
		return nil, fmt.Errorf("code must be 6 digits")
	}
	label, err := gsNavigate(gw, side)
	if err != nil {
		return nil, err
	}

	codeCtrl, ok := findVisibleCtrlByIDAndClass(gw.TradeDlg, 12005, "AfxWnd42")
	if !ok {
		return nil, fmt.Errorf("code box (12005 AfxWnd42) not found")
	}
	cr := getRect(codeCtrl.Hwnd)
	gsClickAt((cr.Left+cr.Right)/2, (cr.Top+cr.Bottom)/2, false)
	gsSendDigits(code)
	time.Sleep(650 * time.Millisecond)

	if req.Price > 0 {
		if priceCtrl, okP := findVisibleCtrlByIDAndClass(gw.TradeDlg, 12006, "Edit"); okP {
			setWindowText(priceCtrl.Hwnd, fmt.Sprintf("%.2f", req.Price))
			// Trigger Tab so TDX recalculates max_buy
			pr := getRect(priceCtrl.Hwnd)
			gsClickAt((pr.Left+pr.Right)/2, (pr.Top+pr.Bottom)/2, false)
			procKeybdEvent.Call(vkTab, 0, 0, 0)
			procKeybdEvent.Call(vkTab, 0, keyeventfKeyUp, 0)
			time.Sleep(250 * time.Millisecond)
		}
	}
	if req.Qty > 0 {
		if qtyCtrl, okQ := findVisibleCtrlByIDAndClass(gw.TradeDlg, 12007, "Edit"); okQ {
			setWindowText(qtyCtrl.Hwnd, strconv.Itoa(req.Qty))
			time.Sleep(150 * time.Millisecond)
		}
	}

	readback := gsReadForm(gw)
	return map[string]any{
		"ok":       true,
		"broker":   "guosheng",
		"nav":      label,
		"code":     code,
		"readback": readback,
	}, nil
}

func handleGSHealth(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	resp := map[string]any{
		"ok":          true,
		"broker":      "guosheng",
		"version":     agentVersion,
		"online":      ok,
		"pid":         gw.PID,
		"main_title":  gw.MainTitle,
		"trade_hwnd":  gw.TradeDlg,
		"risk":        riskMgr.Config(),
	}
	if ok {
		resp["active_dialogs"] = len(findVisibleDialogs(gw.PID))
		resp["form"] = gsReadForm(gw)
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleGSNav(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("tab"))
	if target == "" {
		target = strings.TrimSpace(r.URL.Query().Get("key"))
	}
	label, err := gsNavigate(gw, target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"broker": "guosheng",
		"nav":    label,
		"form":   gsReadForm(gw),
	})
}

func handleGSPrefill(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	var req gsOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "invalid json: " + err.Error()})
		return
	}
	res, err := gsDoPrefill(gw, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func handleGSSubmit(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	var req gsOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "invalid json: " + err.Error()})
		return
	}
	if !req.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "confirm=true is required to submit order"})
		return
	}
	if err := riskMgr.CheckPrefill(req.Side, req.Code, req.Price, req.Qty, ""); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "err": "risk rejected: " + err.Error()})
		return
	}
	prefillRes, err := gsDoPrefill(gw, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	btn, okB := findVisibleCtrlByIDAndClass(gw.TradeDlg, 2010, "Button")
	if !okB {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": "submit button (2010) not found"})
		return
	}
	clickButton(btn.Hwnd)
	time.Sleep(450 * time.Millisecond)

	var dialogs []any
	var contractNo string
	for i := 0; i < 2; i++ {
		dlgs := findVisibleDialogs(gw.PID)
		if len(dlgs) == 0 {
			break
		}
		if req.ConfirmDialog {
			snap, act := dismissDialog(dlgs[0], true)
			dialogs = append(dialogs, map[string]any{"dialog": snap, "action": act})
			if cn := extractContractNo(snap.Texts); cn != "" {
				contractNo = cn
			}
			time.Sleep(400 * time.Millisecond)
		} else {
			snap, _ := inspectDialog(dlgs[0])
			dialogs = append(dialogs, map[string]any{"dialog": snap, "action": "left_open"})
			break
		}
	}
	riskMgr.LogAudit("gs_submit", true, map[string]any{"side": req.Side, "code": req.Code, "price": req.Price, "qty": req.Qty, "contract_no": contractNo}, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"broker":      "guosheng",
		"prefill":     prefillRes,
		"dialogs":     dialogs,
		"contract_no": contractNo,
	})
}

func handleGSScreenshot(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	if tab := strings.TrimSpace(r.URL.Query().Get("tab")); tab != "" {
		_, _ = gsNavigate(gw, tab)
	}
	pngBytes, pw, ph, err := captureWindowPNG(gw.TradeDlg)
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

func handleGSDialogs(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	dlgs := findVisibleDialogs(gw.PID)
	snaps := make([]DialogSnapshot, 0, len(dlgs))
	for _, d := range dlgs {
		s, _ := inspectDialog(d)
		snaps = append(snaps, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "broker": "guosheng", "dialogs": snaps})
}

func handleGSDialogDismiss(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	accept := r.URL.Query().Get("accept") != "0"
	dlgs := findVisibleDialogs(gw.PID)
	if len(dlgs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": 0})
		return
	}
	snap, act := dismissDialog(dlgs[0], accept)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "broker": "guosheng", "dialog": snap, "action": act})
}

type gsCancelReq struct {
	Scope         string `json:"scope"` // "all" (default)
	Confirm       bool   `json:"confirm"`
	ConfirmDialog bool   `json:"confirm_dialog"`
}

func handleGSCancel(w http.ResponseWriter, r *http.Request) {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "guosheng trade panel not found"})
		return
	}
	var req gsCancelReq
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if !req.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "confirm=true is required to cancel orders"})
		return
	}
	if _, err := gsNavigate(gw, "cancel"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	if refBtn, okR := findVisibleCtrlByIDAndClass(gw.TradeDlg, 1140, "Button"); okR {
		clickButton(refBtn.Hwnd)
		time.Sleep(350 * time.Millisecond)
	}
	if selAll, okS := findVisibleCtrlByIDAndClass(gw.TradeDlg, 14, "Button"); okS {
		clickButton(selAll.Hwnd)
		time.Sleep(200 * time.Millisecond)
	}
	cancelBtn, okC := findVisibleCtrlByIDAndClass(gw.TradeDlg, 1136, "Button")
	if !okC {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": "cancel button (1136) not found"})
		return
	}
	clickButton(cancelBtn.Hwnd)
	time.Sleep(450 * time.Millisecond)

	var dialogs []any
	for i := 0; i < 2; i++ {
		dlgs := findVisibleDialogs(gw.PID)
		if len(dlgs) == 0 {
			break
		}
		if req.ConfirmDialog {
			snap, act := dismissDialog(dlgs[0], true)
			dialogs = append(dialogs, map[string]any{"dialog": snap, "action": act})
			time.Sleep(350 * time.Millisecond)
		} else {
			snap, _ := inspectDialog(dlgs[0])
			dialogs = append(dialogs, map[string]any{"dialog": snap, "action": "left_open"})
			break
		}
	}
	riskMgr.LogAudit("gs_cancel", true, map[string]any{"scope": req.Scope, "dialogs": len(dialogs)}, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "broker": "guosheng", "dialogs": dialogs})
}

func gsKeepalivePing() string {
	gw, ok := findGuoshengTradeWindow()
	if !ok {
		return "offline"
	}
	if len(findVisibleDialogs(gw.PID)) > 0 {
		return "paused_dialog"
	}
	if refBtn, okR := findVisibleCtrlByIDAndClass(gw.TradeDlg, 1140, "Button"); okR {
		clickButton(refBtn.Hwnd)
		return "ok_btn_1140"
	}
	return "online_no_refresh_btn"
}

// Keep unused syscall import check happy if needed
var _ = syscall.UTF16ToString
