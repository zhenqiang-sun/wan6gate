# wan6gate 架构

运行在路由器上的单一 Go 二进制文件；一个进程拥有多个长生命周期 goroutine 和一个小型 Web UI。

## 为什么存在这个项目

中国家庭宽带的 IPv4 越来越多人被困在 CGNAT（路由器从 PPPoE 获取的是 `10.x` 地址而不是公网地址），但**确实**分配了公网 IPv6 前缀。这使得 IPv6 成为向外界暴露家庭服务的唯一可行途径，但也带来了一些新问题，wan6gate 端到端解决了这些问题：

1. **某些客户端仅支持 IPv4** — 通过暴露域名而不是原始 `[2409:...]` URL 解决，并将 AAAA 记录指向它。
2. **PPPoE 重新拨号后 IPv6 前缀会变化** — 通过集成的 DDNS 模块在 5 分钟内将新的 GUA 推送到阿里云 DNS 解决。
3. **某些应用程序完全无法使用 IPv6** — 通过 SOCKS5 监听器解决，局域网侧 IPv4 流量可以通过它到达公网 IPv6 互联网（和域名）。
4. **路由器管理界面不应泄漏到 WAN** — 通过将 Web UI 绑定到仅局域网地址，并添加显式的 fw3 DROP 规则来解决。
5. **在稳定端口上公开 SOCKS5 会被扫描** — 通过一个小型内存限速器解决，在 5 分钟内 10 次认证失败后封禁 IP 30 分钟。
6. **你需要知道发生了什么变化** — 通过飞书 Webhook 通知解决，用于守护进程启动、DDNS IP 变更和 DDNS 同步错误。

## 进程解剖（单一二进制，进程）

```
wan6gate daemon
├─ goroutine: HTTP UI 服务器（仅 LAN）
├─ goroutine: 每个映射的 TCP 转发器      (forwarder.Manager)
├─ goroutine: 每个映射的 UDP 转发器      (forwarder.Manager)
├─ goroutine: 每个服务器的 SOCKS5 监听器  (socks5.Manager)
├─ goroutine: DDNS 同步循环（5 分钟周期）  (ddns.Manager)
├─ goroutine: 历史快照循环（每小时）      (historystore.Store)
├─ goroutine: 封禁列表 GC（5 分钟周期）    (banlist.BanList)
├─ goroutine: 日志写入器（大小轮转）
└─ goroutine: 通知调度器                  (notify.Notifier)
```

所有 goroutine 共享一个父 `context.Context`，在 `SIGINT/SIGTERM` 时取消。重启快速（≈ 2 秒新守护进程启动；cron 看门狗在 60 秒内捕获死亡进程）。

## 包布局

```
internal/
├── config/         JSON 配置 + 原子保存 + 版本迁移 v1→v4
├── system/         net.Interface 辅助函数（读取 pppoe-wan IPv6，扫描端口）
├── firewall/       uci 封装（AddRule、DeleteRule、AddUILockdownRule）
├── forwarder/      TCP / UDP 原始端口转发器 + 管理器
├── socks5/         RFC 1928 + 1929 服务器（仅 TCP）+ 管理器
├── ddns/           阿里云 DNS RPC v1 客户端（HMAC-SHA1）+ 同步循环
├── notify/         飞书 Webhook + 每事件开关 + 防抖
├── banlist/        每 IP 失败计数 + 30 分钟封禁 + GC
├── metrics/        原子计数器 + io.Copy 包装 + 注册表
├── historystore/   每小时 JSONL 快照 + 每月文件轮转
├── clientconfig/   sing-box / Clash / Surge / URI 生成器
├── persistence/    安装/卸载（cron 行 + 看门狗 + fw3 锁定）
└── ui/             HTTP 处理器 + 嵌入的 HTML/JS/CSS + SSE 日志推送
```

## 持久化模型（花费最多心思的部分）

此固件上三个存储层行为不同：

| 路径 | 类型 | 重启后存活？ | 用途 |
|---|---|---|---|
| `/etc/config/` | ext4（`/dev/mapper/sec_cfg`） | 是 | uci 配置（防火墙、dropbear、dhcp） |
| `/etc/crontabs/` | ubifs（`ubi1:cfg` 绑定挂载） | 是 | cron 任务行 + 补丁脚本 |
| `/etc/init.d/` `/etc/rc.d/` | ramfs | **否** | 每次启动时从 squashfs 重新创建 |
| `/data/` | ubifs | 是 | 二进制文件、config.json、日志、指标历史 |
| `/userdisk/` | ubifs（与 /data 同一分区） | 是 | 目前未使用 |

这就是为什么我们**不**将 sysv 风格的初始化脚本放到 `/etc/init.d/`（它会在重启后消失）。相反，我们添加一行 cron 规则，每分钟调用一个补丁脚本；脚本对自身进行 flock，检查守护进程是否存活，如未存活则重新启动。此模式与小米已有的 `ssh_patch.sh` 模式完全相同，因此应该能通过固件更新继续工作。

## 启动恢复序列

```
T+0s    路由器启动，内核就绪
T+~10s  PPPoE 重新拨号，获得新的 IPv6 GUA
T+~30s  dropbear、dnsmasq、nginx 通过标准 rc.d 启动
T+≤60s  busybox cron 执行 `*/1 * * * *` 行：
        - ssh_patch.sh 启用/重启 dropbear（小米默认）
        - wan6gate_watchdog.sh 启动守护进程
        - wan6gate_secharden.sh 关闭 telnetd（手动 P4 安装）
T+≤62s  守护进程绑定 UI + 转发器 + SOCKS5
T+≤62s  守护进程启动时立即触发 DDNS → AAAA 记录在传播中
T+≤120s 阿里云返回成功 → DDNS 完成；飞书"守护进程已启动"已发送
```

重启后外部用户最坏情况体验：约 2 分钟域名解析才能跟上，由阿里云 TTL 决定（默认 600s）。

## 防火墙集成

我们不直接写入 `ip6tables -I` 规则 — 它们无法在 `/etc/init.d/firewall reload` 后存活。相反，我们：

1. 添加一个 uci 段落如 `firewall.wan6gate_<port>_tcp=rule`，包含 `family=ipv6 src=wan dest_port=<port> target=ACCEPT`。
2. `uci commit firewall && /etc/init.d/firewall reload`。
3. fw3 生成实际 `ip6tables` 规则到 `zone_wan_input`。

段落名称遵循 `wan6gate_<端口>_<协议>`，因此清理可通过 grep 完成。UI 锁定规则使用 `wan6gate_ui_lockdown_<端口>`（family=any，因此同时匹配 v4 和 v6）。

## 配置 schema（`config.json`，v4）

```jsonc
{
  "version": 4,
  "ui":   { "bind_addr": "0.0.0.0:18080", "session_secret": "..." },
  "auth": { "username": "admin", "password_bcrypt": "..." },
  "mappings":       [ /* TCP/UDP 转发：监听端口 → 目标 IP:目标端口 */ ],
  "socks5_servers": [ /* SOCKS5 监听器：监听端口 + 用户名 + 密码   */ ],
  "ddns":   { "enabled":..., "domain":..., "access_key_id":..., "access_key_secret":..., ... },
  "notify": { "enabled":..., "webhook_url":..., "notify_on_*":... }
}
```

模式 `0600`。映射和 socks5_servers 是独立的数组；v3 联合 schema（其中 `type:socks5` 位于 `mappings` 内）会自动迁移，并在新文件旁边留下 `.v3.bak`。

## 如果你想移植到不同路由器需要修改什么

- 如果你没有 fw3+uci（例如你将调用 `nft` 或 `iptables-save`），请替换 `internal/firewall/`。
- 如果你的启动系统不是 busybox cron（例如你要写一个 systemd unit 到可写的 `/etc`），请替换 `internal/persistence/`。
- 更新 `system.RouterIPv6()` 以从正确的接口名称读取（`pppoe-wan` 是特定于固件的）。
- 验证 `scripts/_ssh-helper.exp` 中的 dropbear / openssh 特性。

Go 逻辑本身是平台无关的。

## 不在范围内（主动放弃）

- IPv4 DDNS（假设 CGNAT）
- UI 的 HTTPS（仅 LAN）
- 多用户/基于角色的访问（单一管理员）
- 封禁列表在守护进程重启后持久化（新启动时状态丢失 — 攻击者必须再次失败才能被重新封禁）
- in-flight 指标持久化（只有每小时快照存活）
- SOCKS5 UDP ASSOCIATE（TCP CONNECT 覆盖所述用例）