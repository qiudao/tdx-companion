# tdx-companion

macOS (Qt6) 同类自选股联动伴侣 + Windows (Go) 静默代理，用于配合 Parallels Desktop 中运行的通达信金融终端（`TdxW.exe`）。

## 功能特点

- **双向实时联动**：
  - 通达信切股 → macOS 端 300ms 轮询自动跟随，展示当前个股所属**自选股同分类**的其他个股实时涨跌幅与排名。
  - macOS 端点击或键盘 `↑`/`↓` 切换同类个股 → 通过 `UWM_STOCK` 消息直接切换通达信主图，带防循环机制。
- **零弹窗静默架构**：
  - Windows 端使用单文件 Go 程序（`-H=windowsgui` 无窗口编译）监听 `19876` 端口，开机自启运行于交互式 Session，彻底避免频繁调用 `prlctl exec` 导致的终端弹窗。
- **通达信经典黑红皮肤 + 无边框悬浮模式**：
  - 纯黑底、暗红分割线、金黄股名、红涨绿跌、22px 高密度行高。
  - 支持在有框/无框模式间一键切换（工具栏按钮、`⌘+B`、双击顶栏、右键菜单），无框模式下可拖动顶栏自由贴靠。
- **自选股分类管理**：
  - 自动解析通达信 `zxg.blk` 与 `shs/szs/bjs.tnf`，右键可随时调整个股所属分类并持久化至 `~/.tdx_companion_categories.json`。

## 项目结构

```text
.
├── CMakeLists.txt      # Qt6 CMake 构建配置
├── Makefile            # 快捷构建脚本
├── main.cpp            # Qt 入口
├── mainwindow.h/.cpp   # 主窗口 UI（经典黑红皮肤 + 无框切换）
├── tdxbridge.h/.cpp    # HTTP 轮询、切股、行情拉取与自选分类
├── win-agent/
    ├── go.mod
    ├── main.go         # Windows 端静默 Go HTTP Agent（控制/健康检查，端口 19876）
    └── marketdata.go   # 本地 vipdoc 日线/5分钟线解析、全市场情绪广度聚合与缓存
└── docs/
    └── AGENT_ARCHITECTURE.md  # TDX Local Sidecar 架构定位与 API 扩展规划
```

## 构建与运行

### 1. 编译 Windows Go Agent（ARM64 / AMD64）

```bash
cd win-agent
# Apple Silicon Parallels (Windows ARM64)
GOOS=windows GOARCH=arm64 go build -ldflags="-H=windowsgui -s -w" -o tdx-agent.exe .
# 如为 x86_64 Windows，将 GOARCH=arm64 改为 GOARCH=amd64
```

将 `tdx-agent.exe` 放至 Windows 虚拟机（如 `C:\new_tdx\tdx-agent.exe`），放行 TCP `19876` 端口并设为登录时以交互模式自启即可。

### 2. 编译 macOS Qt6 客户端

```bash
make build
open build/tdx_companion.app
```
