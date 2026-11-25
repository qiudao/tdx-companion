//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RiskConfig struct {
	Mode            string  `json:"mode"`              // "prefill_only" (safe default)
	Halted          bool    `json:"halted"`            // kill switch
	MaxSingleAmount float64 `json:"max_single_amount"` // CNY cap per order
	MaxDailyOrders  int     `json:"max_daily_orders"`
	CooldownSec     int     `json:"cooldown_sec"`
}

type AuditRecord struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Detail any    `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

type RiskManager struct {
	mu           sync.Mutex
	cfg          RiskConfig
	dailyDate    string
	dailyCount   int
	lastByKey    map[string]time.Time
	seenIdemp    map[string]time.Time
	auditLogPath string
}

func NewRiskManager() *RiskManager {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		dir = `C:\zsjrxd_zxzq`
	} else {
		dir = filepath.Join(dir, "trade-agent")
	}
	_ = os.MkdirAll(dir, 0755)
	return &RiskManager{
		cfg: RiskConfig{
			Mode:            "prefill_only",
			Halted:          false,
			MaxSingleAmount: 100000.0,
			MaxDailyOrders:  30,
			CooldownSec:     3,
		},
		dailyDate:    time.Now().Format("20060102"),
		lastByKey:    make(map[string]time.Time),
		seenIdemp:    make(map[string]time.Time),
		auditLogPath: filepath.Join(dir, "audit.jsonl"),
	}
}

func (rm *RiskManager) Config() RiskConfig {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.cfg
}

func (rm *RiskManager) SetHalted(h bool) RiskConfig {
	rm.mu.Lock()
	rm.cfg.Halted = h
	c := rm.cfg
	rm.mu.Unlock()
	rm.LogAudit("risk_halt", true, map[string]any{"halted": h}, "")
	return c
}

func (rm *RiskManager) CheckPrefill(side, code string, price float64, qty int, idempKey string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.cfg.Halted {
		return fmt.Errorf("kill switch active: trading halted")
	}
	today := time.Now().Format("20060102")
	if today != rm.dailyDate {
		rm.dailyDate = today
		rm.dailyCount = 0
		rm.seenIdemp = make(map[string]time.Time)
	}
	if rm.dailyCount >= rm.cfg.MaxDailyOrders {
		return fmt.Errorf("daily limit reached (%d)", rm.cfg.MaxDailyOrders)
	}
	if price > 0 && qty > 0 && price*float64(qty) > rm.cfg.MaxSingleAmount {
		return fmt.Errorf("amount %.2f exceeds max_single_amount %.2f", price*float64(qty), rm.cfg.MaxSingleAmount)
	}
	if idempKey != "" {
		if _, exists := rm.seenIdemp[idempKey]; exists {
			return fmt.Errorf("duplicate idempotency_key: %s", idempKey)
		}
	}
	k := side + ":" + code
	if last, ok := rm.lastByKey[k]; ok && time.Since(last) < time.Duration(rm.cfg.CooldownSec)*time.Second {
		return fmt.Errorf("cooldown active for %s (%ds)", k, rm.cfg.CooldownSec)
	}

	rm.dailyCount++
	rm.lastByKey[k] = time.Now()
	if idempKey != "" {
		rm.seenIdemp[idempKey] = time.Now()
	}
	return nil
}

func (rm *RiskManager) LogAudit(action string, ok bool, detail any, errMsg string) {
	rec := AuditRecord{
		Time:   time.Now().Format(time.RFC3339),
		Action: action,
		OK:     ok,
		Detail: detail,
		Error:  errMsg,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(rm.auditLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
