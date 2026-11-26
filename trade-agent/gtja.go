//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runGTJAPowerShell(script string, timeoutSec int) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	full := "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8\n" +
		"$ErrorActionPreference='Stop'\n" + script
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", full)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	outStr := strings.TrimSpace(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("gtja uia error: %v (stderr=%s stdout=%s)", err, strings.TrimSpace(stderr.String()), outStr)
	}
	if outStr == "" {
		return map[string]any{"ok": true}, nil
	}
	// Parse last JSON line
	lines := strings.Split(outStr, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") {
			var res map[string]any
			if jerr := json.Unmarshal([]byte(line), &res); jerr == nil {
				return res, nil
			}
		}
	}
	return map[string]any{"ok": true, "raw": outStr}, nil
}

const gtjaPSHeader = `
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName System.Windows.Forms
$code = @"
using System; using System.Threading; using System.Runtime.InteropServices;
public class GTJAUtil {
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool BringWindowToTop(IntPtr h);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int n);
  [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
  [DllImport("user32.dll")] public static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, UIntPtr dwExtraInfo);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, UIntPtr e);
  public static void Focus(IntPtr h) {
    SetProcessDPIAware();
    if (IsIconic(h)) { ShowWindow(h, 9); Thread.Sleep(150); }
    keybd_event(0x12, 0, 0, UIntPtr.Zero); keybd_event(0x12, 0, 2, UIntPtr.Zero);
    BringWindowToTop(h); SetForegroundWindow(h);
    Thread.Sleep(200);
  }
  public static void ClickAt(int x, int y) {
    SetCursorPos(x, y); Thread.Sleep(40);
    mouse_event(2, 0, 0, 0, UIntPtr.Zero); Thread.Sleep(25); mouse_event(4, 0, 0, 0, UIntPtr.Zero);
    Thread.Sleep(120);
  }
}
"@
Add-Type $code
function Get-GTJAProc {
  Get-Process -Name jy -EA SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
}
function Read-GTJASummary($root) {
  $all = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
  $summary = [ordered]@{}
  $texts = @()
  foreach ($it in $all) {
    $ct = $it.Current.ControlType.ProgrammaticName.Replace("ControlType.","")
    $nm = $it.Current.Name
    if ($ct -eq "Text" -and $nm -ne "") {
      if ($nm -match "总资产|可用：|可用:|两融-|普通买入|普通卖出|撤单|可买|可卖") {
        $texts += $nm
      }
    }
    if ($ct -eq "Button" -and ($nm -eq "普通买入" -or $nm -eq "普通卖出" -or $nm -eq "撤单")) {
      $summary["submit_button"] = $nm
    }
  }
  $summary["texts"] = $texts
  return $summary
}
`

func gtjaNavKey(tab string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "buy", "f1":
		return "{F1}", "普通买入(F1)", nil
	case "sell", "f2":
		return "{F2}", "普通卖出(F2)", nil
	case "cancel", "f3":
		return "{F3}", "撤单(F3)", nil
	case "holdings", "query", "f4":
		return "{F4}", "资金股票(F4)", nil
	default:
		return "", "", fmt.Errorf("unsupported gtja tab %q (use buy|sell|cancel|holdings)", tab)
	}
}

func handleGTJAHealth(w http.ResponseWriter, r *http.Request) {
	script := gtjaPSHeader + `
$p = Get-GTJAProc
if (-not $p) {
  @{ok=$true; broker="gtja"; online=$false; pid=0} | ConvertTo-Json -Compress
  exit 0
}
$root = [System.Windows.Automation.AutomationElement]::FromHandle($p.MainWindowHandle)
$sum = Read-GTJASummary $root
@{ok=$true; broker="gtja"; online=$true; pid=$p.Id; title=$p.MainWindowTitle; hwnd=[int64]$p.MainWindowHandle; summary=$sum} | ConvertTo-Json -Compress -Depth 5
`
	res, err := runGTJAPowerShell(script, 12)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	res["version"] = agentVersion
	res["risk"] = riskMgr.Config()
	writeJSON(w, http.StatusOK, res)
}

func handleGTJANav(w http.ResponseWriter, r *http.Request) {
	tab := strings.TrimSpace(r.URL.Query().Get("tab"))
	if tab == "" {
		tab = strings.TrimSpace(r.URL.Query().Get("key"))
	}
	fk, label, err := gtjaNavKey(tab)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	script := gtjaPSHeader + fmt.Sprintf(`
$p = Get-GTJAProc
if (-not $p) { @{ok=$false; err="gtja jy.exe not found"} | ConvertTo-Json -Compress; exit 0 }
[GTJAUtil]::Focus($p.MainWindowHandle)
[System.Windows.Forms.SendKeys]::SendWait("%s")
Start-Sleep -Milliseconds 450
$root = [System.Windows.Automation.AutomationElement]::FromHandle($p.MainWindowHandle)
$sum = Read-GTJASummary $root
@{ok=$true; broker="gtja"; nav="%s"; summary=$sum} | ConvertTo-Json -Compress -Depth 5
`, fk, label)
	res, err := runGTJAPowerShell(script, 12)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type gtjaOrderReq struct {
	Side          string  `json:"side"`
	Code          string  `json:"code"`
	Price         float64 `json:"price"`
	Qty           int     `json:"qty"`
	Confirm       bool    `json:"confirm"`
	ConfirmDialog bool    `json:"confirm_dialog"`
}

func gtjaRunPrefillOrSubmit(req gtjaOrderReq, doSubmit bool) (map[string]any, error) {
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
	fk := "{F1}"
	btnName := "普通买入"
	if side == "sell" {
		fk = "{F2}"
		btnName = "普通卖出"
	}
	priceStr := ""
	if req.Price > 0 {
		priceStr = fmt.Sprintf("%.2f", req.Price)
	}
	qtyStr := ""
	if req.Qty > 0 {
		qtyStr = strconv.Itoa(req.Qty)
	}

	script := gtjaPSHeader + fmt.Sprintf(`
$p = Get-GTJAProc
if (-not $p) { @{ok=$false; err="gtja jy.exe not found"} | ConvertTo-Json -Compress; exit 0 }
[GTJAUtil]::Focus($p.MainWindowHandle)
[System.Windows.Forms.SendKeys]::SendWait("%s")
Start-Sleep -Milliseconds 450

$root = [System.Windows.Automation.AutomationElement]::FromHandle($p.MainWindowHandle)
$all = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)

$codeEdit = $null; $priceLbl = $null; $qtyLbl = $null; $submitBtn = $null; $nameLbl = $null
foreach ($it in $all) {
  $ct = $it.Current.ControlType.ProgrammaticName.Replace("ControlType.","")
  $nm = $it.Current.Name
  $r = $it.Current.BoundingRectangle
  if ($r.Width -le 0 -or $r.Height -le 0) { continue }
  if ($ct -eq "Edit" -and $r.X -ge 350 -and $r.X -le 600 -and $r.Y -ge 260 -and $r.Y -le 420 -and -not $codeEdit) { $codeEdit = $it }
  if ($ct -eq "Text" -and $nm -eq "名称" -and -not $nameLbl) { $nameLbl = $it }
  if ($ct -eq "Text" -and $nm -eq "价格" -and -not $priceLbl) { $priceLbl = $it }
  if (($ct -eq "CheckBox" -or $ct -eq "Text") -and $nm -eq "数量" -and -not $qtyLbl) { $qtyLbl = $it }
  if ($ct -eq "Button" -and $nm -eq "%s" -and -not $submitBtn) { $submitBtn = $it }
}
if (-not $codeEdit) { @{ok=$false; err="gtja code Edit not found"} | ConvertTo-Json -Compress; exit 0 }

$cr = $codeEdit.Current.BoundingRectangle
$inputX = [int]($cr.X + 80)

# 1. Fill code
[GTJAUtil]::ClickAt($inputX, [int]($cr.Y + $cr.Height / 2))
[System.Windows.Forms.SendKeys]::SendWait("^a{BACKSPACE}%s")
Start-Sleep -Milliseconds 700

# 2. Fill price if provided
if ("%s" -ne "" -and $priceLbl) {
  $pr = $priceLbl.Current.BoundingRectangle
  [GTJAUtil]::ClickAt($inputX, [int]($pr.Y + $pr.Height / 2))
  [System.Windows.Forms.SendKeys]::SendWait("^a{BACKSPACE}%s")
  Start-Sleep -Milliseconds 300
}

# 3. Fill qty if provided
if ("%s" -ne "" -and $qtyLbl) {
  $qr = $qtyLbl.Current.BoundingRectangle
  [GTJAUtil]::ClickAt($inputX, [int]($qr.Y + $qr.Height / 2))
  [System.Windows.Forms.SendKeys]::SendWait("^a{BACKSPACE}%s")
  Start-Sleep -Milliseconds 250
}

# Readback form texts near input panel
$all2 = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
$formTexts = @()
$codeVal = ""
foreach ($it in $all2) {
  $ct = $it.Current.ControlType.ProgrammaticName.Replace("ControlType.","")
  $nm = $it.Current.Name
  $r = $it.Current.BoundingRectangle
  if ($r.Width -le 0 -or $r.Height -le 0) { continue }
  if ($ct -eq "Edit" -and [Math]::Abs($r.Y - $cr.Y) -lt 10) {
    $vp = $null
    if ($it.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$vp)) { $codeVal = $vp.Current.Value }
  }
  if ($ct -eq "Text" -and $nm -ne "" -and $r.X -ge 330 -and $r.X -le 900 -and $r.Y -ge 240 -and $r.Y -le 780) {
    $formTexts += $nm
  }
}

$submitted = $false
if ("%v" -eq "true" -and $submitBtn) {
  $sr = $submitBtn.Current.BoundingRectangle
  [GTJAUtil]::ClickAt([int]($sr.X + $sr.Width / 2), [int]($sr.Y + $sr.Height / 2))
  Start-Sleep -Milliseconds 500
  $submitted = $true
}

$sum = Read-GTJASummary $root
@{ok=$true; broker="gtja"; side="%s"; code="%s"; code_val=$codeVal; price="%s"; qty="%s"; submitted=$submitted; form_texts=$formTexts; summary=$sum} | ConvertTo-Json -Compress -Depth 5
`, fk, btnName, code, priceStr, priceStr, qtyStr, qtyStr, doSubmit, side, code, priceStr, qtyStr)

	return runGTJAPowerShell(script, 18)
}

func handleGTJAPrefill(w http.ResponseWriter, r *http.Request) {
	var req gtjaOrderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": "invalid json: " + err.Error()})
		return
	}
	res, err := gtjaRunPrefillOrSubmit(req, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func handleGTJASubmit(w http.ResponseWriter, r *http.Request) {
	var req gtjaOrderReq
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
	res, err := gtjaRunPrefillOrSubmit(req, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	riskMgr.LogAudit("gtja_submit", true, map[string]any{"side": req.Side, "code": req.Code, "price": req.Price, "qty": req.Qty}, "")
	writeJSON(w, http.StatusOK, res)
}

func handleGTJAScreenshot(w http.ResponseWriter, r *http.Request) {
	pid := findProcessPID("jy.exe")
	if pid == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "gtja jy.exe not found"})
		return
	}
	tops := findTopWindowsByPID(pid, false)
	var mainHwnd uintptr
	for _, t := range tops {
		if strings.Contains(t.Text, "国泰海通") || strings.Contains(t.Class, "Qt5") {
			mainHwnd = t.Hwnd
			break
		}
	}
	if mainHwnd == 0 && len(tops) > 0 {
		mainHwnd = tops[0].Hwnd
	}
	if mainHwnd == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "err": "gtja main window not found"})
		return
	}
	pngBytes, pw, ph, err := captureWindowPNG(mainHwnd)
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
