package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func usage() {
	fmt.Fprintf(os.Stderr, `tdx-cli — macOS 端通达信数据与控制 CLI（直连 win-agent:19876）

用法:
  tdx-cli dev [代码或名称...] [flags]   计算严重异动偏离值（不传参数自动查通达信当前看盘股）
    --scan          全市场扫描触发/逼近严重异动的股票
    --min float     --scan 时的最小 偏10 阈值（默认 80.0）
    --date int      指定历史截止日期 YYYYMMDD
    --detail        打印 D1~D10 偏离明细
    --json          JSON 格式输出
    --bars int      读取末尾K线根数（默认 45）

  tdx-cli health                        查看 win-agent 健康状态与最新日线日期
  tdx-cli status                        查看通达信当前正在查看的股票
  tdx-cli switch <代码或名称>           切换通达信主图个股
  tdx-cli blk list                      列出通达信自定义板块 (.blk)
  tdx-cli blk write <板块名> <代码...>  写入通达信自定义板块 (.blk)
  tdx-cli trade <subcmd>                交易网关 (health|assets|orders|capacity|submit|cancel)
`)
}

func resolveArgToSpec(arg string, nameToKey map[string]string) (*BoardSpec, error) {
	if spec, err := ResolveBoard(arg); err == nil {
		return spec, nil
	}
	if key, ok := nameToKey[strings.TrimSpace(arg)]; ok {
		return ResolveBoard(key)
	}
	return nil, fmt.Errorf("无法解析股票代码或名称: %s", arg)
}

func cmdDev(c *Client, args []string) error {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	scan := fs.Bool("scan", false, "全市场扫描")
	minDev := fs.Float64("min", 80.0, "全市场扫描的最小 偏10 阈值")
	targetDate := fs.Int("date", 0, "截止日期 YYYYMMDD")
	detail := fs.Bool("detail", false, "打印 D1~D10 明细")
	asJSON := fs.Bool("json", false, "JSON 输出")
	nBars := fs.Int("bars", 45, "读取末尾K线根数")
	if err := fs.Parse(args); err != nil {
		return err
	}

	health, err := c.Health()
	if err != nil {
		return fmt.Errorf("连接 win-agent 失败 (%s): %w", c.BaseURL, err)
	}
	keyToName, nameToKey := c.LoadSymbols()

	if *scan {
		return runScan(c, health, keyToName, *minDev, *targetDate, *nBars, *asJSON)
	}

	targets := fs.Args()
	if len(targets) == 0 {
		if health.TdxStock == "" {
			return fmt.Errorf("未传入股票代码，且通达信当前窗口未识别到个股（标题: %q）", health.TdxTitle)
		}
		targets = []string{health.TdxStock}
	}

	idxCache := map[string][]Bar{}
	getIdx := func(spec *BoardSpec) ([]Bar, string, string, error) {
		if b, ok := idxCache[spec.IdxKey]; ok && len(b) > 0 {
			return b, spec.IdxKey, spec.IdxName, nil
		}
		b, err := c.Bars(spec.IdxKey, "day", *nBars)
		if err == nil && len(b) > 0 {
			idxCache[spec.IdxKey] = b
			return b, spec.IdxKey, spec.IdxName, nil
		}
		if spec.FallbackKey != "" {
			if fb, ok := idxCache[spec.FallbackKey]; ok && len(fb) > 0 {
				return fb, spec.FallbackKey, spec.FallbackNm, nil
			}
			fb, err2 := c.Bars(spec.FallbackKey, "day", *nBars)
			if err2 == nil && len(fb) > 0 {
				idxCache[spec.FallbackKey] = fb
				return fb, spec.FallbackKey, spec.FallbackNm, nil
			}
		}
		return nil, "", "", fmt.Errorf("获取基准指数 %s 失败: %w", spec.IdxKey, err)
	}

	results := make([]*DeviationResult, 0, len(targets))
	if !*asJSON {
		fmt.Printf("[数据源: win-agent %s | v%s | lday最新=%d | 更新=%s]\n",
			c.BaseURL, health.Version, health.LdayLatestDate, health.LdayUpdatedAt)
	}

	for _, raw := range targets {
		spec, err := resolveArgToSpec(raw, nameToKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[跳过] %v\n", err)
			continue
		}
		stkBars, err := c.Bars(spec.Key, "day", *nBars)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s%s] 获取日线失败: %v\n", strings.ToUpper(spec.Mkt), spec.Code, err)
			continue
		}
		idxBars, idxKey, idxName, err := getIdx(spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s%s] %v\n", strings.ToUpper(spec.Mkt), spec.Code, err)
			continue
		}
		res, err := CalcDeviation(spec, keyToName[spec.Key], stkBars, idxBars, idxKey, idxName, *targetDate, *detail)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s%s] 计算失败: %v\n", strings.ToUpper(spec.Mkt), spec.Code, err)
			continue
		}
		results = append(results, res)
		if !*asJSON {
			printResult(res, *detail)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	return nil
}

func runScan(c *Client, health *HealthResp, keyToName map[string]string, minDev float64, targetDate, nBars int, asJSON bool) error {
	if nBars < 45 {
		nBars = 45
	}
	// Preload all 4 benchmark indices
	idxBars := map[string][]Bar{}
	for _, k := range []string{"1999998", "1000001", "1000688", "0399107", "0399102"} {
		if b, err := c.Bars(k, "day", nBars+15); err == nil && len(b) > 0 {
			idxBars[k] = b
		}
	}

	cross, err := c.CrossSectionBars(nBars + 15)
	if err != nil {
		return fmt.Errorf("拉取全市场横截面失败: %w", err)
	}

	var hits []*DeviationResult
	for key, bars := range cross {
		if len(bars) <= 30 {
			continue
		}
		spec, err := ResolveBoard(key)
		if err != nil {
			continue
		}
		ib := idxBars[spec.IdxKey]
		ik, in := spec.IdxKey, spec.IdxName
		if len(ib) == 0 && spec.FallbackKey != "" {
			ib = idxBars[spec.FallbackKey]
			ik, in = spec.FallbackKey, spec.FallbackNm
		}
		if len(ib) == 0 {
			continue
		}
		res, err := CalcDeviation(spec, keyToName[key], bars, ib, ik, in, targetDate, false)
		if err != nil {
			continue
		}
		if res.Dev10 >= minDev || res.Triggered10 || res.Triggered30 {
			hits = append(hits, res)
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		return hits[i].Dev10 > hits[j].Dev10
	})

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(hits)
	}

	fmt.Printf("[全市场严重异动扫描 | win-agent %s | lday=%d | 阈值 偏10>=%.1f%% | 命中 %d 只]\n",
		c.BaseURL, health.LdayLatestDate, minDev, len(hits))
	fmt.Printf("%-8s %-10s %-8s %7s %7s %8s %4s %7s %7s %8s %s\n",
		"代码", "名称", "板块", "收盘", "涨幅", "偏10", "N10", "价100", "明100", "偏30", "状态")
	for _, r := range hits {
		status := ""
		if r.Triggered10 {
			status += "[10日严重异动]"
		}
		if r.Triggered30 {
			status += "[30日严重异动]"
		}
		if status == "" {
			status = "逼近红线"
		}
		fmt.Printf("%-8s %-10s %-8s %7.2f %+6.2f%% %+7.2f%% %4d %7.2f %7.2f %+7.2f%% %s\n",
			r.Code, r.Name, r.Board, r.Close, r.PctToday, r.Dev10, r.N10, r.Price100, r.Tomorrow100, r.Dev30, status)
	}
	return nil
}

func printResult(r *DeviationResult, detail bool) {
	trig10 := "否"
	if r.Triggered10 {
		trig10 = "是 (触发!)"
	}
	trig30 := "否"
	if r.Triggered30 {
		trig30 = "是 (触发!)"
	}
	title := r.Code
	if r.Name != "" {
		title = fmt.Sprintf("%s %s", r.Code, r.Name)
	}
	fmt.Printf("================ %s (%s | 基准: %s %s) ================\n",
		title, r.Board, r.IndexName, r.IndexKey[1:])
	fmt.Printf("最新交易日: %d | 开=%.2f 高=%.2f 低=%.2f 收=%.2f (%+.2f%%) | 指数=%.2f\n",
		r.Date, r.Open, r.High, r.Low, r.Close, r.PctToday, r.IndexClose)
	fmt.Printf("偏10: %+.2f%% (N10=%d, 基准日=%d, 基准价=%.2f, 基准指=%.2f) | 10日严重异动: %s\n",
		r.Dev10, r.N10, r.BaseDate10, r.BaseClose10, r.BaseIdx10, trig10)
	fmt.Printf("价100: %.2f | 明100: %.2f (明日基准日=%d, 基准价=%.2f)\n",
		r.Price100, r.Tomorrow100, r.TomorrowBase10, r.TomorrowBaseC)
	fmt.Printf("偏30: %+.2f%% (N30=%d, 基准日=%d, 基准价=%.2f) | 30日严重异动: %s\n",
		r.Dev30, r.N30, r.BaseDate30, r.BaseClose30, trig30)
	fmt.Printf("价200: %.2f | 明200: %.2f\n", r.Price200, r.Tomorrow200)
	if detail && len(r.Details) > 0 {
		fmt.Println("--- D1~D10 偏离明细 ---")
		for _, d := range r.Details {
			fmt.Printf("  D%2d (%d C=%.2f I=%.2f): %+.4f%%\n", d.K, d.BaseDate, d.BaseC, d.BaseIdx, d.DevPct)
		}
	}
}

func cmdSwitch(c *Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法: tdx-cli switch <代码或名称>")
	}
	_, nameToKey := c.LoadSymbols()
	spec, err := resolveArgToSpec(args[0], nameToKey)
	if err != nil {
		return err
	}
	if err := c.Switch(spec.Key); err != nil {
		return err
	}
	fmt.Printf("已切换通达信主图至 %s (%s)\n", spec.Code, spec.Key)
	return nil
}

func cmdBlk(c *Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法: tdx-cli blk list | write <板块名> <代码...>")
	}
	switch args[0] {
	case "list":
		blks, err := c.Blocks()
		if err != nil {
			return err
		}
		for _, b := range blks {
			fmt.Println(b)
		}
		return nil
	case "write":
		if len(args) < 3 {
			return fmt.Errorf("用法: tdx-cli blk write <板块名> <代码或名称...>")
		}
		name := args[1]
		_, nameToKey := c.LoadSymbols()
		keys := make([]string, 0, len(args)-2)
		for _, raw := range args[2:] {
			spec, err := resolveArgToSpec(raw, nameToKey)
			if err != nil {
				return err
			}
			keys = append(keys, spec.Key)
		}
		path, err := c.WriteBlock(name, keys)
		if err != nil {
			return err
		}
		fmt.Printf("已写入板块 %s (%d 只) -> %s\n", name, len(keys), path)
		return nil
	default:
		return fmt.Errorf("未知 blk 子命令: %s", args[0])
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	sub := os.Args[1]
	if sub == "-h" || sub == "--help" || sub == "help" {
		usage()
		return
	}

	c, err := NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}

	switch sub {
	case "dev", "deviation":
		err = cmdDev(c, os.Args[2:])
	case "health":
		var h *HealthResp
		h, err = c.Health()
		if err == nil {
			b, _ := json.MarshalIndent(h, "", "  ")
			fmt.Println(string(b))
		}
	case "status":
		var s *StatusResp
		s, err = c.Status()
		if err == nil {
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
		}
	case "switch":
		err = cmdSwitch(c, os.Args[2:])
	case "blk":
		err = cmdBlk(c, os.Args[2:])
	case "trade":
		err = cmdTrade(c, os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}
