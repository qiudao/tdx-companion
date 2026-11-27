package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

const hubVersion = "0.1.0"

var (
	startTime = time.Now()

	cacheMu       sync.RWMutex
	cachedNotices *NoticesPayload
	cachedLimitUp *LimitUpPayload
)

func chinaLoc() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func refreshNoticesCache() {
	res, err := fetchNotices([]string{"严重异常波动", "停牌核查"}, 7)
	if err != nil {
		log.Printf("[notices] refresh error: %v", err)
		return
	}
	cacheMu.Lock()
	cachedNotices = res
	cacheMu.Unlock()
	log.Printf("[notices] refreshed: %d items", res.Count)
}

func refreshLimitUpCache() {
	res, err := fetchLimitUp()
	if err != nil {
		log.Printf("[limit-up] refresh error: %v", err)
		return
	}
	cacheMu.Lock()
	cachedLimitUp = res
	cacheMu.Unlock()
	log.Printf("[limit-up] refreshed: %d stocks, %d plates (%s)", res.Count, len(res.Plates), res.TradeDate)
}

func startBackgroundRefresh() {
	go func() {
		refreshNoticesCache()
		t := time.NewTicker(60 * time.Second)
		for range t.C {
			refreshNoticesCache()
		}
	}()
	go func() {
		refreshLimitUpCache()
		t := time.NewTicker(45 * time.Second)
		for range t.C {
			refreshLimitUpCache()
		}
	}()
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	cacheMu.RLock()
	nCnt, nUpd := 0, ""
	if cachedNotices != nil {
		nCnt, nUpd = cachedNotices.Count, cachedNotices.UpdatedAt
	}
	lCnt, lUpd := 0, ""
	if cachedLimitUp != nil {
		lCnt, lUpd = cachedLimitUp.Count, cachedLimitUp.UpdatedAt
	}
	cacheMu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"service":             "tdx-hub",
		"version":             hubVersion,
		"uptime_sec":          int(time.Since(startTime).Seconds()),
		"notices_count":       nCnt,
		"notices_updated_at":  nUpd,
		"limit_up_count":      lCnt,
		"limit_up_updated_at": lUpd,
		"endpoints": []string{
			"GET /",
			"GET /health",
			"GET /api/notices?days=7&keywords=严重异常波动,停牌核查&refresh=0",
			"GET /api/limit-up?refresh=0",
		},
	})
}

func handleNotices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	daysStr := strings.TrimSpace(q.Get("days"))
	kwStr := strings.TrimSpace(q.Get("keywords"))
	forceRefresh := q.Get("refresh") == "1"

	if daysStr == "" && kwStr == "" && !forceRefresh {
		cacheMu.RLock()
		c := cachedNotices
		cacheMu.RUnlock()
		if c != nil {
			writeJSON(w, http.StatusOK, c)
			return
		}
	}

	days := 7
	if daysStr != "" {
		if v, err := strconv.Atoi(daysStr); err == nil && v > 0 {
			days = v
		}
	}
	keywords := []string{"严重异常波动", "停牌核查"}
	if kwStr != "" {
		keywords = strings.Split(kwStr, ",")
	}
	res, err := fetchNotices(keywords, days)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	if days == 7 && kwStr == "" {
		cacheMu.Lock()
		cachedNotices = res
		cacheMu.Unlock()
	}
	writeJSON(w, http.StatusOK, res)
}

func handleLimitUp(w http.ResponseWriter, r *http.Request) {
	forceRefresh := r.URL.Query().Get("refresh") == "1"
	if !forceRefresh {
		cacheMu.RLock()
		c := cachedLimitUp
		cacheMu.RUnlock()
		if c != nil {
			writeJSON(w, http.StatusOK, c)
			return
		}
	}
	res, err := fetchLimitUp()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "err": err.Error()})
		return
	}
	cacheMu.Lock()
	cachedLimitUp = res
	cacheMu.Unlock()
	writeJSON(w, http.StatusOK, res)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18900", "HTTP listen address")
	flag.Parse()

	startBackgroundRefresh()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/notices", handleNotices)
	mux.HandleFunc("/api/limit-up", handleLimitUp)

	log.Printf("tdx-hub v%s listening on %s", hubVersion, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
