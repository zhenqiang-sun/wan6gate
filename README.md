# wan6gate

一个小型自托管网关，通过路由器的公网 IPv6 地址暴露家庭服务。专为那些 IPv4 被困在运营商级 NAT（CGNAT）但 ISP 分配了真实公网 IPv6 前缀的家庭设计。

这是一个单一的 Go 二进制文件，运行在路由器上，提供以下功能：

- **TCP/UDP 端口转发** — `[路由器-ipv6]:N` → `<局域网-ip>:M`
- **认证 SOCKS5 代理**（RFC 1928 + 1929）— 将你的路由器作为干净的出口节点
- **IPv6 DDNS 到阿里云** — 你的域名跟随 PPPoE 拨号后分配的 WAN GUA 变化
- **飞书 Webhook 通知** — 守护进程启动、DDNS 变更、DDNS 错误
- **通用计划任务**（v0.3.0 新增）— Cron 定时执行 Shell 命令或 HTTP 请求，失败可告警到飞书
- **内存中的指标 + 每小时持久化历史** — 最近 90 天，支持简单的 SVG 图表
- **SOCKS5 暴力破解防护** — 5 分钟内 10 次失败 → IP 被封禁 30 分钟
- **单页仅局域网 Web UI** — 无前端框架，设计上不启用 HTTPS
- **一键导出客户端配置** — 支持 sing-box / Clash / Surge / 通用 URI

整个项目是为一类特定部署场景编写的：运行 MiWiFi 固件（基于 OpenWrt-18.06）的 aarch64 小米路由器。持久化依赖 uci + fw3 + busybox cron 模式，这些在 OpenWrt 分支中常见，但不能保证在其他固件上相同。移植时请谨慎；请参阅 [docs/architecture.md](docs/architecture.md) 了解需要替换的部分。

## 前置要求

wan6gate 需要通过 SSH 部署到路由器，因此必须先使用 **XMiR-Patcher** 解锁路由器 SSH 权限：

> **XMiR-Patcher** — 小米路由器固件补丁工具
> 仓库：https://github.com/openwrt-xiaomi/xmir-patcher

使用步骤：
1. 下载 XMiR-Patcher
2. 运行 `run.bat`（Windows）或 `run.sh`（Linux/macOS）
3. 选择对应路由器型号，开启 SSH 访问

**重要**：开启 SSH 后才能进行后续的构建和部署操作。

## 快速开始

```bash
# 1. 交叉编译 aarch64 Linux 二进制文件
./scripts/build.sh                 # → build/wan6gate（约 7 MB，静态链接）

# 2. 推送并安装到路由器（需要环境变量）
export ROUTER_HOST=<路由器局域网 IP>          # 例如 192.168.31.1
export ROUTER_PASS='your-router-root-password'
./scripts/deploy.sh

# 3. 打开 http://<路由器局域网 IP>:18080 设置管理员密码并添加映射规则
```

## 构建前提条件（构建主机）

- Go ≥ 1.22（开发环境使用 1.26）
- `expect` 用于密码模式 SSH 辅助脚本（macOS 自带；Linux：`apt install expect` 等）

无 CGO，无 `node_modules`，无前端工具链 — UI 是手写的 HTML / CSS / JS，通过 `//go:embed` 嵌入二进制文件。

## 目标平台前提条件（路由器）

- aarch64 / arm64 Linux（典型小米路由器型号如 BE7000、AX9000 均为 aarch64 ARMv8）
- Dropbear SSH（固件默认；构建辅助脚本处理仅 ssh-rsa 的主机密钥）
- BusyBox `crond` 读取 `/etc/crontabs/`
- `uci` + `fw3`（防火墙配置工具 — 用于持久化端口规则）
- 可写的 `/data/` 分区（二进制文件 + 配置 + 日志存放位置）
- BusyBox `flock`（看门狗使用）

二进制文件零外部运行时依赖；它调用 `/sbin/uci` 和 `/etc/init.d/firewall reload` 来执行防火墙更改。

## 构建

```bash
./scripts/build.sh
```

等同于：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" \
  -o build/wan6gate ./cmd/wan6gate
```

## 部署

```bash
export ROUTER_HOST=<路由器局域网 IP>        # 你的路由器局域网 IP，例如 192.168.31.1
export ROUTER_PASS='your-root-password'    # 切勿提交此文件
./scripts/deploy.sh                        # 构建 + scp + 安装
./scripts/deploy.sh --no-build             # 跳过重新构建
./scripts/deploy.sh --no-install           # 仅推送二进制文件
```

`deploy.sh` 幕后操作：

1. 交叉编译（除非指定 `--no-build`）。
2. 通过 `scripts/_ssh-helper.exp` 封装脚本将二进制文件 `scp` 到 `/data/router-ops/bin/wan6gate`（解决 dropbear 算法集过旧的问题）。
3. 停止任何正在运行的 `wan6gate daemon`。
4. 在路由器上运行 `wan6gate install`，设置：
   - `/data/router-ops/{bin,wan6gate/logs}` 目录
   - `/etc/crontabs/patches/wan6gate_watchdog.sh`（在任何崩溃或启动后 ≤ 60 秒内重新启动守护进程）
   - 看门狗每分钟执行的 cron 行
   - 一个 fw3 `wan6gate_ui_lockdown_18080` 规则，显式 DROP 到达 UI 端口的 WAN 流量
5. 重启 cron。

## 路由器上的运行时布局

```
/data/router-ops/
├── bin/wan6gate                       二进制文件（ubifs，持久化）
└── wan6gate/
    ├── config.json                    模式 0600；映射 + socks5 + ddns + 通知配置
    ├── config.json.v3.bak             自动迁移备份，首次启动时保留
    ├── wan6gate.lock                  单例 flock 文件
    ├── wan6gate.pid                   当前守护进程 PID
    ├── logs/wan6gate.log              大小轮转 1 MB × 3
    └── metrics/YYYY-MM.jsonl          每小时快照，保留 90 天

/etc/crontabs/patches/wan6gate_watchdog.sh
/etc/config/firewall                   uci 段落 wan6gate_<端口>_<协议> + wan6gate_ui_lockdown_*
```

`/data` 是 ubifs（重启后持久化）。`/etc` 是 ramfs，但 `/etc/config/` 和 `/etc/crontabs/` 通过本固件绑定挂载到持久化存储，这就是设计能够在不触碰 squashfs rootfs 的情况下工作的原因。

## 子命令

```
wan6gate daemon                 前台运行；由 cron 看门狗调用
wan6gate install                幂等设置
wan6gate uninstall              清理移除（默认保留配置）
wan6gate uninstall --purge      同时删除配置和二进制文件
wan6gate version
```

`daemon` 接受 `--config <路径>` 和 `--bind 主机:端口` 参数覆盖，用于在非路由器主机上进行本地测试。

## 本地测试（无路由器）

```bash
go build -o build/wan6gate-mac ./cmd/wan6gate
WAN6GATE_DIR=/tmp/wan6gate-test \
  ./build/wan6gate-mac daemon --bind 127.0.0.1:18080
```

打开 `http://127.0.0.1:18080`。当缺少这些二进制文件时，防火墙调用（`uci`、`firewall reload`）会警告并继续，因此 UI / 转发器 / SOCKS5 仍然可以端到端工作（针对模拟的局域网目标）。

## 依赖

```
golang.org/x/crypto/bcrypt    UI 管理员密码哈希
```

其他一切（HTTP 服务器、JSON、阿里云 DNS RPC 签名、SOCKS5 协议、原子指标、SVG 图表渲染）都是标准库 + 手写的。

## 不在范围内的事项

- UI 的 HTTPS（设计上仅限 LAN）
- 多用户 SOCKS5（每个监听器一个凭据；如有需要可打开更多监听器）
- IPv4 DDNS（假设是 CGNAT IPv4）
- SOCKS5 UDP ASSOCIATE（TCP CONNECT 覆盖所述用例）
- 不支持没有 uci+fw3+busybox-cron 的路由器（必须移植）

## 安全建议

- **切勿将 Web UI 端口（默认 `18080`）直接映射到公网。** 该端口设计上只提供 HTTP 明文访问，没有 HTTPS、没有额外认证层，一旦暴露即等同于把路由器管理面开放给整个互联网。`wan6gate install` 会写入 `fw3` 的 `wan6gate_ui_lockdown_18080` 规则丢弃 WAN 口发往该端口的流量，请保持该规则生效。
- **远程管理请走加密通道。** 推荐从内网直连；确需外出时，通过 SSH 本地端口转发（`ssh -L 18080:<路由器 LAN IP>:18080 root@<路由器>`）或先接入本项目提供的认证 SOCKS5 代理、再由其访问局域网内的 Web UI。
- **不要把管理凭据与 SOCKS5 密码写进仓库。** 部署脚本通过 `ROUTER_HOST` / `ROUTER_PASS` 环境变量读取密码，`.gitignore` 已排除 `.env`、`*.pem`、`*.key` 等本地密钥文件。
- **SOCKS5 监听器同样应视为公网暴露面。** 本项目已内置暴力破解防护（5 分钟内 10 次失败 → 封禁 30 分钟），但仍建议配合防火墙限制来源地址，并使用高强度随机密码。
- **升级前先备份 `/data`**。路由器 `/data` 分区通常只有几十 MB，新旧二进制同时存在可能写满；`scripts/deploy.sh` 采用先删后移的原子切换以规避该问题。

## 常见陷阱

- 小米路由器固件上的 Dropbear 通常仅宣传 `ssh-rsa` 主机密钥；现代 OpenSSH 默认不连接。辅助脚本传递 `HostKeyAlgorithms=+ssh-rsa` 等参数。
- BusyBox `crond` 不支持 `@reboot`；我们使用 `*/1 * * * *` 加上 flock 保护的看门狗。不要尝试使用 `@reboot`。
- `/etc/init.d/dropbear` 每次启动时从 squashfs 重新创建；小米内置的 `ssh_patch.sh` 每次启动时重启 dropbear，这会拾取我们的 `/etc/config/dropbear` `Interface=lan` 设置（仅局域网监听）。
- LAN 网段更改后，新安装默认使用 `0.0.0.0:18080` 作为 UI 绑定地址；现有配置保留其原来的 IP。如需精确 IP 可通过 UI 编辑。

## 状态

已在单个家庭生产环境测试。跨重启、PPPoE 重新拨号和 LAN 网段重新编号均稳定运行。详见 [CHANGELOG.md](CHANGELOG.md) 了解各版本范围；[docs/architecture.md](docs/architecture.md) 了解当前设计。

## 开源边界与免责声明

- 本项目以 **MIT 协议按现状提供**（as-is），不含任何担保；作者不对使用本项目造成的设备损坏、数据丢失或安全后果承担责任。
- 本项目**仅在特定小米（MiWiFi / 基于 OpenWrt-18.06 的 `aarch64`）固件上验证过**。其他品牌路由器的固件在 `uci` / `fw3` / BusyBox `crond` 的行为与路径上差异很大。
- **不承诺全品牌路由器兼容性，也不提供刷机（刷写固件）支持。** 本项目不包含、也不会引导你执行任何固件刷写操作；因自行刷机导致的变砖风险完全由使用者自行承担。
- 移植到其他固件时需要自行替换持久化层（`uci` + `fw3` + cron 看门狗），具体接口参见 [docs/architecture.md](docs/architecture.md)。

## 许可证

MIT — 详见 [LICENSE](LICENSE)。
