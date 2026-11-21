//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const vipdocRoot = `C:\new_tdx\vipdoc`

type BreadthRow struct {
	Date      int     `json:"date"`
	Total     int     `json:"total"`
	Up        int     `json:"up"`
	Down      int     `json:"down"`
	Flat      int     `json:"flat"`
	DownPct   float64 `json:"down_pct"`
	MedianPct float64 `json:"median_pct"`
	Drop3     int     `json:"drop3"`
	Drop5     int     `json:"drop5"`
	Drop7     int     `json:"drop7"`
	LimitDown int     `json:"limit_down"`
	LimitUp   int     `json:"limit_up"`
	Rise5     int     `json:"rise5"`
	AmtYi     float64 `json:"amt_yi"`
	ShClose   float64 `json:"sh_close"`
	ShPct     float64 `json:"sh_pct"`
}

type BreadthResponse struct {
	OK         bool         `json:"ok"`
	FileCount  int          `json:"file_count"`
	LatestDate int          `json:"latest_date"`
	Cached     bool         `json:"cached"`
	Days       []BreadthRow `json:"days"`
}

type KlineBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Amount float64 `json:"amount"`
	Volume int64   `json:"volume"`
}

var (
	breadthMu      sync.Mutex
	breadthCache   []BreadthRow
	breadthFiles   int
	breadthModTime time.Time
)

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func isAShareFile(fname string) (string, bool) {
	if len(fname) != 12 {
		return "", false
	}
	pfx := strings.ToLower(fname[:2])
	c := fname[2:8]
	switch pfx {
	case "sh":
		if strings.HasPrefix(c, "60") {
			return "main", true
		}
		if strings.HasPrefix(c, "68") {
			return "20cm", true
		}
	case "sz":
		if strings.HasPrefix(c, "00") {
			return "main", true
		}
		if strings.HasPrefix(c, "30") {
			return "20cm", true
		}
	case "bj":
		if strings.HasPrefix(c, "92") || strings.HasPrefix(c, "8") || strings.HasPrefix(c, "43") {
			return "30cm", true
		}
	}
	return "", false
}

func latestIndexInfo() (int, time.Time) {
	idxPath := filepath.Join(vipdocRoot, "sh", "lday", "sh999999.day")
	fi, err := os.Stat(idxPath)
	if err != nil || fi.Size() < 32 {
		return 0, time.Time{}
	}
	f, err := os.Open(idxPath)
	if err != nil {
		return 0, fi.ModTime()
	}
	defer f.Close()
	_, _ = f.Seek(-32, io.SeekEnd)
	var b [4]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return 0, fi.ModTime()
	}
	return int(binary.LittleEndian.Uint32(b[:])), fi.ModTime()
}

func computeBreadth(maxRecs int) ([]BreadthRow, int) {
	type dayAgg struct {
		total, up, down, flat int
		drop3, drop5, drop7   int
		limitDown, limitUp    int
		rise5                 int
		amtSum                float64
		pcts                  []float64
	}

	stats := make(map[int]*dayAgg)
	fileCount := 0
	buf := make([]byte, maxRecs*32)

	for _, m := range []string{"sh", "sz", "bj"} {
		dir := filepath.Join(vipdocRoot, m, "lday")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".day") {
				continue
			}
			board, ok := isAShareFile(e.Name())
			if !ok {
				continue
			}
			fileCount++
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			fi, err := f.Stat()
			if err != nil || fi.Size() < 64 {
				f.Close()
				continue
			}
			recs := int(fi.Size() / 32)
			take := recs
			if take > maxRecs {
				take = maxRecs
			}
			_, _ = f.Seek(int64(recs-take)*32, io.SeekStart)
			n, _ := io.ReadFull(f, buf[:take*32])
			f.Close()
			take = n / 32

			var prevClose int32
			for i := 0; i < take; i++ {
				off := i * 32
				date := int(binary.LittleEndian.Uint32(buf[off : off+4]))
				closeP := int32(binary.LittleEndian.Uint32(buf[off+16 : off+20]))
				amt := float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+20 : off+24])))

				if i > 0 && prevClose > 0 && closeP > 0 && date > 20200000 {
					pct := float64(closeP-prevClose) * 100.0 / float64(prevClose)
					ds := stats[date]
					if ds == nil {
						ds = &dayAgg{pcts: make([]float64, 0, 5500)}
						stats[date] = ds
					}
					ds.total++
					ds.amtSum += amt
					ds.pcts = append(ds.pcts, pct)
					if closeP > prevClose {
						ds.up++
					} else if closeP < prevClose {
						ds.down++
					} else {
						ds.flat++
					}
					if pct <= -3.0 {
						ds.drop3++
					}
					if pct <= -5.0 {
						ds.drop5++
					}
					if pct <= -7.0 {
						ds.drop7++
					}
					if pct >= 5.0 {
						ds.rise5++
					}
					lim := 9.7
					if board == "20cm" {
						lim = 19.6
					} else if board == "30cm" {
						lim = 29.5
					}
					if pct <= -lim {
						ds.limitDown++
					}
					if pct >= lim {
						ds.limitUp++
					}
				}
				prevClose = closeP
			}
		}
	}

	type idxVal struct{ c, p float64 }
	shIdx := make(map[int]idxVal)
	if raw, err := os.ReadFile(filepath.Join(vipdocRoot, "sh", "lday", "sh999999.day")); err == nil {
		recs := len(raw) / 32
		start := recs - (maxRecs + 10)
		if start < 0 {
			start = 0
		}
		var prevC int32
		for i := start; i < recs; i++ {
			off := i * 32
			date := int(binary.LittleEndian.Uint32(raw[off : off+4]))
			c := int32(binary.LittleEndian.Uint32(raw[off+16 : off+20]))
			if prevC > 0 && c > 0 {
				shIdx[date] = idxVal{float64(c) / 100.0, float64(c-prevC) * 100.0 / float64(prevC)}
			}
			prevC = c
		}
	}

	var dates []int
	for d, s := range stats {
		if s.total >= 1000 {
			dates = append(dates, d)
		}
	}
	sort.Ints(dates)

	rows := make([]BreadthRow, 0, len(dates))
	for _, d := range dates {
		s := stats[d]
		sort.Float64s(s.pcts)
		med := 0.0
		if len(s.pcts) > 0 {
			med = s.pcts[len(s.pcts)/2]
		}
		downPct := 0.0
		if s.total > 0 {
			downPct = float64(s.down) * 100.0 / float64(s.total)
		}
		iv := shIdx[d]
		rows = append(rows, BreadthRow{
			Date:      d,
			Total:     s.total,
			Up:        s.up,
			Down:      s.down,
			Flat:      s.flat,
			DownPct:   round2(downPct),
			MedianPct: round2(med),
			Drop3:     s.drop3,
			Drop5:     s.drop5,
			Drop7:     s.drop7,
			LimitDown: s.limitDown,
			LimitUp:   s.limitUp,
			Rise5:     s.rise5,
			AmtYi:     math.Round(s.amtSum / 1e8),
			ShClose:   round2(iv.c),
			ShPct:     round2(iv.p),
		})
	}
	return rows, fileCount
}

func handleBreadth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	days := 45
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 250 {
			days = n
		}
	}
	force := r.URL.Query().Get("refresh") == "1"

	_, modTime := latestIndexInfo()

	breadthMu.Lock()
	cached := !force && len(breadthCache) > 0 && !modTime.IsZero() && modTime.Equal(breadthModTime) && len(breadthCache) >= days
	if !cached {
		fetchN := 130
		if days+5 > fetchN {
			fetchN = days + 5
		}
		breadthCache, breadthFiles = computeBreadth(fetchN)
		breadthModTime = modTime
	}
	allRows := breadthCache
	fCount := breadthFiles
	breadthMu.Unlock()

	start := len(allRows) - days
	if start < 0 {
		start = 0
	}
	slice := allRows[start:]
	latest := 0
	if len(slice) > 0 {
		latest = slice[len(slice)-1].Date
	}
	_ = json.NewEncoder(w).Encode(BreadthResponse{
		OK:         true,
		FileCount:  fCount,
		LatestDate: latest,
		Cached:     cached,
		Days:       slice,
	})
}

func resolveKlinePath(key, period string) (string, bool) {
	if len(key) != 7 {
		return "", false
	}
	var mkt string
	switch key[0] {
	case '1':
		mkt = "sh"
	case '0':
		mkt = "sz"
	case '2':
		mkt = "bj"
	default:
		return "", false
	}
	code := key[1:]
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	if period == "5m" {
		return filepath.Join(vipdocRoot, mkt, "fzline", mkt+code+".lc5"), true
	}
	return filepath.Join(vipdocRoot, mkt, "lday", mkt+code+".day"), true
}

func handleKline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if period == "" {
		period = "day"
	}
	limit := 120
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}

	path, ok := resolveKlinePath(key, period)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "invalid key (expected 7 chars like 0002242)"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": "kline file not found"})
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() < 32 {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "key": key, "period": period, "bars": []KlineBar{}})
		return
	}

	recs := int(fi.Size() / 32)
	take := recs
	if take > limit {
		take = limit
	}
	buf := make([]byte, take*32)
	_, _ = f.Seek(int64(recs-take)*32, io.SeekStart)
	n, _ := io.ReadFull(f, buf)
	take = n / 32

	bars := make([]KlineBar, 0, take)
	for i := 0; i < take; i++ {
		off := i * 32
		if period == "5m" {
			dRaw := binary.LittleEndian.Uint16(buf[off : off+2])
			mRaw := binary.LittleEndian.Uint16(buf[off+2 : off+4])
			year := int(dRaw)/2048 + 2004
			month := (int(dRaw) % 2048) / 100
			day := (int(dRaw) % 2048) % 100
			hour := int(mRaw) / 60
			min := int(mRaw) % 60
			bars = append(bars, KlineBar{
				Date:   strconv.Itoa(year) + "-" + pad2(month) + "-" + pad2(day) + " " + pad2(hour) + ":" + pad2(min),
				Open:   round2(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+4 : off+8])))),
				High:   round2(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+8 : off+12])))),
				Low:    round2(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+12 : off+16])))),
				Close:  round2(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+16 : off+20])))),
				Amount: math.Round(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+20 : off+24])))),
				Volume: int64(binary.LittleEndian.Uint32(buf[off+24 : off+28])),
			})
		} else {
			d := int(binary.LittleEndian.Uint32(buf[off : off+4]))
			bars = append(bars, KlineBar{
				Date:   strconv.Itoa(d),
				Open:   round2(float64(int32(binary.LittleEndian.Uint32(buf[off+4 : off+8]))) / 100.0),
				High:   round2(float64(int32(binary.LittleEndian.Uint32(buf[off+8 : off+12]))) / 100.0),
				Low:    round2(float64(int32(binary.LittleEndian.Uint32(buf[off+12 : off+16]))) / 100.0),
				Close:  round2(float64(int32(binary.LittleEndian.Uint32(buf[off+16 : off+20]))) / 100.0),
				Amount: math.Round(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+20 : off+24])))),
				Volume: int64(binary.LittleEndian.Uint32(buf[off+24 : off+28])),
			})
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"key":    key,
		"period": period,
		"count":  len(bars),
		"bars":   bars,
	})
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
