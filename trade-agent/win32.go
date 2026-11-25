//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procFindWindowExW       = user32.NewProc("FindWindowExW")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procGetClassNameW       = user32.NewProc("GetClassNameW")
	procGetDlgCtrlID        = user32.NewProc("GetDlgCtrlID")
	procGetParent           = user32.NewProc("GetParent")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procIsWindowEnabled     = user32.NewProc("IsWindowEnabled")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procKeybdEvent          = user32.NewProc("keybd_event")
	procSetFocus            = user32.NewProc("SetFocus")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procGetClipboardData    = user32.NewProc("GetClipboardData")
	procGlobalLock          = kernel32.NewProc("GlobalLock")
	procGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	procCreateToolhelp32    = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW     = kernel32.NewProc("Process32FirstW")
	procProcess32NextW      = kernel32.NewProc("Process32NextW")
)

const (
	wmGetText       = 0x000D
	wmGetTextLength = 0x000E
	wmSetText       = 0x000C
	wmKeyDown       = 0x0100
	wmKeyUp         = 0x0101
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	bmClick         = 0x00F5
	swRestore       = 9
	keyeventfKeyUp  = 0x0002
	cfUnicodeText   = 13

	vkControl = 0x11
	vkC       = 0x43
	vkF1      = 0x70
	vkF2      = 0x71
	vkF3      = 0x72
	vkF4      = 0x73
	vkF5      = 0x74
)

type Rect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
}

type WinNode struct {
	Hwnd       uintptr `json:"hwnd"`
	ParentHwnd uintptr `json:"parent_hwnd"`
	CtrlID     int32   `json:"ctrl_id"`
	Class      string  `json:"class"`
	Text       string  `json:"text"`
	Visible    bool    `json:"visible"`
	Enabled    bool    `json:"enabled"`
	Rect       Rect    `json:"rect"`
}

type processEntry32W struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

func findProcessPID(exeName string) uint32 {
	snap, _, _ := procCreateToolhelp32.Call(0x00000002, 0) // TH32CS_SNAPPROCESS
	if snap == 0 || snap == ^uintptr(0) {
		return 0
	}
	defer syscall.CloseHandle(syscall.Handle(snap))

	var pe processEntry32W
	pe.Size = uint32(unsafe.Sizeof(pe))
	ret, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	for ret != 0 {
		name := syscall.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(name, exeName) {
			return pe.ProcessID
		}
		ret, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe)))
	}
	return 0
}

func getClassName(hwnd uintptr) string {
	var buf [128]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func getWindowText(hwnd uintptr) string {
	// Use SendMessageTimeoutW (SMTO_ABORTIFHUNG=0x0002, 120ms) to safely read Edit/Static text
	var length uintptr
	ok, _, _ := procSendMessageTimeoutW.Call(hwnd, wmGetTextLength, 0, 0, 0x0002, 120, uintptr(unsafe.Pointer(&length)))
	if ok != 0 && length > 0 && length < 4096 {
		buf := make([]uint16, length+2)
		var n uintptr
		ok2, _, _ := procSendMessageTimeoutW.Call(hwnd, wmGetText, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), 0x0002, 120, uintptr(unsafe.Pointer(&n)))
		if ok2 != 0 && n > 0 {
			if n > uintptr(len(buf)) {
				n = uintptr(len(buf))
			}
			return syscall.UTF16ToString(buf[:n])
		}
	}
	var buf [256]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	if n > uintptr(len(buf)) {
		n = uintptr(len(buf))
	}
	return syscall.UTF16ToString(buf[:n])
}

func setWindowText(hwnd uintptr, text string) bool {
	ptr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return false
	}
	ret, _, _ := procSendMessageW.Call(hwnd, wmSetText, 0, uintptr(unsafe.Pointer(ptr)))
	return ret != 0
}

func isVisible(hwnd uintptr) bool {
	v, _, _ := procIsWindowVisible.Call(hwnd)
	return v != 0
}

func isEnabled(hwnd uintptr) bool {
	v, _, _ := procIsWindowEnabled.Call(hwnd)
	return v != 0
}

func getRect(hwnd uintptr) Rect {
	var r Rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func getCtrlID(hwnd uintptr) int32 {
	v, _, _ := procGetDlgCtrlID.Call(hwnd)
	return int32(v)
}

func getParent(hwnd uintptr) uintptr {
	v, _, _ := procGetParent.Call(hwnd)
	return v
}

func inspectNode(hwnd uintptr) WinNode {
	return WinNode{
		Hwnd:       hwnd,
		ParentHwnd: getParent(hwnd),
		CtrlID:     getCtrlID(hwnd),
		Class:      getClassName(hwnd),
		Text:       strings.TrimSpace(getWindowText(hwnd)),
		Visible:    isVisible(hwnd),
		Enabled:    isEnabled(hwnd),
		Rect:       getRect(hwnd),
	}
}

func findTopWindowsByPID(targetPID uint32, includeHidden bool) []WinNode {
	var res []WinNode
	if targetPID == 0 {
		return res
	}
	var hwnd uintptr
	for {
		hwnd, _, _ = procFindWindowExW.Call(0, hwnd, 0, 0)
		if hwnd == 0 {
			break
		}
		var pid uint32
		procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == targetPID {
			if includeHidden || isVisible(hwnd) {
				res = append(res, inspectNode(hwnd))
			}
		}
	}
	return res
}

func findMainXiadanWindow(targetPID uint32) (WinNode, bool) {
	tops := findTopWindowsByPID(targetPID, false)
	var best WinNode
	found := false
	for _, w := range tops {
		if strings.Contains(w.Text, "网上股票交易系统") || strings.Contains(w.Text, "交易系统") || strings.Contains(w.Text, "金融终端") || strings.Contains(w.Class, "Afx:") {
			area := (w.Rect.Right - w.Rect.Left) * (w.Rect.Bottom - w.Rect.Top)
			bestArea := (best.Rect.Right - best.Rect.Left) * (best.Rect.Bottom - best.Rect.Top)
			if !found || area > bestArea {
				best = w
				found = true
			}
		}
	}
	if !found && len(tops) > 0 {
		return tops[0], true
	}
	return best, found
}

func enumChildNodes(parentHwnd uintptr, includeHidden bool) []WinNode {
	var res []WinNode
	if parentHwnd == 0 {
		return res
	}
	// Iterative BFS using FindWindowExW — zero syscall.NewCallback allocations
	queue := []uintptr{parentHwnd}
	visited := map[uintptr]bool{parentHwnd: true}
	for len(queue) > 0 && len(res) < 2000 {
		cur := queue[0]
		queue = queue[1:]
		var ch uintptr
		for {
			ch, _, _ = procFindWindowExW.Call(cur, ch, 0, 0)
			if ch == 0 {
				break
			}
			if visited[ch] {
				continue
			}
			visited[ch] = true
			vis := isVisible(ch)
			if includeHidden || vis {
				res = append(res, inspectNode(ch))
				queue = append(queue, ch)
			}
		}
	}
	return res
}

func findVisibleDialogs(targetPID uint32) []WinNode {
	tops := findTopWindowsByPID(targetPID, false)
	var dialogs []WinNode
	for _, w := range tops {
		if w.Class == "#32770" {
			dialogs = append(dialogs, w)
		}
	}
	return dialogs
}

func focusWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}

func sendNavKey(hwnd uintptr, keyName string) error {
	var vk uintptr
	switch strings.ToUpper(strings.TrimSpace(keyName)) {
	case "F1":
		vk = vkF1
	case "F2":
		vk = vkF2
	case "F3":
		vk = vkF3
	case "F4":
		vk = vkF4
	case "F5":
		vk = vkF5
	default:
		return fmt.Errorf("unsupported nav key: %s (expected F1..F5)", keyName)
	}
	focusWindow(hwnd)
	time.Sleep(80 * time.Millisecond)
	procKeybdEvent.Call(vk, 0, 0, 0)
	time.Sleep(30 * time.Millisecond)
	procKeybdEvent.Call(vk, 0, keyeventfKeyUp, 0)
	time.Sleep(150 * time.Millisecond)
	return nil
}

func findVisibleEditByCtrlID(parentHwnd uintptr, ctrlID int32) (WinNode, bool) {
	children := enumChildNodes(parentHwnd, false)
	for _, ch := range children {
		if ch.CtrlID == ctrlID && strings.EqualFold(ch.Class, "Edit") {
			return ch, true
		}
	}
	return WinNode{}, false
}

func findVisibleCtrlByIDAndClass(parentHwnd uintptr, ctrlID int32, className string) (WinNode, bool) {
	children := enumChildNodes(parentHwnd, false)
	for _, ch := range children {
		if ch.CtrlID == ctrlID && (className == "" || strings.EqualFold(ch.Class, className)) {
			return ch, true
		}
	}
	return WinNode{}, false
}

func clickButton(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	procPostMessageW.Call(hwnd, bmClick, 0, 0)
}

func clearClipboard() {
	if r, _, _ := procOpenClipboard.Call(0); r != 0 {
		procEmptyClipboard.Call()
		procCloseClipboard.Call()
	}
}

func readClipboardText() string {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)
	// Convert null-terminated UTF-16LE buffer safely (cap at 512K chars)
	var u16 []uint16
	const maxChars = 512 * 1024
	for i := 0; i < maxChars; i++ {
		ch := *(*uint16)(unsafe.Pointer(ptr + uintptr(i)*2))
		if ch == 0 {
			break
		}
		u16 = append(u16, ch)
	}
	return syscall.UTF16ToString(u16)
}

func copyGridTSV(pid uint32, mainHwnd uintptr) (string, []WinNode, error) {
	grid, ok := findVisibleCtrlByIDAndClass(mainHwnd, 1047, "CVirtualGridCtrl")
	if !ok {
		return "", nil, fmt.Errorf("CVirtualGridCtrl (id=1047) not found on current panel")
	}
	clearClipboard()
	focusWindow(mainHwnd)
	time.Sleep(60 * time.Millisecond)
	// Click inside grid to focus it cleanly
	procSendMessageW.Call(grid.Hwnd, wmLButtonDown, 1, uintptr(10|(10<<16)))
	procSendMessageW.Call(grid.Hwnd, wmLButtonUp, 0, uintptr(10|(10<<16)))
	time.Sleep(80 * time.Millisecond)

	// Send Ctrl+C
	procKeybdEvent.Call(vkControl, 0, 0, 0)
	time.Sleep(20 * time.Millisecond)
	procKeybdEvent.Call(vkC, 0, 0, 0)
	time.Sleep(30 * time.Millisecond)
	procKeybdEvent.Call(vkC, 0, keyeventfKeyUp, 0)
	procKeybdEvent.Call(vkControl, 0, keyeventfKeyUp, 0)
	time.Sleep(220 * time.Millisecond)

	if dlgs := findVisibleDialogs(pid); len(dlgs) > 0 {
		return "", dlgs, fmt.Errorf("dialog appeared during grid copy (e.g. captcha/notice)")
	}
	return readClipboardText(), nil, nil
}

func parseTSVRows(tsv string) ([]string, []map[string]string) {
	tsv = strings.ReplaceAll(tsv, "\r\n", "\n")
	tsv = strings.TrimSpace(tsv)
	if tsv == "" {
		return nil, nil
	}
	lines := strings.Split(tsv, "\n")
	if len(lines) == 0 {
		return nil, nil
	}
	headers := strings.Split(lines[0], "\t")
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	rows := make([]map[string]string, 0, len(lines)-1)
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		cols := strings.Split(ln, "\t")
		row := make(map[string]string, len(headers))
		for i, h := range headers {
			if h == "" {
				continue
			}
			val := ""
			if i < len(cols) {
				val = strings.TrimSpace(cols[i])
			}
			row[h] = val
		}
		rows = append(rows, row)
	}
	return headers, rows
}

type DialogSnapshot struct {
	Title   string   `json:"title"`
	Texts   []string `json:"texts"`
	Buttons []string `json:"buttons"`
	Hwnd    uintptr  `json:"hwnd"`
}

func inspectDialog(dlg WinNode) (DialogSnapshot, []WinNode) {
	children := enumChildNodes(dlg.Hwnd, false)
	snap := DialogSnapshot{
		Title: dlg.Text,
		Hwnd:  dlg.Hwnd,
	}
	for _, ch := range children {
		if ch.Text == "" {
			continue
		}
		if strings.EqualFold(ch.Class, "Static") {
			snap.Texts = append(snap.Texts, ch.Text)
		} else if strings.EqualFold(ch.Class, "Button") {
			snap.Buttons = append(snap.Buttons, fmt.Sprintf("%s(id=%d)", ch.Text, ch.CtrlID))
		}
	}
	return snap, children
}

func dismissDialog(dlg WinNode, accept bool) (DialogSnapshot, string) {
	snap, children := inspectDialog(dlg)
	var target WinNode
	found := false
	for _, ch := range children {
		if !strings.EqualFold(ch.Class, "Button") {
			continue
		}
		t := ch.Text
		if accept {
			if ch.CtrlID == 6 || ch.CtrlID == 1 || strings.Contains(t, "是") || strings.Contains(t, "确定") {
				target = ch
				found = true
				break
			}
		} else {
			if ch.CtrlID == 7 || ch.CtrlID == 2 || strings.Contains(t, "否") || strings.Contains(t, "取消") {
				target = ch
				found = true
				break
			}
		}
	}
	if !found && accept {
		// Fallback to single OK/confirm button
		for _, ch := range children {
			if strings.EqualFold(ch.Class, "Button") && ch.Enabled {
				target = ch
				found = true
				break
			}
		}
	}
	if found {
		clickButton(target.Hwnd)
		return snap, fmt.Sprintf("clicked %s (id=%d)", target.Text, target.CtrlID)
	}
	return snap, "no matching button"
}
