package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const tradeAgentPort = 19877

type TradeClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type OcrResp struct {
	OK       bool       `json:"ok"`
	RowCount int        `json:"row_count"`
	Rows     [][]string `json:"rows"`
}

type PositionRow struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	TotalQty    int     `json:"total_qty"`
	SellableQty int     `json:"sellable_qty"`
	FrozenQty   int     `json:"frozen_qty"`
	CostPrice   float64 `json:"cost_price"`
	CurPrice    float64 `json:"cur_price"`
	MarketValue float64 `json:"market_value"`
	Verified    bool    `json:"verified"`
}

type AssetSummary struct {
	TotalAssets      float64 `json:"total_assets"`
	NetAssets        float64 `json:"net_assets"`
	Debt             float64 `json:"debt"`
	MaintenanceRatio float64 `json:"maintenance_ratio"`
	MarketValue      float64 `json:"market_value"`
	AvailableCash    float64 `json:"available_cash"`
	AvailableMargin  float64 `json:"available_margin"`
	WithdrawableCash float64 `json:"withdrawable_cash"`
	RatioVerified    bool    `json:"ratio_verified"`
}

type OpenOrderRow struct {
	Time       string `json:"time"`
	ContractNo string `json:"contract_no"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Raw        string `json:"raw"`
}

func NewTradeClient() (*TradeClient, error) {
	ip, err := DetectVMIP()
	if err != nil {
		return nil, err
	}
	return &TradeClient{
		BaseURL: fmt.Sprintf("http://%s:%d", ip, tradeAgentPort),
		Token:   strings.TrimSpace(os.Getenv("TRADE_AGENT_TOKEN")),
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (tc *TradeClient) doRaw(method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, tc.BaseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tc.Token != "" {
		req.Header.Set("X-Trade-Token", tc.Token)
	}
	resp, err := tc.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func runVisionOCR(pngBytes []byte) (*OcrResp, error) {
	tmp, err := os.CreateTemp("", "tdx-ocr-*.png")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(pngBytes); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	ocrBin := "vision-ocr"
	if p, err := exec.LookPath("vision-ocr"); err == nil {
		ocrBin = p
	}
	out, err := exec.Command(ocrBin, tmpPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("vision-ocr 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	var resp OcrResp
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("vision-ocr JSON 解析失败: %w", err)
	}
	return &resp, nil
}

func (tc *TradeClient) captureOCR(target, tab string) (*OcrResp, error) {
	path := fmt.Sprintf("/screenshot?target=%s&tab=%s&maximize=1", url.QueryEscape(target), url.QueryEscape(tab))
	st, pngBytes, err := tc.doRaw(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if st >= 400 {
		return nil, fmt.Errorf("screenshot HTTP %d: %s", st, string(pngBytes))
	}
	return runVisionOCR(pngBytes)
}

var (
	reFloat  = regexp.MustCompile(`-?\d+\.\d+`)
	reNum    = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
	reCode6  = regexp.MustCompile(`\b((?:00|30|60|68)\d{4})\b`)
	reTime6  = regexp.MustCompile(`\d{2}:\d{2}:\d{2}`)
)

func parseFloatCell(s string) (float64, bool) {
	m := reNum.FindString(strings.ReplaceAll(s, ",", ""))
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(m, 64)
	return v, err == nil
}

func parsePositionsOCR(ocr *OcrResp, keyToName map[string]string) []PositionRow {
	var out []PositionRow
	for _, row := range ocr.Rows {
		joined := strings.Join(row, " ")
		mCode := reCode6.FindStringSubmatch(joined)
		if len(mCode) != 2 {
			continue
		}
		code := mCode[1]
		// Lookup canonical name from TDX dictionary
		name := ""
		if spec, err := ResolveBoard(code); err == nil {
			name = keyToName[spec.Key]
		}
		// Extract all numbers after the stock code
		idxCode := strings.Index(joined, code)
		tail := joined[idxCode+len(code):]
		nums := reNum.FindAllString(strings.ReplaceAll(tail, ",", ""), -1)
		if len(nums) < 5 {
			continue
		}
		totF, _ := strconv.ParseFloat(nums[0], 64)
		sellF, _ := strconv.ParseFloat(nums[1], 64)
		frozF, _ := strconv.ParseFloat(nums[2], 64)
		costF, _ := strconv.ParseFloat(nums[3], 64)
		curF, _ := strconv.ParseFloat(nums[4], 64)

		tot := int(math.Round(totF))
		sell := int(math.Round(sellF))
		froz := int(math.Round(frozF))
		mv := math.Round(float64(tot)*curF*100) / 100
		verified := name != "" && tot > 0 && tot%100 == 0 && sell >= 0 && sell <= tot && curF > 0

		out = append(out, PositionRow{
			Code:        code,
			Name:        name,
			TotalQty:    tot,
			SellableQty: sell,
			FrozenQty:   froz,
			CostPrice:   costF,
			CurPrice:    curF,
			MarketValue: mv,
			Verified:    verified,
		})
	}
	return out
}

func parseAssetsOCR(ocr *OcrResp) AssetSummary {
	var a AssetSummary
	// Flatten tokens with row context to match label -> next numeric value on same row
	for _, row := range ocr.Rows {
		for i, cell := range row {
			nextNum := func() (float64, bool) {
				for j := i + 1; j < len(row); j++ {
					if v, ok := parseFloatCell(row[j]); ok {
						return v, true
					}
				}
				return 0, false
			}
			switch {
			case strings.Contains(cell, "信用资产") && a.TotalAssets == 0:
				if v, ok := nextNum(); ok && v > 0 {
					a.TotalAssets = v
				}
			case strings.Contains(cell, "信用资产") && a.TotalAssets > 0 && a.NetAssets == 0:
				if v, ok := nextNum(); ok && v > 0 && v <= a.TotalAssets {
					a.NetAssets = v
				}
			case strings.Contains(cell, "总资产") && a.TotalAssets == 0:
				if v, ok := nextNum(); ok && v > 0 {
					a.TotalAssets = v
				}
			case strings.Contains(cell, "净资产") && a.NetAssets == 0:
				if v, ok := nextNum(); ok && v > 0 {
					a.NetAssets = v
				}
			case strings.Contains(cell, "维持担保"):
				if v, ok := nextNum(); ok && v > 0 {
					a.MaintenanceRatio = v
				}
			case strings.Contains(cell, "标的券市值") || strings.Contains(cell, "证券市值"):
				if v, ok := nextNum(); ok {
					a.MarketValue = v
				}
			case strings.Contains(cell, "可用金额") || strings.Contains(cell, "可买担保"):
				if v, ok := nextNum(); ok && a.AvailableCash == 0 {
					a.AvailableCash = v
				}
			case strings.Contains(cell, "可用保证金"):
				if v, ok := nextNum(); ok {
					a.AvailableMargin = v
				}
			case strings.Contains(cell, "可取金额"):
				if v, ok := nextNum(); ok {
					a.WithdrawableCash = v
				}
			}
		}
		// Fallback for rows where label wrapped onto previous line (e.g. ["圆担保品划转", "99850.68", "可买担保….", "63142.68"])
		if a.TotalAssets == 0 {
			for i, cell := range row {
				if strings.Contains(cell, "可买担保") && i > 0 {
					if v, ok := parseFloatCell(row[i-1]); ok && v > 0 {
						a.TotalAssets = v
					}
				}
			}
		}
	}

	if a.TotalAssets > 0 && a.NetAssets > 0 && a.TotalAssets >= a.NetAssets {
		a.Debt = math.Round((a.TotalAssets-a.NetAssets)*100) / 100
		if a.Debt > 0 && a.MaintenanceRatio > 0 {
			calcRatio := a.TotalAssets / a.Debt * 100.0
			if math.Abs(calcRatio-a.MaintenanceRatio) <= 1.0 {
				a.RatioVerified = true
			}
		} else if a.Debt == 0 {
			a.RatioVerified = true
		}
	}
	return a
}

func parseOrdersOCR(ocr *OcrResp, keyToName map[string]string) []OpenOrderRow {
	var out []OpenOrderRow
	for _, row := range ocr.Rows {
		joined := strings.Join(row, " ")
		if strings.Contains(joined, "暂无相应数据") || strings.Contains(joined, "合同编号") {
			continue
		}
		mCode := reCode6.FindStringSubmatch(joined)
		if len(mCode) != 2 {
			continue
		}
		code := mCode[1]
		name := ""
		if spec, err := ResolveBoard(code); err == nil {
			name = keyToName[spec.Key]
		}
		tStr := reTime6.FindString(joined)
		// Extract contract number (digits immediately before code or after time)
		cNo := ""
		idxCode := strings.Index(joined, code)
		before := joined[:idxCode]
		if tStr != "" {
			if idxT := strings.Index(before, tStr); idxT >= 0 {
				before = before[idxT+len(tStr):]
			}
		}
		if nums := regexp.MustCompile(`\d{3,12}`).FindAllString(before, -1); len(nums) > 0 {
			cNo = nums[len(nums)-1]
		}
		out = append(out, OpenOrderRow{
			Time:       tStr,
			ContractNo: cNo,
			Code:       code,
			Name:       name,
			Raw:        joined,
		})
	}
	return out
}

func prettyPrintJSON(raw []byte) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Println(string(raw))
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func printAnyJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func cmdTrade(c *Client, args []string) error {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, `用法:
  tdx-cli trade health                                       查看 trade-agent 状态
  tdx-cli trade snapshot                                     一键全量校验快照 (资产+持仓+挂单+数学闭环校验)
  tdx-cli trade assets [--raw]                               F4 资产页解析 (自动最大化防截断)
  tdx-cli trade positions [--raw]                            F2 持仓表解析 + 码表/市值校验
  tdx-cli trade orders [--raw]                               F3 在途可撤委托解析
  tdx-cli trade capacity <order_type> <代码或名称> [价格]    零验证码查可买/可卖股数与两融指标
  tdx-cli trade submit <order_type> <代码> <价格> <数量> --live   实盘下单 (collateral_buy|margin_buy|collateral_sell|repay_by_sell)
  tdx-cli trade cancel [all|buy|sell]                        一键撤单 (默认 all)
`)
		return nil
	}

	tc, err := NewTradeClient()
	if err != nil {
		return err
	}
	keyToName, nameToKey := c.LoadSymbols()
	resolveCode := func(raw string) (string, error) {
		spec, err := resolveArgToSpec(raw, nameToKey)
		if err != nil {
			return "", err
		}
		return spec.Code, nil
	}
	hasRaw := false
	for _, a := range args[1:] {
		if a == "--raw" {
			hasRaw = true
		}
	}

	switch args[0] {
	case "health":
		_, raw, err := tc.doRaw(http.MethodGet, "/health", nil)
		if err != nil {
			return err
		}
		prettyPrintJSON(raw)
		return nil

	case "assets":
		ocr, err := tc.captureOCR("main", "F4")
		if err != nil {
			return err
		}
		if hasRaw {
			printAnyJSON(ocr)
			return nil
		}
		printAnyJSON(parseAssetsOCR(ocr))
		return nil

	case "positions":
		ocr, err := tc.captureOCR("grid", "F2")
		if err != nil {
			return err
		}
		if hasRaw {
			printAnyJSON(ocr)
			return nil
		}
		pos := parsePositionsOCR(ocr, keyToName)
		sumMV := 0.0
		for _, p := range pos {
			sumMV += p.MarketValue
		}
		printAnyJSON(map[string]any{
			"ok":               true,
			"count":            len(pos),
			"sum_market_value": math.Round(sumMV*100) / 100,
			"positions":        pos,
		})
		return nil

	case "orders":
		ocr, err := tc.captureOCR("grid", "F3")
		if err != nil {
			return err
		}
		if hasRaw {
			printAnyJSON(ocr)
			return nil
		}
		orders := parseOrdersOCR(ocr, keyToName)
		printAnyJSON(map[string]any{
			"ok":     true,
			"count":  len(orders),
			"orders": orders,
		})
		return nil

	case "snapshot":
		ocrAssets, err := tc.captureOCR("main", "F4")
		if err != nil {
			return err
		}
		assets := parseAssetsOCR(ocrAssets)
		ocrPos, err := tc.captureOCR("grid", "F2")
		if err != nil {
			return err
		}
		positions := parsePositionsOCR(ocrPos, keyToName)
		ocrOrd, err := tc.captureOCR("grid", "F3")
		if err != nil {
			return err
		}
		orders := parseOrdersOCR(ocrOrd, keyToName)

		sumMV := 0.0
		allPosOK := true
		for _, p := range positions {
			sumMV += p.MarketValue
			if !p.Verified {
				allPosOK = false
			}
		}
		sumMV = math.Round(sumMV*100) / 100
		mvMatch := (assets.MarketValue == 0 && sumMV == 0) ||
			(assets.MarketValue > 0 && math.Abs(sumMV-assets.MarketValue)/assets.MarketValue < 0.01)

		printAnyJSON(map[string]any{
			"ok":                  true,
			"math_verified":       assets.RatioVerified && mvMatch && allPosOK,
			"mv_cross_check_ok":   mvMatch,
			"sum_positions_mv":    sumMV,
			"assets":              assets,
			"positions":           positions,
			"open_orders":         orders,
		})
		return nil

	case "capacity":
		if len(args) < 3 {
			return fmt.Errorf("用法: tdx-cli trade capacity <collateral_buy|margin_buy|collateral_sell|repay_by_sell> <代码或名称> [价格]")
		}
		orderType := args[1]
		code, err := resolveCode(args[2])
		if err != nil {
			return err
		}
		q := fmt.Sprintf("/stock/capacity?order_type=%s&code=%s", url.QueryEscape(orderType), url.QueryEscape(code))
		if len(args) >= 4 {
			q += "&price=" + url.QueryEscape(args[3])
		}
		_, raw, err := tc.doRaw(http.MethodGet, q, nil)
		if err != nil {
			return err
		}
		prettyPrintJSON(raw)
		return nil

	case "submit":
		if len(args) < 5 {
			return fmt.Errorf("用法: tdx-cli trade submit <order_type> <代码或名称> <价格> <数量> [--live]")
		}
		orderType := args[1]
		code, err := resolveCode(args[2])
		if err != nil {
			return err
		}
		price := args[3]
		qty := args[4]
		live := false
		for _, a := range args[5:] {
			if a == "--live" {
				live = true
			}
		}
		if !live {
			return fmt.Errorf("安全保护：实盘下单必须显式追加 --live 参数")
		}
		payload := map[string]any{
			"order_type":          orderType,
			"code":                code,
			"price":               price,
			"qty":                 qty,
			"confirm_live":        true,
			"auto_confirm_dialog": true,
			"idempotency_key":     fmt.Sprintf("cli-%s-%s-%d", orderType, code, time.Now().UnixMilli()),
		}
		st, raw, err := tc.doRaw(http.MethodPost, "/order/submit", payload)
		if err != nil {
			return err
		}
		prettyPrintJSON(raw)
		if st >= 400 {
			return fmt.Errorf("submit failed (HTTP %d)", st)
		}
		return nil

	case "cancel":
		action := "cancel_all"
		if len(args) >= 2 {
			switch strings.ToLower(args[1]) {
			case "all", "cancel_all":
				action = "cancel_all"
			case "buy", "cancel_buy":
				action = "cancel_buy"
			case "sell", "cancel_sell":
				action = "cancel_sell"
			default:
				return fmt.Errorf("未知撤单类型: %s (all|buy|sell)", args[1])
			}
		}
		st, raw, err := tc.doRaw(http.MethodPost, "/order/cancel", map[string]any{
			"action":              action,
			"auto_confirm_dialog": true,
		})
		if err != nil {
			return err
		}
		prettyPrintJSON(raw)
		if st >= 400 {
			return fmt.Errorf("cancel failed (HTTP %d)", st)
		}
		return nil

	default:
		return fmt.Errorf("未知 trade 子命令: %s", args[0])
	}
}
