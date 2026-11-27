# tdx-hub — 云端 A 股实时情报中心

零外部依赖 Go HTTP 常驻服务，部署在腾讯云主机 **`tengxun_kanglei`**，用于持续抓取、缓存并对外提供 A 股实时公告与盘面数据，支持后续按模块横向扩展。

## 1. 部署信息（非敏感元数据）

- **目标主机**：`tengxun_kanglei`（SSH 免密：`ssh tengxun_kanglei`，本机 `~/.ssh/config` 已配置）
- **公网 IP**：`1.15.146.228`
- **二进制路径**：`/opt/tdx-hub/tdx-hub`
- **监听端口**：`127.0.0.1:18900`
- **托管方式**：`systemd` 服务 `tdx-hub.service`（开机自启 + 异常自动重启）
- **反向代理**：`Caddy`（`/etc/caddy/Caddyfile` 将 `:80` 反代至 `127.0.0.1:18900`）

## 2. 现有接口

| 路径 | 方法 | 说明 |
| :--- | :--- | :--- |
| `/` | `GET` | 内置暗黑交易台 Web 看板（公告 + 涨停板双 Tab，自动刷新） |
| `/health` | `GET` | 服务健康状态、各模块缓存条数与最后刷新时间 |
| `/api/notices` | `GET` | 巨潮资讯（CNINFO）严重异常波动 / 停牌核查公告（后台每 60s 刷新；支持 `?days=7&keywords=严重异常波动,停牌核查&refresh=1`） |
| `/api/limit-up` | `GET` | 实时涨停板题材分类与涨停原因（选股宝 + 东财融合，后台每 45s 刷新；支持 `?refresh=1`） |

## 3. 快速访问

- **公网直访**（需在腾讯云控制台防火墙放行 TCP `80`）：
  - 看板：`http://1.15.146.228/`
  - 健康检查：`http://1.15.146.228/health`
  - 公告 API：`http://1.15.146.228/api/notices`
  - 涨停 API：`http://1.15.146.228/api/limit-up`
- **免开公网端口（通过 SSH 随时拉取）**：
  ```bash
  # 直接拉取 JSON
  ssh tengxun_kanglei 'curl -s http://127.0.0.1/api/notices'
  ssh tengxun_kanglei 'curl -s http://127.0.0.1/api/limit-up'

  # 本地端口转发，浏览器访问 http://127.0.0.1:18900/
  ssh -L 18900:127.0.0.1:18900 -N tengxun_kanglei
  ```

## 4. 一键编译与部署

在 `tdx-companion` 根目录执行：

```bash
make hub-deploy
```

或手动执行：

```bash
cd tools/tdx-hub
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o tdx-hub-linux-amd64 .
scp tdx-hub-linux-amd64 tengxun_kanglei:/tmp/tdx-hub
rm -f tdx-hub-linux-amd64
ssh tengxun_kanglei 'sudo mv /tmp/tdx-hub /opt/tdx-hub/tdx-hub && sudo chmod +x /opt/tdx-hub/tdx-hub && sudo systemctl restart tdx-hub.service && sudo systemctl is-active tdx-hub.service'
```

## 5. 常用运维命令

```bash
# 查看运行状态与实时日志
ssh tengxun_kanglei 'sudo systemctl status tdx-hub.service --no-pager'
ssh tengxun_kanglei 'sudo journalctl -u tdx-hub.service -n 50 --no-pager'

# 重启服务
ssh tengxun_kanglei 'sudo systemctl restart tdx-hub.service'
```

## 6. 扩展新模块约定

1. 在 `tools/tdx-hub/` 下新增独立 `.go` 文件（如 `lhb.go`、`monitor.go`）实现采集与数据结构。
2. 在 `main.go` 中注册后台刷新协程与 `mux.HandleFunc("/api/<module>", ...)` 路由，并把路径写入 `/health` 的 `endpoints`。
3. 如需页面展示，在 `index.html` 中新增对应 Tab，执行 `make hub-deploy` 即可上线。
