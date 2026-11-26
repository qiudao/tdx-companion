//go:build windows

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"syscall"
	"unsafe"
)

var (
	procGetMenu          = user32.NewProc("GetMenu")
	procGetSubMenu       = user32.NewProc("GetSubMenu")
	procGetMenuItemCount = user32.NewProc("GetMenuItemCount")
	procGetMenuState     = user32.NewProc("GetMenuState")
	procGetMenuStringW   = user32.NewProc("GetMenuStringW")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procGetDlgCtrlID     = user32.NewProc("GetDlgCtrlID")
	procGetWindowLongW   = user32.NewProc("GetWindowLongW")
)

const (
	mfByPosition = 0x00000400
	mfChecked    = 0x00000008
	wmInitMenu   = 0x0116
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

type childWinInfo struct {
	Class  string `json:"class"`
	Text   string `json:"text"`
	CtrlID int    `json:"ctrl_id"`
	W      int32  `json:"w"`
	H      int32  `json:"h"`
	X      int32  `json:"x"`
	Y      int32  `json:"y"`
}

func getWinText(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return strings.TrimSpace(syscall.UTF16ToString(buf[:n]))
}

func getWinClass(hwnd uintptr) string {
	var buf [128]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func collectCheckedMenus(hMenu uintptr, prefix string, depth int, out *[]string) {
	if hMenu == 0 || depth > 4 {
		return
	}
	cntRaw, _, _ := procGetMenuItemCount.Call(hMenu)
	cnt := int(int32(cntRaw))
	if cnt <= 0 || cnt > 200 {
		return
	}
	var buf [128]uint16
	for i := 0; i < cnt; i++ {
		n, _, _ := procGetMenuStringW.Call(hMenu, uintptr(i), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), mfByPosition)
		label := ""
		if n > 0 {
			label = strings.TrimSpace(syscall.UTF16ToString(buf[:n]))
		}
		full := label
		if prefix != "" && label != "" {
			full = prefix + " > " + label
		}
		state, _, _ := procGetMenuState.Call(hMenu, uintptr(i), mfByPosition)
		if state != 0xFFFFFFFF && (state&mfChecked) != 0 && full != "" {
			*out = append(*out, full)
		}
		sub, _, _ := procGetSubMenu.Call(hMenu, uintptr(i))
		if sub != 0 {
			collectCheckedMenus(sub, full, depth+1, out)
		}
	}
}

func collectVisibleChildren(root uintptr, maxNodes int) []childWinInfo {
	var out []childWinInfo
	queue := []uintptr{root}
	for len(queue) > 0 && len(out) < maxNodes {
		cur := queue[0]
		queue = queue[1:]
		var ch uintptr
		for {
			ch, _, _ = procFindWindowExW.Call(cur, ch, 0, 0)
			if ch == 0 {
				break
			}
			vis, _, _ := procIsWindowVisible.Call(ch)
			if vis == 0 {
				continue
			}
			var r winRect
			procGetWindowRect.Call(ch, uintptr(unsafe.Pointer(&r)))
			w := r.Right - r.Left
			h := r.Bottom - r.Top
			if w > 0 && h > 0 {
				cid, _, _ := procGetDlgCtrlID.Call(ch)
				out = append(out, childWinInfo{
					Class:  getWinClass(ch),
					Text:   getWinText(ch),
					CtrlID: int(int32(cid)),
					W:      w,
					H:      h,
					X:      r.Left,
					Y:      r.Top,
				})
				if len(out) >= maxNodes {
					break
				}
			}
			queue = append(queue, ch)
		}
	}
	return out
}

func detectTdxChartType(hwnd uintptr, title string) string {
	lb := strings.LastIndex(title, "[")
	rb := strings.LastIndex(title, "]")
	inner := ""
	if lb >= 0 && rb > lb {
		inner = strings.TrimSpace(title[lb+1 : rb])
	}
	if strings.Contains(inner, "行情报价") || strings.Contains(inner, "综合排名") {
		return "quote"
	}
	if strings.Contains(inner, "基本资料") || strings.Contains(strings.ToUpper(inner), "F10") {
		return "f10"
	}
	if strings.Contains(inner, "分时") {
		return "min"
	}
	if !strings.Contains(inner, "分析图表") {
		return "other"
	}
	// Inside "[分析图表-...]", Tongdaxin shows different bottom CFQS_SwitchEx controls:
	//   K-line view:  ctrl_id 216 / 217 visible (no 215/4045)
	//   Intraday min: ctrl_id 215 / 4045 visible (no 216/217)
	if hwnd != 0 {
		children := collectVisibleChildren(hwnd, 80)
		hasKlineCtrl := false
		hasMinCtrl := false
		for _, c := range children {
			if c.Class != "CFQS_SwitchEx" {
				continue
			}
			if c.CtrlID == 216 || c.CtrlID == 217 {
				hasKlineCtrl = true
			} else if c.CtrlID == 215 || c.CtrlID == 4045 {
				hasMinCtrl = true
			}
		}
		if hasKlineCtrl && !hasMinCtrl {
			return "kline"
		}
		if hasMinCtrl && !hasKlineCtrl {
			return "min"
		}
	}
	return "kline"
}

func handleDebugTdxView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	hwnd, title := findTdxWindow()
	if hwnd == 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "TdxW not found"})
		return
	}
	var checked []string
	hMenu, _, _ := procGetMenu.Call(hwnd)
	if hMenu != 0 {
		collectCheckedMenus(hMenu, "", 0, &checked)
	}
	children := collectVisibleChildren(hwnd, 60)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":            true,
		"title":         title,
		"chart_type":    detectTdxChartType(hwnd, title),
		"has_menu":      hMenu != 0,
		"checked_menus": checked,
		"children":      children,
	})
}
