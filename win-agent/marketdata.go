//go:build windows

package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const vipdocRoot = `C:\new_tdx\vipdoc`

type KlineBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Amount float64 `json:"amount"`
	Volume int64   `json:"volume"`
}

type CrossRow struct {
	Date      int     `json:"date"`
	Key       string  `json:"key"`
	Board     string  `json:"board"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	PrevClose float64 `json:"prev_close"`
	Amount    float64 `json:"amount"`
	Volume    int64   `json:"volume"`
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func isAShareFile(fname string) (mktPrefix, code, board string, ok bool) {
	if len(fname) != 12 {
		return "", "", "", false
	}
	pfx := strings.ToLower(fname[:2])
	c := fname[2:8]
	switch pfx {
	case "sh":
		if strings.HasPrefix(c, "60") {
			return "1", c, "main", true
		}
		if strings.HasPrefix(c, "68") {
			return "1", c, "20cm", true
		}
	case "sz":
		if strings.HasPrefix(c, "00") {
			return "0", c, "main", true
		}
		if strings.HasPrefix(c, "30") {
			return "0", c, "20cm", true
		}
	case "bj":
		if strings.HasPrefix(c, "92") || strings.HasPrefix(c, "8") || strings.HasPrefix(c, "43") {
			return "2", c, "30cm", true
		}
	}
	return "", "", "", false
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

func recentIndexDates(days int) int {
	idxPath := filepath.Join(vipdocRoot, "sh", "lday", "sh999999.day")
	raw, err := os.ReadFile(idxPath)
	if err != nil || len(raw) < 32 {
		return 0
	}
	recs := len(raw) / 32
	idx := recs - days
	if idx < 0 {
		idx = 0
	}
	return int(binary.LittleEndian.Uint32(raw[idx*32 : idx*32+4]))
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

// GET /data/bars?key=0002242&period=day|5m&limit=120
func handleBars(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if period == "" {
		period = "day"
	}
	limit := 120
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "key": key, "period": period, "count": 0, "bars": []KlineBar{}})
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

// GET /data/cross-section?days=60&start=20260901&format=csv|json
// Streams raw OHLCV + prev_close across all A-shares with optional gzip compression.
func handleCrossSection(w http.ResponseWriter, r *http.Request) {
	days := 60
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			days = n
		}
	}
	startDate := 0
	if v := r.URL.Query().Get("start"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 19900000 {
			startDate = n
		}
	}
	if startDate == 0 {
		startDate = recentIndexDates(days)
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}

	var out io.Writer = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		out = gz
	}

	maxReadRecs := days + 30
	if startDate > 0 && maxReadRecs < 260 {
		maxReadRecs = 260
	}
	buf := make([]byte, maxReadRecs*32)

	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		rows := make([]CrossRow, 0, 6000)
		scanCrossSection(buf, maxReadRecs, startDate, func(cr CrossRow) {
			rows = append(rows, cr)
		})
		_ = json.NewEncoder(out).Encode(map[string]any{
			"ok":         true,
			"start_date": startDate,
			"count":      len(rows),
			"rows":       rows,
		})
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	bw := bufio.NewWriterSize(out, 64*1024)
	defer bw.Flush()
	_, _ = bw.WriteString("date,key,board,open,high,low,close,prev_close,amount,volume\n")
	scanCrossSection(buf, maxReadRecs, startDate, func(cr CrossRow) {
		_, _ = fmt.Fprintf(bw, "%d,%s,%s,%.2f,%.2f,%.2f,%.2f,%.2f,%.0f,%d\n",
			cr.Date, cr.Key, cr.Board, cr.Open, cr.High, cr.Low, cr.Close, cr.PrevClose, cr.Amount, cr.Volume)
	})
}

func scanCrossSection(buf []byte, maxReadRecs int, startDate int, emit func(CrossRow)) {
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
			mktPfx, code, board, ok := isAShareFile(e.Name())
			if !ok {
				continue
			}
			key := mktPfx + code
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			fi, err := f.Stat()
			if err != nil || fi.Size() < 32 {
				f.Close()
				continue
			}
			recs := int(fi.Size() / 32)
			take := recs
			if take > maxReadRecs {
				take = maxReadRecs
			}
			_, _ = f.Seek(int64(recs-take)*32, io.SeekStart)
			n, _ := io.ReadFull(f, buf[:take*32])
			f.Close()
			take = n / 32

			var prevC int32
			for i := 0; i < take; i++ {
				off := i * 32
				d := int(binary.LittleEndian.Uint32(buf[off : off+4]))
				o := int32(binary.LittleEndian.Uint32(buf[off+4 : off+8]))
				h := int32(binary.LittleEndian.Uint32(buf[off+8 : off+12]))
				l := int32(binary.LittleEndian.Uint32(buf[off+12 : off+16]))
				c := int32(binary.LittleEndian.Uint32(buf[off+16 : off+20]))
				amt := float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[off+20 : off+24])))
				vol := int64(binary.LittleEndian.Uint32(buf[off+24 : off+28]))

				if d >= startDate && c > 0 {
					emit(CrossRow{
						Date:      d,
						Key:       key,
						Board:     board,
						Open:      float64(o) / 100.0,
						High:      float64(h) / 100.0,
						Low:       float64(l) / 100.0,
						Close:     float64(c) / 100.0,
						PrevClose: float64(prevC) / 100.0,
						Amount:    amt,
						Volume:    vol,
					})
				}
				prevC = c
			}
		}
	}
}

// GET /data/blocks — list all .blk block files in T0002/blocknew
func handleListBlocks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	dir := filepath.Join(tdxRoot, "blocknew")
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "err": err.Error()})
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".blk") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "blocks": names})
}
