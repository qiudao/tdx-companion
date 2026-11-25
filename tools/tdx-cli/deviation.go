package main

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

type BoardSpec struct {
	Mkt         string // sh / sz
	Prefix      string // 1 / 0
	Code        string // 6 digits
	Key         string // 7 chars e.g. 0002685
	Board       string // 深市主板 / 沪市主板 / 创业板 / 科创板
	IdxKey      string // primary benchmark index key
	IdxName     string
	FallbackKey string
	FallbackNm  string
}

type DayDev struct {
	K        int     `json:"k"`
	BaseDate int     `json:"base_date"`
	BaseC    float64 `json:"base_close"`
	BaseIdx  float64 `json:"base_idx"`
	DevPct   float64 `json:"dev_pct"`
}

type DeviationResult struct {
	Key            string   `json:"key"`
	Code           string   `json:"code"`
	Name           string   `json:"name,omitempty"`
	Board          string   `json:"board"`
	IndexKey       string   `json:"index_key"`
	IndexName      string   `json:"index_name"`
	Date           int      `json:"date"`
	Open           float64  `json:"open"`
	High           float64  `json:"high"`
	Low            float64  `json:"low"`
	Close          float64  `json:"close"`
	PctToday       float64  `json:"pct_today"`
	IndexClose     float64  `json:"index_close"`
	Dev10          float64  `json:"dev10"`
	N10            int      `json:"n10"`
	BaseDate10     int      `json:"base_date10"`
	BaseClose10    float64  `json:"base_close10"`
	BaseIdx10      float64  `json:"base_idx10"`
	Triggered10    bool     `json:"triggered10"`
	Price100       float64  `json:"price100"`
	Tomorrow100    float64  `json:"tomorrow100"`
	TomorrowBase10 int      `json:"tomorrow_base_date10"`
	TomorrowBaseC  float64  `json:"tomorrow_base_close10"`
	Dev30          float64  `json:"dev30"`
	N30            int      `json:"n30"`
	BaseDate30     int      `json:"base_date30"`
	BaseClose30    float64  `json:"base_close30"`
	Triggered30    bool     `json:"triggered30"`
	Price200       float64  `json:"price200"`
	Tomorrow200    float64  `json:"tomorrow200"`
	Details        []DayDev `json:"details,omitempty"`
}

var codeRe = regexp.MustCompile(`(\d{6})`)

func ResolveBoard(raw string) (*BoardSpec, error) {
	s := strings.TrimSpace(raw)
	// Handle 7-char key directly
	if len(s) == 7 && (s[0] == '0' || s[0] == '1') && isAllDigits(s[1:]) {
		s = s[1:]
	}
	m := codeRe.FindStringSubmatch(s)
	if len(m) != 2 {
		return nil, fmt.Errorf("无法识别6位股票代码: %s", raw)
	}
	code := m[1]
	switch {
	case strings.HasPrefix(code, "688") || strings.HasPrefix(code, "689"):
		return &BoardSpec{
			Mkt: "sh", Prefix: "1", Code: code, Key: "1" + code,
			Board: "科创板", IdxKey: "1000688", IdxName: "科创50",
		}, nil
	case strings.HasPrefix(code, "60"):
		return &BoardSpec{
			Mkt: "sh", Prefix: "1", Code: code, Key: "1" + code,
			Board: "沪市主板", IdxKey: "1999998", IdxName: "上证A指",
			FallbackKey: "1000001", FallbackNm: "上证指数",
		}, nil
	case strings.HasPrefix(code, "30"):
		return &BoardSpec{
			Mkt: "sz", Prefix: "0", Code: code, Key: "0" + code,
			Board: "创业板", IdxKey: "0399102", IdxName: "创业板综",
		}, nil
	case strings.HasPrefix(code, "00"):
		return &BoardSpec{
			Mkt: "sz", Prefix: "0", Code: code, Key: "0" + code,
			Board: "深市主板", IdxKey: "0399107", IdxName: "深证A指",
		}, nil
	default:
		return nil, fmt.Errorf("暂不支持该板块代码: %s", code)
	}
}

func ceil2(v float64) float64 {
	return math.Ceil(v*100.0) / 100.0
}

func CalcDeviation(spec *BoardSpec, name string, stkBars []Bar, idxBars []Bar, idxKey, idxName string, targetDate int, includeDetails bool) (*DeviationResult, error) {
	idxMap := make(map[int]float64, len(idxBars))
	for _, b := range idxBars {
		idxMap[b.Date] = b.Close
	}

	type alignedRow struct {
		bar Bar
		idx float64
	}
	aligned := make([]alignedRow, 0, len(stkBars))
	for _, b := range stkBars {
		if targetDate > 0 && b.Date > targetDate {
			continue
		}
		if ic, ok := idxMap[b.Date]; ok && ic > 0 && b.Close > 0 {
			aligned = append(aligned, alignedRow{bar: b, idx: ic})
		}
	}

	n := len(aligned)
	if n <= 30 {
		return nil, fmt.Errorf("有效对齐K线不足31根（当前 %d 根）", n)
	}

	cur := aligned[n-1]
	prev := aligned[n-2]
	ref := func(k int) alignedRow { return aligned[n-1-k] }

	var dArr [11]float64
	maxD10 := -1e18
	maxN10 := 1
	trigN10 := 0
	var details []DayDev
	if includeDetails {
		details = make([]DayDev, 0, 10)
	}

	for k := 1; k <= 10; k++ {
		r := ref(k)
		dk := (cur.bar.Close/r.bar.Close - cur.idx/r.idx) * 100.0
		dArr[k] = dk
		if dk > maxD10 {
			maxD10 = dk
			maxN10 = k
		}
		if trigN10 == 0 && dk >= 100.0 {
			trigN10 = k
		}
		if includeDetails {
			details = append(details, DayDev{
				K: k, BaseDate: r.bar.Date, BaseC: r.bar.Close, BaseIdx: r.idx, DevPct: dk,
			})
		}
	}

	effN10 := maxN10
	if trigN10 > 0 {
		effN10 = trigN10
	}
	dynD10 := dArr[effN10]
	base10 := ref(effN10)

	// 30日内极值与固定30日（与通达信 LLVBARS(C, 30) 一致：最近30根K线中最低收盘价距今周期，取最近一次出现）
	minC30 := cur.bar.Close
	lowB30Raw := 0
	for i := 0; i < 30; i++ {
		c := ref(i).bar.Close
		if c < minC30 {
			minC30 = c
			lowB30Raw = i
		}
	}
	lowB30 := lowB30Raw
	if lowB30 < 1 {
		lowB30 = 1
	}
	rLow30 := ref(lowB30)
	rFix30 := ref(30)
	dynD30 := (cur.bar.Close/rLow30.bar.Close - cur.idx/rLow30.idx) * 100.0
	fixD30 := (cur.bar.Close/rFix30.bar.Close - cur.idx/rFix30.idx) * 100.0
	maxD30 := dynD30
	effN30 := lowB30
	if fixD30 > dynD30 {
		maxD30 = fixD30
		effN30 = 30
	}
	base30 := ref(effN30)

	// 今日临界价
	price100 := ceil2(base10.bar.Close * (cur.idx/base10.idx + 1.0))
	price200 := ceil2(base30.bar.Close * (cur.idx/base30.idx + 2.0))

	// 明日10日临界价（假设指数不变）
	minM10 := cur.bar.Close * 2.0
	minM10K := 0
	for k := 1; k <= 9; k++ {
		r := ref(k)
		v := r.bar.Close * (cur.idx/r.idx + 1.0)
		if v < minM10 {
			minM10 = v
			minM10K = k
		}
	}
	tomorrow100 := ceil2(minM10)
	tomorrowBase10 := ref(minM10K)

	// 明日30日临界价（假设指数不变）
	minC29 := cur.bar.Close
	mLowB30 := 0
	for i := 0; i < 29; i++ {
		c := ref(i).bar.Close
		if c < minC29 {
			minC29 = c
			mLowB30 = i
		}
	}
	rMLow30 := ref(mLowB30)
	rMFix30 := ref(29)
	m30Dyn := rMLow30.bar.Close * (cur.idx/rMLow30.idx + 2.0)
	m30Fix := rMFix30.bar.Close * (cur.idx/rMFix30.idx + 2.0)
	tomorrow200 := ceil2(math.Min(m30Dyn, m30Fix))

	return &DeviationResult{
		Key:            spec.Key,
		Code:           spec.Code,
		Name:           name,
		Board:          spec.Board,
		IndexKey:       idxKey,
		IndexName:      idxName,
		Date:           cur.bar.Date,
		Open:           cur.bar.Open,
		High:           cur.bar.High,
		Low:            cur.bar.Low,
		Close:          cur.bar.Close,
		PctToday:       (cur.bar.Close/prev.bar.Close - 1.0) * 100.0,
		IndexClose:     cur.idx,
		Dev10:          dynD10,
		N10:            effN10,
		BaseDate10:     base10.bar.Date,
		BaseClose10:    base10.bar.Close,
		BaseIdx10:      base10.idx,
		Triggered10:    trigN10 > 0,
		Price100:       price100,
		Tomorrow100:    tomorrow100,
		TomorrowBase10: tomorrowBase10.bar.Date,
		TomorrowBaseC:  tomorrowBase10.bar.Close,
		Dev30:          maxD30,
		N30:            effN30,
		BaseDate30:     base30.bar.Date,
		BaseClose30:    base30.bar.Close,
		Triggered30:    maxD30 >= 200.0,
		Price200:       price200,
		Tomorrow200:    tomorrow200,
		Details:        details,
	}, nil
}
