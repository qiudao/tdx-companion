package main

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	prlctlBin = "/usr/local/bin/prlctl"
	agentPort = 19876
)

type HealthResp struct {
	OK             bool   `json:"ok"`
	Version        string `json:"version"`
	UptimeSec      int    `json:"uptime_sec"`
	TdxOnline      bool   `json:"tdx_online"`
	TdxTitle       string `json:"tdx_title"`
	TdxStock       string `json:"tdx_stock"`
	LdayLatestDate int    `json:"lday_latest_date"`
	LdayUpdatedAt  string `json:"lday_updated_at"`
}

type StatusResp struct {
	OK    bool   `json:"ok"`
	Title string `json:"title"`
	Stock string `json:"stock"`
}

type Bar struct {
	Date   int     `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Amount float64 `json:"amount"`
	Volume int64   `json:"volume"`
}

type rawBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Amount float64 `json:"amount"`
	Volume int64   `json:"volume"`
}

type barsResp struct {
	OK     bool     `json:"ok"`
	Key    string   `json:"key"`
	Period string   `json:"period"`
	Count  int      `json:"count"`
	Bars   []rawBar `json:"bars"`
	Err    string   `json:"err"`
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func DetectVMIP() (string, error) {
	if env := strings.TrimSpace(os.Getenv("TDX_AGENT_HOST")); env != "" {
		return env, nil
	}
	out, err := exec.Command(prlctlBin, "list", "-a", "-f").Output()
	if err != nil {
		return "", fmt.Errorf("prlctl 检测虚拟机失败: %w（也可设置环境变量 TDX_AGENT_HOST）", err)
	}
	ipRe := regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	for _, line := range strings.Split(string(out), "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "running") && strings.Contains(low, "windows") {
			if m := ipRe.FindStringSubmatch(line); len(m) == 2 {
				return m[1], nil
			}
		}
	}
	return "", fmt.Errorf("未检测到运行中的 Parallels Windows 虚拟机（也可设置环境变量 TDX_AGENT_HOST）")
}

func NewClient() (*Client, error) {
	ip, err := DetectVMIP()
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL: fmt.Sprintf("http://%s:%d", ip, agentPort),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *Client) getBytes(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	return io.ReadAll(r)
}

func (c *Client) Health() (*HealthResp, error) {
	raw, err := c.getBytes("/health")
	if err != nil {
		return nil, err
	}
	var h HealthResp
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *Client) Status() (*StatusResp, error) {
	raw, err := c.getBytes("/status")
	if err != nil {
		return nil, err
	}
	var s StatusResp
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) Switch(key string) error {
	raw, err := c.getBytes("/switch?key=" + url.QueryEscape(key))
	if err != nil {
		return err
	}
	var resp struct {
		OK  bool   `json:"ok"`
		Err string `json:"err"`
	}
	_ = json.Unmarshal(raw, &resp)
	if !resp.OK {
		if resp.Err == "" {
			resp.Err = string(raw)
		}
		return fmt.Errorf("切股失败: %s", resp.Err)
	}
	return nil
}

func (c *Client) Bars(key, period string, limit int) ([]Bar, error) {
	q := fmt.Sprintf("/data/bars?key=%s&period=%s&limit=%d", url.QueryEscape(key), url.QueryEscape(period), limit)
	raw, err := c.getBytes(q)
	if err != nil {
		return nil, err
	}
	var br barsResp
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, err
	}
	if !br.OK {
		return nil, fmt.Errorf("bars error: %s", br.Err)
	}
	bars := make([]Bar, 0, len(br.Bars))
	for _, rb := range br.Bars {
		d, err := strconv.Atoi(rb.Date)
		if err != nil {
			continue
		}
		bars = append(bars, Bar{
			Date:   d,
			Open:   rb.Open,
			High:   rb.High,
			Low:    rb.Low,
			Close:  rb.Close,
			Amount: rb.Amount,
			Volume: rb.Volume,
		})
	}
	return bars, nil
}

func (c *Client) CrossSectionBars(days int) (map[string][]Bar, error) {
	raw, err := c.getBytes(fmt.Sprintf("/data/cross-section?days=%d&format=csv", days))
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(raw))
	if _, err := r.Read(); err != nil { // header
		return nil, err
	}
	out := make(map[string][]Bar, 5500)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 10 {
			continue
		}
		d, err1 := strconv.Atoi(rec[0])
		o, err2 := strconv.ParseFloat(rec[3], 64)
		h, err3 := strconv.ParseFloat(rec[4], 64)
		l, err4 := strconv.ParseFloat(rec[5], 64)
		cl, err5 := strconv.ParseFloat(rec[6], 64)
		amt, _ := strconv.ParseFloat(rec[8], 64)
		vol, _ := strconv.ParseInt(rec[9], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		key := rec[1]
		out[key] = append(out[key], Bar{
			Date: d, Open: o, High: h, Low: l, Close: cl, Amount: amt, Volume: vol,
		})
	}
	return out, nil
}

func (c *Client) Blocks() ([]string, error) {
	raw, err := c.getBytes("/data/blocks")
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK     bool     `json:"ok"`
		Blocks []string `json:"blocks"`
		Err    string   `json:"err"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Err)
	}
	return resp.Blocks, nil
}

func (c *Client) WriteBlock(name string, keys []string) (string, error) {
	body := strings.Join(keys, "\r\n") + "\r\n"
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/blk?name="+url.QueryEscape(name), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		OK   bool   `json:"ok"`
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Path, nil
}

// LoadSymbols parses shs.tnf / szs.tnf from win-agent and decodes GB18030 names via iconv.
// Returns keyToName (e.g. "0002685" -> "华东重机") and nameToKey ("华东重机" -> "0002685").
func (c *Client) LoadSymbols() (map[string]string, map[string]string) {
	keyToName := make(map[string]string, 6000)
	nameToKey := make(map[string]string, 6000)

	type rawEntry struct {
		key  string
		name []byte
	}
	var entries []rawEntry

	for _, item := range []struct {
		fname string
		pfx   string
	}{
		{"shs.tnf", "1"},
		{"szs.tnf", "0"},
		{"bjs.tnf", "2"},
	} {
		raw, err := c.getBytes("/file?name=" + item.fname)
		if err != nil || len(raw) < 50+360 {
			continue
		}
		const stride = 360
		for i := 50; i+stride <= len(raw); i += stride {
			code := strings.TrimSpace(string(raw[i : i+6]))
			if len(code) != 6 || !isAllDigits(code) {
				continue
			}
			// Only keep A-share stocks for name lookup priority
			if !isAShareCode(item.pfx, code) {
				continue
			}
			nb := raw[i+31 : i+47]
			if z := bytes.IndexByte(nb, 0); z >= 0 {
				nb = nb[:z]
			}
			nb = bytes.TrimSpace(nb)
			if len(nb) > 0 {
				entries = append(entries, rawEntry{key: item.pfx + code, name: nb})
			}
		}
	}

	if len(entries) == 0 {
		return keyToName, nameToKey
	}

	var buf bytes.Buffer
	for _, e := range entries {
		buf.Write(e.name)
		buf.WriteByte('\n')
	}

	cmd := exec.Command("iconv", "-f", "GB18030", "-t", "UTF-8")
	cmd.Stdin = &buf
	decoded, err := cmd.Output()
	if err != nil {
		return keyToName, nameToKey
	}
	lines := strings.Split(string(decoded), "\n")
	for i, e := range entries {
		if i >= len(lines) {
			break
		}
		name := strings.TrimSpace(lines[i])
		if name == "" {
			continue
		}
		keyToName[e.key] = name
		if _, exists := nameToKey[name]; !exists {
			nameToKey[name] = e.key
		}
	}
	return keyToName, nameToKey
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isAShareCode(pfx, code string) bool {
	switch pfx {
	case "1":
		return strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68")
	case "0":
		return strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30")
	case "2":
		return strings.HasPrefix(code, "92") || strings.HasPrefix(code, "8") || strings.HasPrefix(code, "43")
	}
	return false
}
