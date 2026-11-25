# UDP 转发优化与路由器部署运维记录（2026-11-25）

本文档记录 `wan6gate` UDP 端口转发器统计接入、并发竞态修复以及两台小米 OpenWrt 路由器的硬件基线与安全升级规范。

---

## 一、UDP 转发器优化总结

### 1. 优化前的问题
1. **指标统计完全缺失**：[udp.go](../internal/forwarder/udp.go) 虽在 `Manager` 中接收了服务 `id`，但未接入 `internal/metrics`，导致 Web 面板「端口映射」中 UDP 规则的 `活跃 / 累计` 与 `入站 / 出站` 长期显示为 `0 / 0`、`0 B / 0 B`，顶部概览与流量历史图亦无 UDP 数据。
2. **空闲清理与并发竞态**：
   - 后端回包读协程在 `udpFlowIdle`（60s）超时后退出，未立即同步清理 `flows` 映射，在 `janitor`（30s 周期）扫描前会留下最多 30s 的“僵尸 flow”窗口。
   - 若客户端持续单向发包而后端超过 60s 无回包，读协程超时会误关活跃会话。
   - 空闲判定使用墙钟 `time.Time`，受路由器 NTP 校时影响。
3. **可观测性不足**：UDP 会话建立、keepalive 续期与超时回收缺少 `slog` 日志。

### 2. 本次核心改动
- **源码**：[internal/forwarder/udp.go](../internal/forwarder/udp.go)
  - **接入 `metrics`**：新建会话调用 `metrics.Begin(f.id)`；入站（`client -> backend`）累加 `BytesIn`，出站（`backend -> client`）累加 `BytesOut`；使用 `time.Now().UnixNano()` 更新 `counters.LastActive`；`sync.Once` 保障 `closeFlow` 只执行一次 `end(err)`，`Active` 永不泄漏或为负。
  - **单入口错误与合法目标校验**：校验 `net.ParseIP(f.targetIP) != nil`；停机取消（`ctx.Err() != nil`）或流已关闭导致的连接错误不再误计入 `Errors`；回包写失败统一经 `closeFlow(fl, werr)` 单入口计一次错误。
  - **统一锁保护与单调空闲判定**：以 `monoBase` + `time.Since` 实现单调纳秒空闲计算；客户端复用、后端读包续期与 `closeFlowIfIdle` 摘除共用 `f.mu` 锁串行校验，彻底消除误关活跃单向流与并发摘除竞态；`Run()` 使用子 `runCtx` 统一收口退出与 `WaitGroup` 等待。
- **回归测试**：[internal/forwarder/udp_test.go](../internal/forwarder/udp_test.go)
  - `TestUDPEchoAndMetrics`：双向转发、`Accepted` / `Active` / `BytesIn` / `BytesOut` 验证、空闲回收归零与重连新建流。
  - `TestUDPUnidirectionalKeepalive`：持续单向发包超过 2 倍空闲阈值期间后端保持静默，之后再回包验证原流未被误关闭。
  - `TestUDPMetricsNoLeakOnCancel`：`cancel()` 退出时 `Active == 0`、`Errors == 0`、`flows` 清空且 `Run()` 及时返回 `context.Canceled`。
  - `TestUDPConcurrentFlowsAndReadError`：3 路并发客户端精准字节/会话统计，以及无效目标 IP 拨号失败精确 `Errors == 1` 且无 `Active` 泄漏。

### 3. 本地验证记录
- `gofmt -l internal/forwarder/`：无格式差异
- `go vet ./...`：通过
- `go test -race -count=2 -v ./internal/forwarder`：4 项 × 2 轮全部 `PASS`
- `go test -race -count=1 ./...`：全仓库测试通过
- `./scripts/build.sh`：成功交叉编译 Linux `arm64` 静态二进制 `build/wan6gate`

---

## 二、路由器环境基线与差异说明

两台路由器均为小米 OpenWrt `aarch64`，均已升级至 **`wan6gate 0.2.3-14-g307db06-dirty`**。

| 项目 | 主路由（`192.168.31.1` / `router.example.com`） | 当前局域网路由（`192.168.32.1`） |
| :--- | :--- | :--- |
| **系统 / 内核** | OpenWrt `5.4.164` (`aarch64`) | OpenWrt `4.4.60` (`aarch64`) |
| **闪存 `/data` 容量** | 总 `20.6M`（已用 `11.5M`，剩余 `8.0M`） | 总 `42.7M`（已用 `21.4M`，剩余 `19.0M`） |
| **外置存储** | `/dev/sda1` 挂载 `/mnt/usb-disk`（大容量外置盘） | 无 |
| **UI `bind_addr`** | `192.168.31.1:18080`（仅监听 LAN IP） | `0.0.0.0:18080`（全网卡监听） |
| **WAN 端口保护** | 仅监听 LAN IP，WAN 口直连被阻断 | `fw3` 安装 `wan6gate_ui_lockdown_18080` (`DROP` WAN) |
| **主要业务转发** | 示例 TCP/UDP 转发（远程桌面类应用的多端口映射）、`<socks5-port>` SOCKS5 | 示例 TCP 转发、`<socks5-port>` SOCKS5 |

### 关于 `bind_addr` 差异与 `/data` 空间的说明
1. **`bind_addr` 差异无安全或功能隐患**：主路由源自早期配置绑定在 LAN IP `192.168.31.1:18080`；另一台绑定 `0.0.0.0:18080` 并由 `wan6gate install` 自动写入 `fw3` 的 `wan6gate_ui_lockdown_18080` 规则阻断 WAN 口直接访问 `18080`。两边管理界面均应仅通过内网，或经由加密隧道（SSH 本地端口转发 / 认证 SOCKS5 代理）接入后访问，切勿将 UI 管理端口直接暴露到公网。
2. **主路由 `/data` 空间管理**：
   - 主路由出厂 `ubi1:cfg`（`/data`）分区仅 `20.6M`：系统基础目录占约 `8.7M`（`/data/sec_cfg` `4.0M`、`/data/usr` `3.9M`、`/data/etc` `783K`），`wan6gate` 二进制 `7.8M`、轮转日志 `4.3M`（`rotator` 封顶 `2MB × 3`）、历史指标 `2.4M`（`historystore` 默认保留 `90` 天），稳态剩余约 `8.0M`，日常不会无限增长。
   - 若未来需要进一步释放 `/data` 空间，可将高频写入的 `logs/` 或 `metrics/` 迁至已挂载的 USB 盘 `/mnt/usb-disk` 并做软链接（二进制与 `config.json` 仍建议保留在板载 `/data`），或调小 `rotator` 轮转上限。

---

## 三、远程跨代理安全升级经验（重要）

当运维端通过目标路由器自身提供的 `<socks5-port>` SOCKS5 代理 SSH 接入该路由器（如从外网维护 `192.168.31.1`）时，直接执行 `deploy.sh` 中的 `pkill -f 'wan6gate daemon'` 会导致代理通道立即断开、后续 `install` 命令中断；同时主路由 `/data` 剩余空间（`~8M`）不足以同时容纳新旧两个 `7.8M` 二进制。

**推荐标准安全升级流程**：
1. 先将新二进制与独立切换脚本 `scp` 上传至内存盘 `/tmp/`（`tmpfs` 约 `380M` 空闲，且不中断现有服务）。
2. 在路由器本地备份旧二进制到 `/tmp/wan6gate.bak`，随后以 **路由器本机后台脱壳脚本** 执行原子切换（不依赖外部 SSH 会话存活）：
   - 停止旧 `wan6gate` 进程；
   - `rm -f /data/router-ops/bin/wan6gate && mv /tmp/wan6gate.new /data/router-ops/bin/wan6gate`（先删后移，避免 UBIFS 空间不足）；
   - 执行 `/data/router-ops/bin/wan6gate install` 与 `/etc/crontabs/patches/wan6gate_watchdog.sh` 立即拉起新进程；
   - 自检进程存活失败则自动从 `/tmp/wan6gate.bak` 回滚恢复。
3. 几秒后重连路由器检查升级日志、版本与进程状态，并清理 `/tmp/` 下的临时备份与脚本。
