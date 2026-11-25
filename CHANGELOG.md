# 更新日志

所有版本都是持续集成 — 暂没有 LTS 分支，也没有向后兼容承诺。

## v0.3.0 — 2026-10-16

新增通用计划任务（Scheduled Tasks）系统，支持定时重启 Mesh 子路由等局域网运维场景。

变更：

- **通用计划任务系统**：
  - 新增 `internal/tasks` 模块，纯标准库实现标准 5 位 Cron 语法匹配器（分、时、日、月、周，支持 `*`, `/`, `,`, `-`）
  - 双模执行引擎：支持 Shell 命令（`/bin/sh -c`）与 HTTP 请求（GET/POST/PUT/DELETE，支持自定义 Header 与 Body）
  - 安全防护：单任务执行互斥锁（防止阻塞重叠）、默认 60 秒（可配置）超时保护、最大 64KB 输出截断保护路由器内存
  - 执行历史：内存 50 条环形缓冲区（Ring Buffer）保留运行记录与输出
  - 飞书通知集成：支持每任务自选通知策略（`on_failure` 仅失败时告警 / `always` 总是通知 / `never` 不通知）
  - Web UI 集成：主页新增「计划任务」卡片、新建/编辑弹窗（带每周/每天/每小时 Cron 快捷填充）、实时「立即执行」测试按钮、深色终端风格执行日志查看弹窗
- 版本号 0.2.3 → 0.3.0

## v0.2.3 — 2026-05-10

修复 SOCKS5 / TCP 转发长连接 5 分钟硬性断连 bug。

变更：

- **修复 SOCKS5/TCP 长连接 5 分钟必断（最重要）**：`socks5/server.go` 与 `forwarder/tcp.go` 进入双向流转发时，把 `idleTimeout` 当作绝对截止时间一次性写到两端 `SetDeadline`，而 `io.Copy` 路径上的 `metrics.CountedWriter` 仅更新 `LastActive` 指标，从未在数据流动时刷新 deadline。结果：任何穿过代理的 TCP 连接（SSH、长 HTTP/SSE、内网 RDP 等）都会在建立后整 5 分钟被强制 timeout，与有无活动无关。表现为 SSH 等长会话"时不时断"
- 修复方式：`metrics.CountedWriter` 增加可选 `OnActivity` 钩子，SOCKS5 与 TCP 转发各自注入"节流刷新双端 deadline"回调（每 30 秒最多一次 `SetDeadline` 系统调用，避免高吞吐场景的 syscall 风暴），真正实现"5 分钟无活动才关"的语义；最坏情况下"真正空闲到关闭"延后约 30 秒
- 版本号 0.2.2 → 0.2.3

## v0.2.2 — 2026-05-04

DDNS 滞后误报修复 + 版本号自动派生。

变更：

- **DDNS 滞后误报修复（最重要）**：`SyncOnce` 在 IP 未变化的快速路径里直接 `return nil`，不更新 `LastSyncedAt`，导致 IP 长期不变时 UI 看到的"上次同步时间"被冻结，误以为同步循环挂了。修复：no-op 命中也调用 `recordOK` 打卡（零 API 调用、不发飞书通知），仅刷新本地时间戳
- UI 区分"DDNS 错误"（`last_error` 非空，需检查凭据/网络）vs "DDNS 滞后"（守护进程 DDNS goroutine 没在跑），两者颜色虽都偏红，但顶栏胶囊文字与 tooltip 不同；滞后中等程度（2×–4× 间隔）显示黄色 ◔ 图标
- 版本号自动从 `git describe --tags --always --dirty --match 'v[0-9]*'` 派生：
  - 在 tag 上：`v0.2.2` → 显示 `0.2.2`
  - tag 后 N 个 commit：`0.2.2-3-gabc1234`
  - 工作树有未提交变更：自动加 `-dirty` 后缀
  - 找不到 v 前缀 tag：fallback 到 `internal/version` 包内默认值（避免把裸 SHA 当版本号显示）
- `internal/version.Number` 由 const 改为 var，支持 `-ldflags -X` 注入；`Full()` 智能识别 `git describe` 已含 `-g<sha>` 时不再额外追加 `(commit)` 后缀

## v0.2.1 — 2026-05-04

UI 视觉打磨 + 通知时区修复;无 schema / API 变更。

变更：

- 顶栏新增中央状态胶囊（绿/黄/红 traffic-light + WAN IPv6 简写），鼠标悬停显示 `运行 Xm · WAN IPv6: ...` 等详情
- 概览改为桌面 2 × 2 等高网格，每列 ~520 px，IPv6 不再硬断；窄屏 (<800 px) 自动降为单列
- 守护进程组拆主 / 次两层（虚线分隔），构建 commit / PID / 配置 schema 用更小字号 + 更浅颜色，调试细节不再抢眼
- DDNS 同步行紧凑化为 `✓ <IP> · 12 秒前`，绝对时间挂在 title 上
- 服务 / 流量组合并入 / 出站为单行 `↓ 213 KB · ↑ 1.08 MB`
- 飞书通知时间改用 `Asia/Shanghai`（通过 `_ "time/tzdata"` 自带 IANA 库，无需路由器装 tzdata），同时附带 `+0800` 后缀消除歧义；可通过 `-ldflags -X wan6gate/internal/notify.DisplayTZName=...` 覆盖
- `system.CollectRouterInfo` 去除 OpenWrt `DISTRIB_DESCRIPTION` 末尾常见的 ` unknown` 占位
- 概览 `section-head` 新增 totals 行 `路由器 X · 守护进程 Y · ↓N ↑N`，首屏不需扫所有组就能看到核心数字
- 自动刷新指示：右上角脉冲圆点（5 秒周期）+ `5s` 文字；in-flight 时变蓝表示正在请求
- `main` 顶部错误横幅：DDNS 报错时弹出红色横幅显示完整错误，可关闭，关闭状态在 sessionStorage 记忆，新错误（不同 message）会重新弹出
- DDNS 健康度阈值：滞后 > 2× `interval_seconds` → 黄、> 4× → 红；DDNS 同步行 + 顶栏状态 pill 同时联动变色（之前只在错误时才变色）
- `/api/overview` 的 `ddns` 段加 `interval_seconds`，给前端做健康判定用
- 流量 `↓ / ↑` 字号提升 + 上下箭头着色（蓝/绿）；启动时间改为相对时间 `4 分钟前`，绝对时间挂在 `title`
- 480px 以下进一步紧凑：隐藏自动刷新指示，缩小 totals 行字号

## v0.2.0 — 2026-05-04

UI 与可观测性增强；配置 schema v4 → v5。

变更：

- UI 主页新增「概览」区块，统一展示 WAN IPv6、守护进程版本/构建/PID/启动时间、路由器主机名/内核/系统已运行/负载/内存、端口映射 + SOCKS5 计数、累计入/出流量、DDNS 与通知状态；顶栏精简为标题 + 用户操作
- 新接口 `GET /api/overview` 聚合 daemon / router / network / services / traffic / ddns / notify
- 新增 `internal/version` 包，`scripts/build.sh` 通过 `-ldflags -X` 注入 git short SHA + 构建时间；`wan6gate version` 输出形如 `wan6gate 0.2.0 (a1b2c3d4e5)`
- 新增 `internal/system.CollectRouterInfo()`，从 `/etc/openwrt_release`、`/proc/{uptime,loadavg,meminfo,sys/kernel/...}` 读取路由器基础信息；非 Linux 主机优雅降级
- 飞书通知新增 SOCKS5 暴力破解封禁事件（15 分钟聚合：首次封禁后启动定时器，期间累积 IP/次数，到时一次性发出汇总）；`NotifyConfig.NotifyOnSocks5Ban` 默认开启
- `banlist` 增加 `SetOnBan(func(ip string))` 钩子（异步调用，不阻塞 SOCKS5 auth 热路径）
- 取消 UI 后端对 `webhook_url` 与 DDNS `access_key_secret` 的脱敏返回；前端表单回填明文，`type=text` 直接显示（仅 LAN UI + 已认证）
- 配置 schema v4 → v5：自动迁移，老配置加载时把 `notify_on_socks5_ban` 默认置 true；`config.json.v3.bak` 备份机制不变
- 文档（README / CHANGELOG / architecture / 注释）去除个性化字符串（具体路由器型号、域名、IP、端口示例），改为通用占位

## v0.1.0 — 2026-05-02

初始版本。在运行 MiWiFi 固件（基于 OpenWrt-18.06）的 aarch64 小米路由器上端到端构建和验证。

功能：

- TCP 和 UDP 端口转发器，`[路由器-ipv6]:N` → 固定局域网目标
- 认证 SOCKS5 代理（RFC 1928 + 1929；仅支持 CONNECT）
- 阿里云 DDNS 自动同步 WAN GUA，支持重试 + 错误恢复
- 飞书 Webhook 通知：守护进程启动、DDNS IP 变更、DDNS 错误（15 分钟防抖）
- 内存中的指标 — accepted、errors、bytes_in、bytes_out、active — 按服务分类，在 `/api/metrics` 和 UI 内联展示
- 每小时持久化历史（按月份的 JSONL，保留 90 天），UI 中使用纯 JS SVG 折线图
- SOCKS5 暴力破解防护：每个 IP 5 分钟内 10 次认证失败 → 封禁 30 分钟；UI 显示封禁列表并支持手动解封
- 一键导出客户端配置 — sing-box JSON、Clash YAML、Surge `[Proxy]` 行和通用 `socks5h://` URI
- 仅局域网 Web UI（默认 `0.0.0.0:18080`），单页，手写 HTML/CSS/JS 嵌入二进制文件
- bcrypt 哈希的 UI 管理员密码；Cookie 会话
- 持久化：
  - 二进制文件、配置、日志、指标 → `/data/router-ops/wan6gate/`
  - 防火墙规则 → uci `firewall.wan6gate_*` 段落（`/etc/config/firewall`，ext4）
  - 看门狗 → `/etc/crontabs/patches/wan6gate_watchdog.sh` + cron 行（busybox crond 不支持 `@reboot`）
  - 纵深防御 fw3 DROP 规则用于 WAN→UI 端口
- v3 → v4 配置迁移（v0.1.0 首次启动时）— 旧的带有 `type:socks5` 的 `mappings[]` 拆分为新的 `socks5_servers[]` 数组；原始配置保留为 `config.json.v3.bak`（一次性备份）

不在范围内（决策，非遗漏）：

- UI 的 HTTPS（设计上仅限 LAN）
- 多用户 SOCKS5（请打开另一个监听器）
- IPv4 DDNS（CGNAT — 无公网 v4）
- SOCKS5 UDP ASSOCIATE（TCP CONNECT 覆盖所述用例）
- 守护进程重启后 in-flight 指标或封禁列表的持久化