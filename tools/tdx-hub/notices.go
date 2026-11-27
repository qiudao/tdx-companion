package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type cninfoResp struct {
	TotalAnnouncement int             `json:"totalAnnouncement"`
	Announcements     []cninfoRawItem `json:"announcements"`
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
	Board      string `json:"board"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	TimeUnixMs int64  `json:"time_ms"`
	TimeStr    string `json:"time"`
	URL        string `json:"url"`
	Keyword    string `json:"keyword"`
}

type NoticesPayload struct {
	UpdatedAt string       `json:"updated_at"`
	Days      int          `json:"days"`
	Keywords  []string     `json:"keywords"`
	Count     int          `json:"count"`
	Items     []NoticeItem `json:"items"`
}

func boardByCode(code string) string {
	if strings.HasPrefix(code, "688") || strings.HasPrefix(code, "689") {
		return "科创"
	}
	if strings.HasPrefix(code, "6") {
		return "沪主"
	}
	if strings.HasPrefix(code, "30") {
		return "创业"
	}
	if strings.HasPrefix(code, "00") {
		return "深主"
	}
	if strings.HasPrefix(code, "92") || strings.HasPrefix(code, "8") || strings.HasPrefix(code, "4") {
		return "北交"
	}
	return "A股"
}

func queryCNINFO(client *http.Client, keyword, seDate string) ([]cninfoRawItem, error) {
	form := url.Values{}
	form.Set("pageNum", "1")
	form.Set("pageSize", "30")
	form.Set("column", "szse")
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

func fetchNotices(keywords []string, days int) (*NoticesPayload, error) {
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	loc := chinaLoc()
	now := time.Now().In(loc)
	start := now.AddDate(0, 0, -days).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	seDate := start + "~" + end

	httpClient := &http.Client{Timeout: 10 * time.Second}
	var mu sync.Mutex
	var wg sync.WaitGroup
	byID := map[string]NoticeItem{}
	var firstErr error
	cleanKWs := make([]string, 0, len(keywords))

	for _, rawKW := range keywords {
		kw := strings.TrimSpace(rawKW)
		if kw == "" {
			continue
		}
		cleanKWs = append(cleanKWs, kw)
		wg.Add(1)
		go func(keyword string) {
			defer wg.Done()
			items, err := queryCNINFO(httpClient, keyword, seDate)
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
				tStr := ""
				if it.AnnouncementTime > 0 {
					tStr = time.UnixMilli(it.AnnouncementTime).In(loc).Format("2006-01-02 15:04")
				}
				pdfURL := ""
				if it.AdjunctURL != "" {
					pdfURL = "https://static.cninfo.com.cn/" + strings.TrimPrefix(it.AdjunctURL, "/")
				}
				cleanTitle := strings.NewReplacer("<em>", "", "</em>", "", "\n", " ", "\r", "").Replace(strings.TrimSpace(it.AnnouncementTitle))
				byID[id] = NoticeItem{
					ID:         id,
					Board:      boardByCode(it.SecCode),
					Code:       it.SecCode,
					Name:       strings.TrimSpace(it.SecName),
					Title:      cleanTitle,
					TimeUnixMs: it.AnnouncementTime,
					TimeStr:    tStr,
					URL:        pdfURL,
					Keyword:    keyword,
				}
			}
		}(kw)
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
	return &NoticesPayload{
		UpdatedAt: now.Format("2006-01-02 15:04:05"),
		Days:      days,
		Keywords:  cleanKWs,
		Count:     len(list),
		Items:     list,
	}, nil
}
