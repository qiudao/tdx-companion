package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type xgbResp struct {
	Code int       `json:"code"`
	Data []xgbItem `json:"data"`
}

type xgbItem struct {
	Symbol               string         `json:"symbol"`
	StockChiName         string         `json:"stock_chi_name"`
	Price                float64        `json:"price"`
	ChangePercent        float64        `json:"change_percent"`
	LimitUpDays          int            `json:"limit_up_days"`
	MDaysNBoardsDays     int            `json:"m_days_n_boards_days"`
	MDaysNBoardsBoards   int            `json:"m_days_n_boards_boards"`
	FirstLimitUp         int64          `json:"first_limit_up"`
	LastLimitUp          int64          `json:"last_limit_up"`
	BreakLimitUpTimes    int            `json:"break_limit_up_times"`
	TurnoverRatio        float64        `json:"turnover_ratio"`
	NonRestrictedCapital float64        `json:"non_restricted_capital"`
	SurgeReason          xgbSurgeReason `json:"surge_reason"`
}

type xgbSurgeReason struct {
	StockReason   string     `json:"stock_reason"`
	RelatedPlates []xgbPlate `json:"related_plates"`
}

type xgbPlate struct {
	PlateName   string `json:"plate_name"`
	PlateReason string `json:"plate_reason"`
}

type emResp struct {
	Data struct {
		QDate int      `json:"qdate"`
		Pool  []emItem `json:"pool"`
	} `json:"data"`
}

type emItem struct {
	C      string  `json:"c"`
	N      string  `json:"n"`
	P      float64 `json:"p"`
	Zdp    float64 `json:"zdp"`
	Amount float64 `json:"amount"`
	Ltsz   float64 `json:"ltsz"`
	Hs     float64 `json:"hs"`
	Lbc    int     `json:"lbc"`
	Fbt    int     `json:"fbt"`
	Lbt    int     `json:"lbt"`
	Fund   float64 `json:"fund"`
	Zbc    int     `json:"zbc"`
	Hybk   string  `json:"hybk"`
	Zttj   struct {
		Days int `json:"days"`
		Ct   int `json:"ct"`
	} `json:"zttj"`
}

type LimitUpStock struct {
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Price        float64  `json:"price"`
	Pct          float64  `json:"pct"`
	Lbc          int      `json:"lbc"`
	BoardLabel   string   `json:"board_label"`
	FirstTime    string   `json:"first_time"`
	LastTime     string   `json:"last_time"`
	Zbc          int      `json:"zbc"`
	AmountYi     float64  `json:"amount_yi"`
	FundYi       float64  `json:"fund_yi"`
	LtszYi       float64  `json:"ltsz_yi"`
	Hs           float64  `json:"hs"`
	PrimaryPlate string   `json:"primary_plate"`
	OtherPlates  []string `json:"other_plates"`
	Reason       string   `json:"reason"`
	Is20cm       bool     `json:"is_20cm"`
	Is30cm       bool     `json:"is_30cm"`
}

type PlateSummary struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	MaxLbc int    `json:"max_lbc"`
	Reason string `json:"reason"`
}

type LimitUpPayload struct {
	UpdatedAt string         `json:"updated_at"`
	TradeDate string         `json:"trade_date"`
	Count     int            `json:"count"`
	Plates    []PlateSummary `json:"plates"`
	Stocks    []LimitUpStock `json:"stocks"`
}

func roundN(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func fmtUnixHMS(ts int64, loc *time.Location) string {
	if ts <= 0 {
		return "--"
	}
	return time.Unix(ts, 0).In(loc).Format("15:04:05")
}

func fmtEmHMS(v int) string {
	if v <= 0 {
		return "--"
	}
	s := fmt.Sprintf("%06d", v)
	return s[0:2] + ":" + s[2:4] + ":" + s[4:6]
}

func httpGetJSON(client *http.Client, urlStr string, dst any) error {
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func fetchLimitUp() (*LimitUpPayload, error) {
	loc := chinaLoc()
	now := time.Now().In(loc)
	client := &http.Client{Timeout: 10 * time.Second}

	var xgb xgbResp
	xgbErr := httpGetJSON(client, "https://flash-api.xuangubao.cn/api/pool/detail?pool_name=limit_up", &xgb)

	// Find latest Eastmoney trading date (try today, then step back up to 4 days for weekends/holidays)
	emMap := map[string]emItem{}
	tradeDate := now.Format("20060102")
	for back := 0; back <= 4; back++ {
		dStr := now.AddDate(0, 0, -back).Format("20060102")
		var em emResp
		emURL := "https://push2ex.eastmoney.com/getTopicZTPool?ut=7eea3edcaed734bea9cbfc24409ed989&dpt=wz.ztzt&Pageindex=0&pagesize=500&sort=fbt:asc&date=" + dStr
		if err := httpGetJSON(client, emURL, &em); err == nil && len(em.Data.Pool) > 0 {
			if em.Data.QDate > 0 {
				tradeDate = fmt.Sprintf("%d", em.Data.QDate)
			} else {
				tradeDate = dStr
			}
			for _, it := range em.Data.Pool {
				emMap[it.C] = it
			}
			break
		}
	}

	if xgbErr != nil && len(emMap) == 0 {
		return nil, xgbErr
	}

	stocksByCode := map[string]LimitUpStock{}
	plateReasons := map[string]string{}

	for _, it := range xgb.Data {
		code := it.Symbol
		if dot := strings.IndexByte(code, '.'); dot > 0 {
			code = code[:dot]
		}
		if code == "" {
			continue
		}
		em, hasEm := emMap[code]
		var plateNames []string
		for _, pl := range it.SurgeReason.RelatedPlates {
			pn := strings.TrimSpace(pl.PlateName)
			pr := strings.TrimSpace(pl.PlateReason)
			if pn != "" {
				plateNames = append(plateNames, pn)
				if pr != "" && plateReasons[pn] == "" {
					plateReasons[pn] = pr
				}
			}
		}
		primaryPlate := "其他/个股公告"
		var otherPlates []string
		if len(plateNames) > 0 {
			primaryPlate = plateNames[0]
			if len(plateNames) > 1 {
				otherPlates = plateNames[1:]
			}
		} else if hasEm && em.Hybk != "" {
			primaryPlate = em.Hybk
		}

		lbc := it.LimitUpDays
		if lbc <= 0 && hasEm {
			lbc = em.Lbc
		}
		if lbc <= 0 {
			lbc = 1
		}
		mDays, nBoards := it.MDaysNBoardsDays, it.MDaysNBoardsBoards
		if mDays == 0 && hasEm {
			mDays, nBoards = em.Zttj.Days, em.Zttj.Ct
		}
		boardLabel := "首板"
		if lbc >= 2 {
			boardLabel = fmt.Sprintf("%d板", lbc)
		}
		if mDays > lbc && nBoards > lbc {
			boardLabel = fmt.Sprintf("%d天%d板", mDays, nBoards)
		}

		firstT := fmtUnixHMS(it.FirstLimitUp, loc)
		if firstT == "--" && hasEm {
			firstT = fmtEmHMS(em.Fbt)
		}
		lastT := fmtUnixHMS(it.LastLimitUp, loc)
		if lastT == "--" && hasEm {
			lastT = fmtEmHMS(em.Lbt)
		}

		price := it.Price
		if price <= 0 && hasEm {
			price = em.P / 1000.0
		}
		pct := it.ChangePercent * 100.0
		if hasEm && em.Zdp != 0 {
			pct = em.Zdp
		}
		amtYi := 0.0
		fundYi := 0.0
		ltszYi := it.NonRestrictedCapital / 1e8
		hs := it.TurnoverRatio * 100.0
		if hasEm {
			amtYi = em.Amount / 1e8
			fundYi = em.Fund / 1e8
			if em.Ltsz > 0 {
				ltszYi = em.Ltsz / 1e8
			}
			if em.Hs > 0 {
				hs = em.Hs
			}
		}
		reason := strings.TrimSpace(it.SurgeReason.StockReason)
		if reason == "" {
			reason = "暂无详细涨停说明"
		}
		stocksByCode[code] = LimitUpStock{
			Code:         code,
			Name:         strings.TrimSpace(it.StockChiName),
			Price:        roundN(price, 2),
			Pct:          roundN(pct, 2),
			Lbc:          lbc,
			BoardLabel:   boardLabel,
			FirstTime:    firstT,
			LastTime:     lastT,
			Zbc:          it.BreakLimitUpTimes,
			AmountYi:     roundN(amtYi, 2),
			FundYi:       roundN(fundYi, 2),
			LtszYi:       roundN(ltszYi, 1),
			Hs:           roundN(hs, 1),
			PrimaryPlate: primaryPlate,
			OtherPlates:  otherPlates,
			Reason:       reason,
			Is20cm:       strings.HasPrefix(code, "30") || strings.HasPrefix(code, "688") || strings.HasPrefix(code, "689") || pct >= 19.5,
			Is30cm:       strings.HasPrefix(code, "92") || strings.HasPrefix(code, "8") || strings.HasPrefix(code, "43") || pct >= 29.0,
		}
	}

	for code, em := range emMap {
		if _, ok := stocksByCode[code]; ok {
			continue
		}
		lbc := em.Lbc
		if lbc <= 0 {
			lbc = 1
		}
		boardLabel := "首板"
		if lbc >= 2 {
			boardLabel = fmt.Sprintf("%d板", lbc)
		}
		if em.Zttj.Days > lbc && em.Zttj.Ct > lbc {
			boardLabel = fmt.Sprintf("%d天%d板", em.Zttj.Days, em.Zttj.Ct)
		}
		plate := em.Hybk
		if plate == "" {
			plate = "其他/个股公告"
		}
		stocksByCode[code] = LimitUpStock{
			Code:         code,
			Name:         strings.TrimSpace(em.N),
			Price:        roundN(em.P/1000.0, 2),
			Pct:          roundN(em.Zdp, 2),
			Lbc:          lbc,
			BoardLabel:   boardLabel,
			FirstTime:    fmtEmHMS(em.Fbt),
			LastTime:     fmtEmHMS(em.Lbt),
			Zbc:          em.Zbc,
			AmountYi:     roundN(em.Amount/1e8, 2),
			FundYi:       roundN(em.Fund/1e8, 2),
			LtszYi:       roundN(em.Ltsz/1e8, 1),
			Hs:           roundN(em.Hs, 1),
			PrimaryPlate: plate,
			Reason:       "东财涨停池收录（暂无题材细分说明）",
			Is20cm:       strings.HasPrefix(code, "30") || strings.HasPrefix(code, "688") || strings.HasPrefix(code, "689") || em.Zdp >= 19.5,
			Is30cm:       strings.HasPrefix(code, "92") || strings.HasPrefix(code, "8") || strings.HasPrefix(code, "43") || em.Zdp >= 29.0,
		}
	}

	stocks := make([]LimitUpStock, 0, len(stocksByCode))
	plateCount := map[string]int{}
	plateMax := map[string]int{}
	for _, s := range stocksByCode {
		stocks = append(stocks, s)
		plateCount[s.PrimaryPlate]++
		if s.Lbc > plateMax[s.PrimaryPlate] {
			plateMax[s.PrimaryPlate] = s.Lbc
		}
	}
	sort.Slice(stocks, func(i, j int) bool {
		if stocks[i].Lbc != stocks[j].Lbc {
			return stocks[i].Lbc > stocks[j].Lbc
		}
		if stocks[i].FirstTime != stocks[j].FirstTime {
			return stocks[i].FirstTime < stocks[j].FirstTime
		}
		return stocks[i].FundYi > stocks[j].FundYi
	})

	plates := make([]PlateSummary, 0, len(plateCount))
	for name, cnt := range plateCount {
		plates = append(plates, PlateSummary{
			Name:   name,
			Count:  cnt,
			MaxLbc: plateMax[name],
			Reason: plateReasons[name],
		})
	}
	sort.Slice(plates, func(i, j int) bool {
		oi := strings.HasPrefix(plates[i].Name, "其他")
		oj := strings.HasPrefix(plates[j].Name, "其他")
		if oi != oj {
			return !oi
		}
		if plates[i].Count != plates[j].Count {
			return plates[i].Count > plates[j].Count
		}
		if plates[i].MaxLbc != plates[j].MaxLbc {
			return plates[i].MaxLbc > plates[j].MaxLbc
		}
		return plates[i].Name < plates[j].Name
	})

	return &LimitUpPayload{
		UpdatedAt: now.Format("2006-01-02 15:04:05"),
		TradeDate: tradeDate,
		Count:     len(stocks),
		Plates:    plates,
		Stocks:    stocks,
	}, nil
}
