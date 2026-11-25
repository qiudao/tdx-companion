package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const tradeAgentPort = 19877

type TradeClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
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

func runVisionOCR(pngBytes []byte) ([]byte, error) {
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
	return out, nil
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

func cmdTrade(c *Client, args []string) error {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, `用法:
  tdx-cli trade health                                       查看 trade-agent 状态
  tdx-cli trade assets                                       F4 资产页截图 + Vision OCR
  tdx-cli trade orders                                       F3 可撤委托截图 + Vision OCR
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
	_, nameToKey := c.LoadSymbols()
	resolveCode := func(raw string) (string, error) {
		spec, err := resolveArgToSpec(raw, nameToKey)
		if err != nil {
			return "", err
		}
		return spec.Code, nil
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
		st, pngBytes, err := tc.doRaw(http.MethodGet, "/screenshot?target=main&tab=F4", nil)
		if err != nil {
			return err
		}
		if st >= 400 {
			return fmt.Errorf("screenshot HTTP %d: %s", st, string(pngBytes))
		}
		ocrOut, err := runVisionOCR(pngBytes)
		if err != nil {
			return err
		}
		fmt.Println(string(ocrOut))
		return nil

	case "orders":
		st, pngBytes, err := tc.doRaw(http.MethodGet, "/screenshot?target=grid&tab=F3", nil)
		if err != nil {
			return err
		}
		if st >= 400 {
			return fmt.Errorf("screenshot HTTP %d: %s", st, string(pngBytes))
		}
		ocrOut, err := runVisionOCR(pngBytes)
		if err != nil {
			return err
		}
		fmt.Println(string(ocrOut))
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
