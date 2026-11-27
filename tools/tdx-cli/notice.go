package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type cninfoResp struct {
	TotalAnnouncement int              `json:"totalAnnouncement"`
	Announcements     []cninfoRawItem  `json:"announcements"`
}

type cninfoRawItem struct {
	AnnouncementID    string `json:"announcementId"`
	SecCode           string `json:"secCode"`
	SecName           string `json:"secName"`
	AnnouncementTitle string `json:"announcementTitle"`
	AnnouncementTime  int64  `json:"announcementTime"`
	AdjunctURL        string `json:"adjunctUrl"`
}

type NoticeItem struct {
	ID         string `json:"id"`
	Exchange   string `json:"exchange"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	TimeUnixMs int64  `json:"time_ms"`
	TimeStr    string `json:"time"`
	URL        string `json:"url"`
	InWatch    bool   `json:"in_watchlist"`
	MatchedKW  string `json:"keyword"`
}

func loadLocalWatchlistCodes() map[string]bool {
	out := map[string]bool{}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	path := filepath.Join(home, ".tdx_companion_cache", "zxg.blk")
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(line)
		if len(s) == 7 {
			out[s[1:]] = true
		} else if len(s) == 6 {
			out[s] = true
		}
	}
	return out
}

func queryCNINFO(client *http.Client, column, keyword, seDate string) ([]cninfoRawItem, error) {
	form := url.Values{}
	form.Set("pageNum", "1")
	form.Set("pageSize", "30")
	form.Set("column", column)
	form.Set("tabName", "fulltext")
	form.Set("seDate", seDate)
	form.Set("searchkey", keyword)
	form.Set("isHLtitle", "false")

	req, err := http.NewRequest(http.MethodPost, "http://www.cninfo.com.cn/new/hisAnnouncement/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", "http://www.cninfo.com.cn")
	req.Header.Set("Referer", "http://www.cninfo.com.cn/new/commonUrl/pageOfSearch?url=disclosure/list/search")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed cninfoResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed.Announcements, nil
}

func fetchSevereNotices(keywords []string, days int, zxg map[string]bool, onlyZxg bool) ([]NoticeItem, error) {
	now := time.Now()
	start := now.AddDate(0, 0, -days).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	seDate := start + "~" + end

	exchanges := []struct {
		col  string
		name string
	}{
		{"szse", "深市"},
		{"sse", "沪市"},
		{"bj", "北交"},
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	var mu sync.Mutex
	var wg sync.WaitGroup
	byID := map[string]NoticeItem{}
	var firstErr error

	for _, ex := range exchanges {
		for _, kw := range keywords {
			kw = strings.TrimSpace(kw)
			if kw == "" {
				continue
			}
			wg.Add(1)
			go func(col, exName, keyword string) {
				defer wg.Done()
				items, err := queryCNINFO(httpClient, col, keyword, seDate)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				for _, it := range items {
					id := strings.TrimSpace(it.AnnouncementID)
					if id == "" {
						id = it.SecCode + "_" + it.AnnouncementTitle
					}
					if _, exists := byID[id]; exists {
						continue
					}
					inWatch := zxg[it.SecCode]
					if onlyZxg && !inWatch {
						continue
					}
					tStr := ""
					if it.AnnouncementTime > 0 {
						tStr = time.UnixMilli(it.AnnouncementTime).Format("2006-01-02 15:04")
					}
					pdfURL := ""
					if it.AdjunctURL != "" {
						pdfURL = "https://static.cninfo.com.cn/" + strings.TrimPrefix(it.AdjunctURL, "/")
					}
					cleanTitle := strings.NewReplacer("<em>", "", "</em>", "", "\n", " ", "\r", "").Replace(strings.TrimSpace(it.AnnouncementTitle))
					byID[id] = NoticeItem{
						ID:         id,
						Exchange:   exName,
						Code:       it.SecCode,
						Name:       strings.TrimSpace(it.SecName),
						Title:      cleanTitle,
						TimeUnixMs: it.AnnouncementTime,
						TimeStr:    tStr,
						URL:        pdfURL,
						InWatch:    inWatch,
						MatchedKW:  keyword,
					}
				}
			}(ex.col, ex.name, kw)
		}
	}
	wg.Wait()

	list := make([]NoticeItem, 0, len(byID))
	for _, v := range byID {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].TimeUnixMs != list[j].TimeUnixMs {
			return list[i].TimeUnixMs > list[j].TimeUnixMs
		}
		return list[i].Code < list[j].Code
	})
	if len(list) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return list, nil
}

func sendMacNotification(title, subtitle, body string) {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return s
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s" subtitle "%s" sound name "Glass"`,
		esc(body), esc(title), esc(subtitle))
	_ = exec.Command("osascript", "-e", script).Run()
}

func seenFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tdx_local_data", "notice_seen.json")
}

func loadSeenIDs() map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(seenFilePath())
	if err != nil {
		return out
	}
	var ids []string
	if json.Unmarshal(raw, &ids) == nil {
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

func saveSeenIDs(seen map[string]bool) {
	p := seenFilePath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	if len(ids) > 2000 {
		ids = ids[len(ids)-2000:]
	}
	if b, err := json.Marshal(ids); err == nil {
		_ = os.WriteFile(p, b, 0644)
	}
}

func printNoticeLine(it NoticeItem) {
	tag := "    "
	if it.InWatch {
		tag = "★自选"
	}
	fmt.Printf("%s | %s | %s %s %-8s | %s\n", it.TimeStr, it.Exchange, tag, it.Code, it.Name, it.Title)
	if it.URL != "" {
		fmt.Printf("                   %s\n", it.URL)
	}
}

func cmdNotice(args []string) error {
	fs := flag.NewFlagSet("notice", flag.ContinueOnError)
	days := fs.Int("days", 3, "回看最近天数")
	kwStr := fs.String("keywords", "严重异常波动,停牌核查", "逗号分隔关键词")
	watch := fs.Bool("watch", false, "持续轮询监控新公告并弹窗提醒")
	interval := fs.Int("interval", 60, "轮询间隔（秒）")
	notify := fs.Bool("notify", false, "发送 macOS 系统通知（--watch 默认开启）")
	onlyZxg := fs.Bool("zxg", false, "仅显示自选股公告")
	asJSON := fs.Bool("json", false, "JSON 输出")
	if err := fs.Parse(args); err != nil {
		return err
	}

	keywords := strings.Split(*kwStr, ",")
	zxg := loadLocalWatchlistCodes()

	if !*watch {
		items, err := fetchSevereNotices(keywords, *days, zxg, *onlyZxg)
		if err != nil {
			return fmt.Errorf("查询巨潮公告失败: %w", err)
		}
		if *asJSON {
			b, _ := json.MarshalIndent(items, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		fmt.Printf("[巨潮资讯 CNINFO | 近%d天 | 关键词: %s | 命中 %d 条 | 自选库 %d 只]\n",
			*days, *kwStr, len(items), len(zxg))
		for _, it := range items {
			printNoticeLine(it)
			if *notify {
				prefix := ""
				if it.InWatch {
					prefix = "★自选 "
				}
				sendMacNotification("严重异动公告", prefix+it.Code+" "+it.Name, it.Title)
			}
		}
		return nil
	}

	// Watch mode
	seen := loadSeenIDs()
	firstRun := len(seen) == 0
	if *interval < 15 {
		*interval = 15
	}
	fmt.Printf("[公告监控已启动] 源=巨潮资讯(沪/深/北) | 关键词=%s | 间隔=%ds | 自选库=%d只\n",
		*kwStr, *interval, len(zxg))

	for {
		items, err := fetchSevereNotices(keywords, 2, zxg, *onlyZxg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] 轮询失败: %v\n", time.Now().Format("15:04:05"), err)
		} else {
			newCount := 0
			for _, it := range items {
				if seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				newCount++
				printNoticeLine(it)
				if !firstRun || *notify {
					prefix := ""
					if it.InWatch {
						prefix = "★自选 "
					}
					sendMacNotification("严重异动/核查公告", prefix+it.Code+" "+it.Name, it.Title)
				}
			}
			if newCount > 0 {
				saveSeenIDs(seen)
			}
			if firstRun {
				fmt.Printf("[%s] 初始基线已建立（%d 条历史记录），等待新公告...\n", time.Now().Format("15:04:05"), len(items))
				firstRun = false
			}
		}
		time.Sleep(time.Duration(*interval) * time.Second)
	}
}
