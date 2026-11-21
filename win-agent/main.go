//go:build windows

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	procFindWindowExW      = user32.NewProc("FindWindowExW")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procRegisterWindowMsgW = user32.NewProc("RegisterWindowMessageW")
	procPostMessageW       = user32.NewProc("PostMessageW")
	uwmStock               uintptr
)

const tdxRoot = `C:\new_tdx\T0002`

func init() {
	ptr, _ := syscall.UTF16PtrFromString("Stock")
	uwmStock, _, _ = procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(ptr)))
}

func findTdxWindow() (uintptr, string) {
	var hwnd uintptr
	var buf [256]uint16
	for {
		hwnd, _, _ = procFindWindowExW.Call(0, hwnd, 0, 0)
		if hwnd == 0 {
			return 0, ""
		}
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			continue
		}
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 {
			title := syscall.UTF16ToString(buf[:n])
			if strings.Contains(title, "通达信") {
				return hwnd, title
			}
		}
	}
}

func extractStockName(title string) string {
	lb := strings.LastIndex(title, "[")
	rb := strings.LastIndex(title, "]")
	if lb < 0 || rb <= lb {
		return ""
	}
	inner := strings.TrimSpace(title[lb+1 : rb])
	// Handle "分析图表-文投控股" or "分时图-九阳股份"
	if dash := strings.Index(inner, "-"); dash >= 0 {
		prefix := inner[:dash]
		if strings.Contains(prefix, "行情报价") || strings.Contains(prefix, "综合排名") {
			return ""
		}
		return strings.TrimSpace(inner[dash+1:])
	}
	return ""
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	hwnd, title := findTdxWindow()
	resp := map[string]any{
		"ok":    hwnd != 0,
		"title": title,
		"stock": extractStockName(title),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func handleSwitch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if len(key) != 7 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "invalid 7-char key"})
		return
	}
	codeNum, err := strconv.ParseInt(key[1:], 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "invalid code"})
		return
	}
	var wParam int64
	switch key[0] {
	case '1':
		wParam = 7000000 + codeNum
	case '0':
		wParam = 6000000 + codeNum
	case '2':
		wParam = 4000000 + codeNum
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "unknown market"})
		return
	}

	hwnd, _ := findTdxWindow()
	if hwnd == 0 {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "TdxW window not found"})
		return
	}
	ret, _, _ := procPostMessageW.Call(hwnd, uwmStock, uintptr(wParam), 0)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": ret != 0, "wparam": wParam})
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("name")))
	allow := map[string]string{
		"zxg.blk":              filepath.Join(tdxRoot, "blocknew", "zxg.blk"),
		"LSNG.blk":             filepath.Join(tdxRoot, "blocknew", "LSNG.blk"),
		"shs.tnf":              filepath.Join(tdxRoot, "hq_cache", "shs.tnf"),
		"szs.tnf":              filepath.Join(tdxRoot, "hq_cache", "szs.tnf"),
		"bjs.tnf":              filepath.Join(tdxRoot, "hq_cache", "bjs.tnf"),
		"tdxhy.cfg":            filepath.Join(tdxRoot, "hq_cache", "tdxhy.cfg"),
		"tdxzs3.cfg":           filepath.Join(tdxRoot, "hq_cache", "tdxzs3.cfg"),
		"infoharbor_block.dat": filepath.Join(tdxRoot, "hq_cache", "infoharbor_block.dat"),
	}
	path, ok := allow[name]
	if !ok {
		http.Error(w, "file not allowed", http.StatusForbidden)
		return
	}
	http.ServeFile(w, r, path)
}

func handleWriteBlk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	var safe strings.Builder
	for _, ch := range name {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			safe.WriteRune(ch)
		}
	}
	if safe.Len() == 0 {
		http.Error(w, "invalid block name", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dst := filepath.Join(tdxRoot, "blocknew", safe.String()+".blk")
	if err := os.WriteFile(dst, body, 0644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": dst})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", handleStatus)
	mux.HandleFunc("/switch", handleSwitch)
	mux.HandleFunc("/file", handleFile)
	mux.HandleFunc("/blk", handleWriteBlk)

	_ = http.ListenAndServe("0.0.0.0:19876", mux)
}
