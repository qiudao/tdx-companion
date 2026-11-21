# TDX Data & Control Gateway (`tdx-agent`) 架构设计

## 1. 核心定位

`tdx-agent` 运行在 Parallels Windows 虚拟机内（交互式 Session 1，监听 `0.0.0.0:19876`），严格定位为 **纯「数据层 + 窗口控制层」基础设施（Zero Business Logic）**：

- **Windows 端只做不可替代的事**：Win32 窗口控制 + 本地通达信二进制文件（`.day` / `.lc5` / `.tnf` / `.blk`）的结构化读取与输出。
- **Mac 端负责所有业务与分析**：Qt6 联动伴侣、离线 SQLite 复盘库（`~/.tdx_local_data/market.db`）、Python 选股/统计脚本及 AI 助手查询。
- 业务口径（涨跌停阈值、是否剔除 ST、复权方式、各类情绪/选股指标）全部留在 Mac 端，**Windows 端一次部署、长期不动**。

## 2. 职责边界

| 端 | 职责 | 说明 |
| :--- | :--- | :--- |
| **Windows (`tdx-agent`)** | 窗口控制原语 + 结构化原始数据原语 | 单二进制、零外部依赖、7×24 常驻，不写具体指标/策略 |
| **macOS (`tools/tdx_sync.py`)** | 增量同步到本地离线库 + 任意统计/复盘/选股 | 写入 `~/.tdx_local_data/market.db`，**关闭虚拟机也可离线复盘** |
| **macOS (`tdx_companion`)** | 盘中/盘后实时联动 UI | 跟随通达信切股、同类自选展示、一键切回通达信 |

## 3. HTTP 原语接口规范（`v2.0`）

### 3.1 元信息与窗口控制
- `GET /health` — Agent 版本、运行时长、通达信窗口在线状态、本地日线最新日期
- `GET /status` — 当前通达信窗口标题与正在查看的股票名称
- `GET /switch?key=0002242` — 向通达信发送 `UWM_STOCK` 切换个股（`1`=沪, `0`=深, `2`=北）

### 3.2 结构化数据原语（`/data/*`）
- `GET /data/bars?key=0002242&period=day|5m&limit=120`
  - **纵切（时间序列）**：读取单股日线（`.day`）或 5 分钟线（`.lc5`），返回标准 OHLCV JSON。
- `GET /data/cross-section?days=60&start=20260901&format=csv|json`
  - **横切（全市场横截面）**：读取全市场 A 股近 `days` 个交易日（或 `>= start`）的 `date,key,board,open,high,low,close,prev_close,amount,volume`。
  - 自动支持 `Accept-Encoding: gzip` 压缩（60 个交易日全市场仅 ~6MB，局域网毫秒级传输），供 Mac 端增量同步至本地 SQLite 离线库或直接做任意统计。
- `GET /data/blocks` — 列出 `T0002/blocknew` 下所有自定义板块 `.blk` 名称
- `GET /file?name=shs.tnf` — 下载白名单内的原始缓存/码表/自选文件（`zxg.blk`, `shs/szs/bjs.tnf` 等）
- `POST /blk?name=ai_pick` — 将 Mac 端选出的 7 位代码列表回写为通达信自定义板块 `.blk`

## 4. 离线复盘工作流（Mac 端）

```bash
# 1. 盘后在通达信下载完日线数据后，一键增量同步至 Mac 本地 SQLite（~/.tdx_local_data/market.db）
python3 tools/tdx_sync.py sync --days 120

# 2. 之后随时可离线运行分析（即使关闭 Parallels Windows 虚拟机）
python3 tools/tdx_sync.py breadth --days 20
```
