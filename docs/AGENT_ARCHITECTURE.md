# TDX Local Sidecar (`tdx-agent`) 架构与扩展设计

## 1. 核心定位

`tdx-agent` 运行在 Parallels Windows 虚拟机内（交互式 Session 1，监听 `0.0.0.0:19876`），定位为 **通达信本地「数据 + 控制」统一网关（Local Sidecar）**：

- 将通达信封闭的 **Win32 窗口消息** 与 **本地二进制文件（`.day` / `.lc5` / `.tnf` / `.blk`）** 封装为轻量、极速的局域网 HTTP/JSON API。
- 服务对象包括：macOS Qt6 联动伴侣、Python 复盘脚本、浏览器情绪看板以及 AI 助手直连。

## 2. 职责边界

| 层级 | 职责 | 说明 |
| :--- | :--- | :--- |
| **控制层 (Control)** | 读取当前焦点股、发送 `UWM_STOCK` 切股、读写 `.blk` 板块 | 必须运行于交互式 Session 1，零弹窗、零回调泄漏 |
| **数据层 (Data)** | 直接解析 `C:\new_tdx\vipdoc` 日线/分钟线及 `hq_cache` 码表 | 避免跨虚拟机拷贝海量小文件，按需返回 JSON |
| **聚合层 (Analytics)** | 全市场本地并发扫描与聚合（情绪广度、条件选股） | 5500+ 只 `.day` 本地扫描 < 0.3s，结果按文件修改时间缓存 |

> **不做**：复杂 UI（留在 macOS 端）、重度回测引擎、外部行情抓取。保持单二进制、零外部依赖、7×24 常驻稳定。

## 3. HTTP API 规范

### 控制与文件接口（已有）
- `GET /status` — 返回当前通达信窗口标题与正在查看的股票名称
- `GET /switch?key=0002242` — 向通达信发送 `UWM_STOCK` 切换个股（`1`=沪, `0`=深, `2`=北）
- `GET /file?name=zxg.blk` — 下载白名单内的通达信缓存/自选文件
- `POST /blk?name=ai_pick` — 将股票代码列表回写为通达信自定义板块 `.blk`

### 阶段 1：数据与情绪基础接口（已实现）
- `GET /api/health`
  - 返回 Agent 版本、运行时长、通达信窗口状态、以及本地 `sh999999.day` 最新交易日。
- `GET /api/breadth?days=45`
  - 扫描沪/深/北交所全市场 A 股 `.day`（剔除指数/ETF/债券），返回近 `days` 个交易日的：
    - `total` / `up` / `down` / `flat` / `down_pct`
    - `median_pct`（全市场涨跌幅中位数）
    - `drop3` / `drop5` / `drop7` / `rise5` / `limit_down` / `limit_up`
    - `amt_yi`（两市+北交所成交额，亿元）
    - `sh_close` / `sh_pct`（上证指数对照）
  - 内置基于 `sh999999.day` 修改时间的内存缓存，盘后数据未更新时毫秒级返回。
- `GET /api/kline?key=0002242&period=day&limit=120`
  - 读取单股本地 K 线（支持 `period=day` 日线与 `period=5m` 5 分钟线 `.lc5`），返回最新 `limit` 根 OHLCV JSON。

### 阶段 2：选股与板块闭环（规划）
- `POST /api/screen` — 本地并发扫描全市场 `.day`，支持常用结构条件（连跌反包、放量突破、均线多头等），可选参数 `write_blk=ai_pick` 一键回写通达信自定义板块。
- `GET /api/watchlist/stats` — 基于本地 `.day` 计算自选股近 5/20/60 日强弱与相对收益排行。

### 阶段 3：性能与生态（规划）
- 支持 `period=1m`（`.lc1`）及盘中分钟级异动扫描。
- 统一 OpenAPI / MCP 封装，便于 AI 助手直接组合查询与复盘。
