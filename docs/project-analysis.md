# IP Guard 项目分析报告

分析时间：2026-11-16　｜　仓库：`cytusc/ip-list`　｜　版本：`0.2.4`（package.json）
近期提交：116 次，最后一次 `2026-11-11 08:41`（作者 lwt），工作区有 1 处未提交修改（`scripts/daily-maintenance.sh`）与 1 个未跟踪文件（`xray-oneclick.sh`）

---

## 1. 结论速览

1. **这是一套“IP 可用性工程”系统，不是普通的 CRUD 后台**：核心资产是“某个 IP 在某协议某配置下能不能用、多快、证书对不对、由谁证明”的证据链，前后端只是这套证据链的采集与展示层。
2. **数据可信度设计是这个项目最突出的优点**：`node_ip_evidence` → `current_node_ip_pool` → `shared_node_ip_pool` 三层视图把“原始扫描结果”和“当前可对外发布的结果”严格分离，任何一项证明（证书、完整代理检测、跨轮恢复观察、15 分钟新鲜度）失效，IP 自动跌出可用池。这类设计在同类“优选 IP”项目里少见。
3. **规模已经越过“个人脚本”阶段**：后端 79 个 Go 文件 / 14,907 行、前端 91 个 TS/TSX 文件 / 8,591 行、PostgreSQL 29 张表 + 5 个视图、119 个 Go 测试函数、6 个 Playwright 用例、完整 CI/CD 与线上部署脚本。
4. **主要技术风险不在功能，而在两处硬约束**：`main.go:273` 给所有 HTTP 请求套了 10 秒超时（前端 `src/lib/api.ts:30` 只给了 12 秒），而 `docs/database-performance.md:49` 记录 `/available-ips` 曾达 9,278 ms；以及单次全量扫描没有断点续跑能力——巡检有 `node_scan_schedules` 兜底会重开一轮，手动单次扫描遇进程重启直接中断。
5. **架构债集中在“单包 + 巨型组件”**：后端 31 个文件全是 `package main`，`dns.go`(21.5KB)、`scans.go`(20.5KB) 已是高复杂度核心；前端 `cf-manager.tsx`(33KB)、`dns-manager.tsx`(24KB)、`available-ip-list.tsx`(23KB) 需要拆分。
6. **线上实测（2026-11-16，见第 10 节）**：生产跑在 `192.3.110.240`（LAX，2C/1.9G，Debian 13，且与 xray/sing-box 等同机），部署的是最新提交 `cd16660`，服务连续 5 天以上零重启；但 187 个启用网段一天做 48 万次探测只换来 72 个可用 IP（0.015%），CF 队列从凌晨 02:00 积压到 21:00 仍有 123 条——当前瓶颈已经不在代码，而在“扫什么、扫多少”。

---

## 2. 项目定位与功能地图

**定位**：自建的代理节点 IP 优选 + 可用 IP 探测 + 华为云 DNS 自动发布 + 本机延迟探针的一体化管理平台。典型用途是给 `hk.reiiv.com`、`gb.reiiv.com` 这类按地区划分的二级域名自动挑选“当前最快且确实可用”的 IP。

| 导航模块 | 前端入口 | 后端核心文件 | 作用 |
| --- | --- | --- | --- |
| CF 优选 | `src/app/(console)/cf/*` | `backend/cf.go` (16.9KB) | 直连候选 IP，校验 HTTPS 2xx/3xx 与证书，产出 CF 共用池 |
| 连通性测试 | `src/app/(console)/probes` | `backend/scans.go` (20.6KB)、`probe.go`、`probe_xray.go` | 用 mihomo/Xray 把节点服务器地址替换为候选 IP，测真实代理连通与延迟 |
| 证书异常 IP | `certificate-issues` | `certificates.go`、`certificate_upgrade.go` | 入口 TLS 证书链/域名/有效期异常清单，异常 IP 不得进入可用池 |
| 可用 IP | `available-ips` | `available_ips.go` (12.6KB) | 跨协议汇总、按 ASN/服务商/归属地检索、生成分享链接、下载测速 |
| 域名解析 | `dns` | `dns.go` (21.5KB) + `dns_policy.go`、`dns_huawei.go`、`dns_speed.go` 等 10 个文件 | 按国家/地区把可用 IP 发布到华为云 DNS，含同步决策、通知、历史 |
| 延迟探针 | `latency-probes` | `latency_probes.go` (17.4KB)、`probe_retention.go` | 管理独立部署的 ICMP/TCP 探针，采集端到端延迟 |
| 节点管理 | `nodes` | `main.go` 通用 handler、`import.go`、`node_links.go` | VLESS/Trojan 链接导入（每次 1–20 条）、启停、分享链接生成 |
| 网段配置 | `networks` | `network_metadata.go`、`network_recheck.go`、`auto_scans` | IPv4/IPv6 CIDR 池，自动补全 ASN/服务商，改动即触发增量检测 |
| IP 黑名单 | `blacklist` | `blacklist.go` | 人工排除，优先级高于所有自动判定 |

九条链路共享同一套“网段池 → 候选地址 → 检测 → 证据 → 池 → DNS”的流水线，这是理解本项目的关键。

---

## 3. 技术栈与运行拓扑

```
                     ┌──────────────────────────────┐
  浏览器 ── HTTPS ──▶ │ Nginx（生产，外部 TLS）        │
                     └──────────────┬───────────────┘
                                    │ 127.0.0.1:3000
                     ┌──────────────▼───────────────┐
                     │ Next.js 16 (App Router, RSC) │  只有服务端能访问后端
                     │ Server Actions / route.ts 代理 │  Cookie HttpOnly 转发
                     └──────────────┬───────────────┘
                                    │ 127.0.0.1:8081（仅内网）
      ┌─────────────────────────────▼──────────────────────────────┐
      │ Go API（单个二进制，标准库 ServeMux，9 个后台 goroutine）      │
      │  ├ mihomo v1.19.33 内核（内嵌）/ 可选 Xray 子进程             │
      │  ├ 华为云 DNS SDK（AK/SK 只在服务端）                         │
      │  ├ IPPure 归属地、RIPEstat ASN、Feishu Webhook                │
      └───────┬───────────────────────────────────┬────────────────┘
              │                                   │
      ┌───────▼───────┐                   ┌───────▼───────┐
      │ PostgreSQL 17 │                   │  Redis 7      │
      │ 唯一事实源     │                   │ 会话/限流/进度 │
      └───────────────┘                   └───────────────┘
              ▲
              │ Bearer 密钥（SHA-256 校验 + AES-GCM 加密存原文）
      ┌───────┴────────────────────────────────────────────────────┐
      │ 本机延迟探针（6 平台预编译二进制：darwin/linux/windows × amd64/arm64）│
      │ macOS=LaunchAgent，Linux=systemd，Windows=计划任务             │
      └────────────────────────────────────────────────────────────┘
```

前端依赖（`package.json`）：`next ^16`、`react ^19.2`、`radix-ui`、`tailwindcss ^4`、`sonner`、`cmdk`。后端（`backend/go.mod`）：`pgx v5.7.6`、`go-redis v9.12`、`scs v2.9`、`ulule/limiter`、`validator/v10`、`mihomo v1.19.33`、`huaweicloud-sdk-go-v3`、`go4.org/netipx`。Go 1.26、Node 22+、PostgreSQL 17、Redis 7。

---

## 4. 架构分析

### 4.1 前端：读走 RSC，写走 Server Action，浏览器拿不到后端

- `src/lib/api.ts:19` 的 `api()` 是唯一出口：注入 `Cookie`、`Origin`、`cache: "no-store"`，并叠加 12 秒 `AbortSignal.timeout`。因为 `import "server-only"`，浏览器永远无法直接调用 Go API。
- 鉴权在服务端：`src/lib/auth.ts:15` 的 `requireUser()` 读 `ipfilter_session` 后请求 `/auth/me`，失败 `redirect("/login")`；`(console)/layout.tsx` 用 `dynamic = "force-dynamic"` 保证每个页面都鉴权。
- 写操作集中在 `src/app/actions.ts`（310+ 行）：`saveNode/saveNetwork/saveNetworks/...` 统一 `revalidatePath`，错误统一走 `failure()` 映射为 `{ok:false,error,fields}`，前端表单只处理结构化结果。
- `src/app/api/*/route.ts` 有 15 个薄代理（`available-ips`、`available-ips/speed-test`、`cf/scan`、`scan-records`、`probe-agent/*`、`probe-downloads/*` 等），用于给客户端组件轮询或下载；业务逻辑零下沉（已复核：15 个 `route.ts`）。
- 质量细节：`tsconfig.json` 开 `strict`，全仓 `: any`/`as any` 出现次数为 **0**；UI 全量 shadcn/Radix 风格自建组件（`src/components/ui/*`），无重型组件库。

### 4.2 后端：单二进制 + 标准库路由 + 9 个常驻循环

`backend/main.go:201` 的 `routes()` 用 Go 1.22 方法路由（`"GET /nodes"` 形式）注册约 50 个端点，`protected()` 包一层会话校验，最外层中间件统一加 `Cache-Control: no-store`、`nosniff`、**Origin 白名单**（`main.go:269`，仅 `/probe-agent/*` 豁免）与 10 秒请求超时。

启动即拉起 9 个后台循环（`main.go:123-131`）：连通数据清理、可用 IP 测速、探针采样压缩、历史证书补检、节点健康、巡检恢复、自动增量队列、网段元数据补全、DNS 定时同步。它们全部挂在 `signal.NotifyContext` 上，进程退出即收敛。

Handler 分布（`func (a *app)` 计数）：`dns.go 15`、`scans.go 14`、`latency_probes.go 10`、`scan_records.go 9`、`cf.go 8`、`main.go 7`，其余 25 个文件 1–5 个。**没有 service/repository 分层，业务逻辑与 SQL 直接同文件**——在 1.5 万行量级仍可维护，但已接近这个模式的舒适上限。

### 4.3 并发与互斥：三层保护，各自解决不同问题

| 机制 | 位置 | 解决什么 |
| --- | --- | --- |
| Redis 租约 + Lua 原子续租/取消检查 | `scans.go:44-47` `scanControlScript`，45 秒过期，10 秒心跳 | 跨进程/跨实例的单任务互斥，停止请求与续租合并为一次往返 |
| PostgreSQL advisory lock | `dns.go:18` `dnsLockID=74281934`，`pg_try_advisory_lock` | DNS 云端写入、策略编辑、队列请求串行；多 API 进程同样生效 |
| Go 信号量 | `scans.go:28` `xrayScanSlots`（64 容量） | 所有协议任务共享 Xray 进程内存上限，避免并发翻倍 |

状态机落在数据库而非内存：`node_scan_rounds` 每轮一行、`network_auto_scans` 队列表、`network_scan_backoff` 退避表、`node_scan_schedules` 巡检快照。**进程重启后能续跑**，这是与“只放内存/只放 Redis”方案的分水岭。

### 4.4 存储分工

- **PostgreSQL = 事实源**：29 表 + 5 视图，含全部检测证据、DNS 记录归属、探针采样与汇总。
- **Redis = 易失层**：SCS 会话（`ipfilter:session:`，24h）、登录限流（15 分钟 10 次）、扫描进度快照（24h）、ASN/归属地缓存（24h）、完成列表 `ipfilter:cf:done`（5 条）。
- 关键取舍：**扫描进度在 Redis，检测结果在 PG**。所以“关页面没事、重启服务要重扫”，这个边界在 README 中有明确交代，不是隐藏缺陷。

---

## 5. 关键链路

### 5.1 一次连通性检测

1. `POST /scans` 校验 `node_id`(uuid)、1–64 个网段、`interval_minutes` 0–1440、`full_every` 1–288（`scans.go:109`）。
2. 取 Redis 租约 → 展开网段为去重 IP 列表（单批上限 4096，`defaultNodeScanConcurrency=64`）。
3. 每个 IP 用 `parseShare()` 解析出的节点参数，**只替换服务器地址，保留端口/UUID/SNI/Host/传输参数**（`probe.go:20` 起，注释明确“never rewrite the whole URI”）。
4. mihomo 为每 IP 建独立实例做 URLTest；`PROBE_KERNEL=xray` 时改走每 IP 一个 Xray 子进程 + 私有 Unix socket。
5. 先做 3 次独立 TLS 握手严格校验（SNI/链/有效期），再发真实代理请求；结果带 `certificate` jsonb 写入 `node_ip_pool`。
6. 每轮写 `node_scan_rounds` 一行，进度/取消由 Lua 脚本每次检查；结束落最终结果，明细保留最近 30 轮 / 记录 7 天。

### 5.2 三种自动化，一个队列

- **增量**：新增/改 CIDR/重新启用网段 → 事务内写入 `network_auto_scans`，5 秒轮询消费，只测 PG 里没有结果的新地址。
- **全天候巡检**：首轮全量，日常只复测可用 IP，第 13、25… 轮回到全量；整段全失败才自动禁用该网段（手动恢复有独立入口）。
- **每日全量**：`scripts/daily-scan.py` + systemd timer，02:00 把全部启用网段入队，用统一 `recheck_after` 截止时间，重启补跑。
- **退避**：`network_scan_backoff` 记录“连续无产出”次数，间隔 1/3/5/7/15 天，避免垃圾网段每天烧 CPU。

### 5.3 证据链（本项目最值得抄的设计）

```
ip_results / node_ip_pool  ← 原始检测（保留失败样本，供续跑）
        │  证书 status ∈ {passed, not_required}，且无同 SNI 的 invalid
        ▼
node_ip_evidence           ← 配置仍存在（config_hash 匹配启用节点）
        │  gRPC IP 还必须：health=passed 且 <15min 且 samples_hash 匹配
        │                 且 ip_health_recovery.ready=true
        ▼
current_node_ip_pool       ← 当前真正可用
        │  按启用节点协议做笛卡尔投影
        ▼
shared_node_ip_pool        ← 列表 / 链接生成 / DNS 取值
```

配套的恢复观察是“连续 3 轮、每轮间隔 ≥5 分钟、覆盖 ≥10 分钟”，间隔超 15 分钟重新计数；确认故障的 IP 持续隔离，直到重新达标。**这套规则把“偶发丢包”和“真挂”区分开**，也是它能自动跑生产 DNS 而不频繁误删的原因。

### 5.4 DNS 发布决策

- 候选来源 = 可用池 ∩ 未拉黑 ∩ 探针采样健康；地区独立基准：亚太 `地区最快值 +10ms`、其他 `+20ms`，延迟按 P95 去峰后均值。
- 每次同步前重新核对云端记录，发现人工改动则跳过本轮（云端不支持条件更新）。
- 三种保守兜底：探针整体离线 → 保留原解析；归属地不完整 → 保留；本轮会删空全部记录 → 保留并告警。
- 变更通知走飞书卡片（🟢新增/🔴移除按域名与 A/AAAA 分组），失败只告警不回滚。

### 5.5 探针数据生命周期

原始采样保留 2 小时 → 每分钟分批压缩为 15 分钟桶（`latency_probe_rollups`，保留 24 小时）→ 汇总保留 `sample_count/success_count/delay_sum/min/max`，因此图表可加权统计，但**无法还原 P95 与抖动**（代码选择诚实标注而非编造）。压缩与删除在同一事务内完成，迟到上报追加到对应桶。

---

## 6. 工程质量评估

**做得好的地方**
- 测试密度：119 个 Go 测试函数覆盖协议迁移、并发、DNS 空计划、健康恢复、分页、保留策略；真实 PG/Redis 集成测试要求库名以 `_test` 结尾（防误连生产）。
- CI（`.github/workflows/cicd.yml`）：lint + typecheck + 前端 node 测试 + Python 单测 + `go test ./...` + `go vet` + 三平台构建 + 打包 + SSH 部署，串行 concurrency，部署后校验 `/health`、失败自动回滚链接。
- 迁移策略：`schema.sql` 全幂等（`IF NOT EXISTS` + `app_migrations` 标记），线上用 `-init` 执行，CI 用 schema.sql 指纹决定是否备份。
- 可观测性下限：`database_trace.go` 记录慢 SQL 的**哈希前缀**、路由、耗时，不记 SQL 正文与参数（避免泄漏节点凭据），阈值 `DB_SLOW_QUERY_THRESHOLD` 可配。
- 安全基本盘：bcrypt cost 12 + 用户不存在时比对固定假哈希（`main.go:358`，抗时序枚举）、`DisallowUnknownFields` + 16KB body 上限、Cookie HttpOnly/SameSite=Lax/生产 Secure、探针 token 只存 SHA-256、部署密钥 AES-256-GCM（AAD 固定 `ip-list/probe-token/v1`）。

**需要正视的地方**

| 级别 | 问题 | 证据 | 影响 |
| --- | --- | --- | --- |
| P1 | 全局 10 秒请求超时 vs 曾实测 9,278 ms 的重查询 | `main.go:273`、`docs/database-performance.md:49` | 数据量再涨一点，`/available-ips` 会撞超时返回 503，前端表现为“后端不可用” |
| P1 | 单次全量扫描无断点续跑；巡检重启后整轮重跑 | `scans.go:40` `scanTTL=24h`、`node_scheduler.go:147` 重开轮次时 `Completed=0` | 4,096 IP 的单次扫描遇重启即中断，进度只能靠 Redis 快照 24h 内查看；巡检虽由 `node_scan_schedules` 自动接续，但从新一轮开头重算，已测结果不复用 |
| P1 | 单管理员、无 RBAC、无操作审计 | `users` 表仅 id/username/password_hash；无 audit 表 | 多人协作无法区分责任，误操作不可追溯 |
| P1 | 内嵌 mihomo（GPL-3.0） | `README.md:225` 已注明许可 | 分发二进制（`bin/ipguard` 内含 mihomo 代码）需履行 GPL 义务，对外闭源分发存在合规敞口，需法务确认 |
| P2 | CSRF 仅靠 Origin 头 | `main.go:269` | 同源 XSS 场景无防护；`SameSite=Lax` 已挡住跨站表单，风险可控但不完整 |
| P2 | 无 metrics / trace / 告警 | 仅 `/health` 与慢 SQL 日志 | 扫描吞吐、Redis 租约异常、DNS 失败率都只能看日志 |
| P2 | 仓库噪声 | `xray-oneclick.sh`(55KB 未跟踪)、`.DS_Store`、`bin/`(78MB 二进制)、`test-results/`、`tsconfig.tsbuildinfo`、`scripts/__pycache__/` | 克隆体积与新人理解成本；建议加入 `.gitignore` 或迁出 |
| P2 | 单包 79 文件无分层 | 全 `package main` | 14.9k 行时仍可用，但 `dns.go`/`scans.go` 继续长会难以测试 |
| P2 | 巨型前端组件 | `cf-manager.tsx` 33.5KB、`dns-manager.tsx` 24.6KB、`available-ip-list.tsx` 23.3KB | 单文件承担表单+列表+状态机+轮询，改动回归面大 |
| P3 | 版本激进 | Go 1.26、Next 16、React 19.2、Tailwind 4 | 上游 breaking change 频繁，AGENTS.md 已提醒“这不是你熟悉的 Next.js” |

---

## 7. 改进建议（按投入产出排序）

1. **给重查询单独放宽超时**（半天）：在 `routes()` 里对 `/available-ips`、`/scan-records`、`/networks` 使用 25–30 秒上下文，其余保持 10 秒；同时把 `docs/database-performance.md` 里的 33 ms 目标写进 CI 的基准断言，防止回退。
2. **扫描断点续跑**（1–2 天）：`node_scan_rounds` 已每 10 秒写心跳（`completed/success/status`），只差把已完成 IP 的明细或位图一并落库；单次扫描重启后从 `completed` 续跑，巡检重开轮次时也复用该轮已完成结果。Redis 保留为热缓存。
3. **补一张最小审计表**（1 天）：`audit_log(user_id, action, target, before/after jsonb, at)`，只记写操作与 DNS 同步，配合现有慢 SQL 日志即可定位事故。
4. **拆分三个巨型组件**（2–3 天）：`cf-manager` 拆 `target-table` / `target-dialog` / `scan-config-panel`；`dns-manager` 拆 `policy-form` / `plan-preview` / `sync-history`。按现有 `use-paged-list`、`use-list-options` 的 hook 约定继续下沉。
5. **后端按领域拆包**（1 周，低优先）：`internal/scan`、`internal/dns`、`internal/probe`、`internal/store`，先迁 `dns.go` 与 `scans.go` 两个热点，保持 handler 薄壳。
6. **补最小可观测性**（1 天）：`/metrics`（Prometheus 文本格式即可）暴露扫描队列深度、活跃租约、各池可用 IP 数、DNS 最近同步结果码。
7. **仓库卫生**（1 小时）：`.gitignore` 已覆盖 `test-results/`、`*.tsbuildinfo`、`__pycache__`、`.DS_Store`、`bin/`，`git ls-files` 也确认它们都没有被跟踪（已复核）——问题仅在工作区留有约 78MB 的 `bin/`、55KB 的 `xray-oneclick.sh` 等未跟踪文件——实际缺的是把 `.DS_Store`、`bin/`、`tsconfig.tsbuildinfo` 从索引/工作区清出；`xray-oneclick.sh` 决定归档或删除。

---

## 8. 一句话总评

**业务模型成熟度高于代码组织成熟度**：证据链、恢复观察、DNS 保守兜底这些“想清楚了才写得出来”的部分已经到位，工程债集中在“单包后端 + 巨型前端组件 + 几个硬编码超时”这类可增量偿还的地方。当前规模（~2.3 万行自有代码、29 张表、单一管理员）仍在可控区间，最适合先修 P1 的两条硬约束，再考虑架构分层。

---

## 9. 复核记录（第二次逐条核对代码，2026-11-16）

复核方式：不依赖文档与 README 自述，直接读源码与 `git ls-files` 重新统计；下列条目与第 1–8 节有出入处以本节为准。

**已修正的三处**

1. **API 代理数量**：实际是 **15 个** `src/app/api/**/route.ts`，不是 9 个（第 4.1 节已改）。
2. **扫描进度持久化**：原判断“仅 Redis 24h、重启全废”过重。真实行为是——巡检任务的 `job` JSON（含 `completed/success`）每 10 秒写入 PostgreSQL `node_scan_schedules`（`scans.go:480-487` → `scan_records.go:85`），重启后 `resumeNodeSchedules`（`node_scheduler.go:14`）会自动接续，但 `node_scheduler.go:146-149` 把 `Completed` 重置为 0，**从新一轮开头重跑**；单次（`interval_minutes=0`）扫描没有调度记录，重启后只能读到 Redis 快照，轮次在 60 秒无心跳后被标记 `interrupted`。所以问题是“不能断点续跑”，不是“全丢”。
3. **仓库卫生**：`.gitignore` 已经覆盖全部噪声目录，`git ls-files` 里没有任何被跟踪的 `bin/`、`.DS_Store`、`*.tsbuildinfo`，原先“从索引清出”的建议不成立。

**复核确认无误的关键事实**

- 规模：后端 79 个 `.go`（其中 41 个非测试文件 8,349 行）、前端 91 个 TS/TSX 共 8,591 行、119 个 `Test*` 函数、`schema.sql` 29 表 / 5 视图 / 20 索引、116 次提交（最后一次 2026-11-11 08:41）。
- `main.go:273` 全局 10 秒请求超时；前端 `src/lib/api.ts:30` 是 12 秒（`AbortSignal.timeout(12000)`），只比后端多 2 秒余量，重查询超时后前端只会显示“无法连接后端服务”。
- `main.go:123-131` 恰好 9 个后台循环；`src/` 内 `: any` / `as any` 出现次数为 0（grep 实测）。
- 三层证据视图与恢复观察（3 轮 / 每轮间隔 ≥5 分钟 / 覆盖 ≥10 分钟）在 `schema.sql` 与 `ip_health_recovery` 里成立。
- 单 IP 并发与内存边界：`xrayScanSlots` 容量 64 与 `input.Concurrency` 校验上限 64 一致；`scans.go:189` 只对节点增量/全量做 4096 上限（`blacklist.go:150`），**CF 全量不限 4096**，靠 `iter.Seq` 惰性遍历控制内存（`scans.go:338` 预分配上限 4096）。
- 扫描协程用的是 `app.scanContext`（进程根 context，`patrol.go:21`、`cf.go:489`），不是 HTTP 请求 context，因此 10 秒请求超时**不会**误杀已经在跑的任务——这点比原报告隐含的担心更稳。

**原报告遗漏、值得补进功能地图的两点**

- **网站回源 DNS（`url.<域名>`）**：`dns_policy.go:184-192` 显示 CF 池另有独立出口——只有 `website_enabled` 且归属地限定 `hk/jp/sg`、且 HTTPS 复查 URL 与配置完全一致时，才会写入 `url.<托管域名>` 记录，不进入国家/地区记录。也就是说“国家记录”和“网站回源记录”是两套准入规则。
- **新加坡兜底**：`dns_policy.go:199-211`，当 `sg.<域名>` 的 A 或 AAAA 没有任何合格 IP 时，单独用香港候选中延迟最低的最多 3 个 IP 兜底，并按 A/AAAA 分别判断。这是原报告“地区独立基准”描述里没有的隐藏策略。

**复核新增的一条风险（P2，已核实代码路径）**

- **关闭网站优选后 `url.<域名>` 记录不会被回收**：`dns_huawei.go:154` 的过滤条件是 `dnsCountry(prefix) != "" || (WebsiteEnabled && prefix == "url")`，一旦 `WebsiteEnabled=false`，`url.` 记录直接不进 `desired`、`byKey`，也就不会产生 `update`/`delete` 计划；而 `dns_managed_records` 里该记录依然存在。结果是它会一直停在上次同步的 IP 上，不再有 HTTPS 复查与探针健康判断托底。建议：关闭网站优选时把 `url.` 记录纳入计划并执行删除（或明确在文档里写明“关闭后需手动清理”），并补一条对应的单测。

---

## 10. 生产部署实测（192.3.110.240，2026-11-16 21:00 CST）

> 通过 ShellRS 的 `cloudnium-lax` 主机登录实测，全部为只读检查。

### 10.1 拓扑与版本

| 项 | 实测值 |
| --- | --- |
| 主机 | `localhost`（主机名），Debian GNU/Linux 13 (trixie)，2 vCPU / 1,974 MB，根盘 20 GB 已用 70%（剩 5.7 GB），inode 27%，uptime 44 天 |
| 发布 | `/opt/ip-list-current` → `cd166604be04…`（即最新提交 `cd16660`），上一版 `27b2b9c9dc…` |
| 服务 | `ip-list-api`、`ip-list-web` 均 active，2026-11-11 08:45:31 启动，`NRestarts=0`；RSS 分别 132 MB / 93 MB |
| 对外入口 | nginx `:8443` TLS → `127.0.0.1:3000`；API `127.0.0.1:8081`；`APP_ENV=production`、`APP_ORIGIN=https://192.3.110.240:8443` |
| 证书 | 自签 `CN=192.3.110.240`，2027-10-25 到期；`PROBE_TLS_PIN` 与当前证书公钥指纹**一致**（探针部署命令可用） |
| 同机其他服务 | xray、sing-box、ss-server、caddy（:80/:443）、nginx air-panel(:7000)。**这不是专用面板机，而是美国落地代理节点**，扫描/测速与代理流量共享 2 核 |
| 扫描内核 | `PROBE_KERNEL=xray`、`XRAY_BIN=/usr/local/bin/xray-ip-list`（与默认 mihomo 路径不同） |
| 健康检查 | `/health` → `{"status":"ok"}`；`/login` 200 |

### 10.2 数据实况

| 指标 | 实测值 |
| --- | --- |
| 配置 | 节点 6、启用网段 187、黑名单 781、探针 1 |
| 证据层 | `node_ip_pool` 482,297 行（10-27 起），其中 `available=86` |
| 当前池 | `current_node_ip_pool` 共 451,569 行，**其中 `available` 仅 46 行**（其余是保留的失败样本，用于续跑与对照） |
| 共享池 | `shared_node_ip_pool` 可用 230 行（46 × 5 个协议投影） |
| 数据库 | `ipguard` 810 MB；最大表 `ip_health_checks` 268 MB / 314,425 行（7 天保留，约 4.5 万行/天）、`node_ip_pool` 245 MB、`ip_results` 97 MB |
| 探针 | 24 小时 29,792 条采样，最新 21:02:01，在线正常 |
| 定时任务 | 02:00 `daily-scan` 入队成功（grpc 187 / cf 172）；05:00 `daily-maintenance` 完成（报告库 792 MB） |

### 10.3 扫描产出：一天 48 万次探测换 72 个 IP

| 日期 | 轮次 | 探测 IP | 成功 | 成功率 |
| --- | --- | --- | --- | --- |
| 2026-11-16（至 11:08 完成） | 223 | 481,523 | 72 | **0.015%** |
| 2026-11-15 | 141 | 331,513 | 41 | 0.012% |

- 节点侧队列在 11:08 就排空了，等于用约 11 小时、48 万次代理探测，最终只让 46 个 IP 通过健康门禁；平均约 **6,700 次探测才留下 1 个可用 IP**。
- DNS 实际发布很薄：`de` 3、`gb` 2、`hk` 3、`jp` 4、`sg` 1、`tw` 1、`us` 8、`url` 23 个 IP；最新一轮同步 `gb`/`url`/`us` 为 update 且 `applied=true`，`warning=none`。
- 结论：网段池已明显“被采空”，继续按 187 个网段每晚全量扫，投入产出比极低。

### 10.4 CF 队列排不完（P1，实测）

- 02:00 入队 172 条 CF 任务，到 21:00 仍剩 **123 条**（已 19 小时）。
- 同一台机器上：节点扫描 4,096 IP 约 **5 分钟/轮**，CF 扫描 4,096 IP 要 **21 分钟/轮**（18:55、19:17、19:38、20:00、20:21 连续五轮，每轮 success=0）。
- 换算吞吐：节点约 4.4 万 IP/小时，CF 约 1.17 万 IP/小时。172 个网段里只要有几个 /18（16,384 个地址），单晚 CF 工作量就远超 24 小时可处理量 → **每晚新增的队列注定积压，永远排不完**。
- 根因是三重叠加：单轮 4096 的批量上限、`cfLock` 串行化整个 CF 池、每次 CF 探测都是一次真实 HTTPS 请求（21 分钟/4096 ≈ 0.3 秒/IP，接近单次超时量级）。

### 10.5 退避机制在当前部署下从未生效

- `network_scan_backoff` **0 行**，而 `usesDailyNetworkBackoff` 的条件是 `protocol == 'grpc' && interval_minutes == 1440`（`network_scan_backoff.go:9-11`）。
- 线上 `node_scan_schedules` **是空表**——没有 24 小时巡检任务；每晚全量来自 `daily-scan.py` 写入的 `network_auto_scans` 队列，该路径**不更新 no_output_streak**。
- 所以“连续无产出 → 1/3/5/7/15 天”的退避在线上一次都没触发，死网段每天照扫，这正是 10.3 低产出的制度性原因。

### 10.6 其他运行问题

- **飞书通知未送达**：`2026/11/16 20:49:59 DNS 变更飞书通知未全部送达：飞书通知请求失败`。DNS 写入本身成功，只有通知这一跳失败（该机在 LAX，出网到 `open.feishu.cn` 不通或被限）。
- **慢查询**：24 小时 166 条超过 500 ms，最大 `background` 4.58 s、`GET /probe-agent/targets` 3.46 s、`POST /probe-agent/results` 2.02 s；用户页面路由未触发慢日志，但探针接口已接近 10 秒上限。
- **遗留失败单元**：`ip-list-dns-migrate-f4e5575.service`（10-28 的一次性 `systemd-run` 迁移）仍处 failed 状态，只影响 `systemctl` 观感。
- 磁盘余量 5.7 GB、inode 27%，按当前 8 个月 810 MB 的增速短期无风险；主要增量来自 `ip_health_checks`。

### 10.7 安全基线（实测）

- `PermitRootLogin yes` + `PasswordAuthentication yes` + 无 ufw + 无 fail2ban，而这台机器同时是公网代理节点，SSH 爆破面较大。
- `/etc/ip-list.env` 权限 `-rw------- root root`（正确）；华为云 AK/SK、管理员密码、`PROBE_CREDENTIAL_KEY` 都在其中，权限正确但需保证备份不外泄。
- 面板是自签证书 + 非标准 8443，符合项目“自签站点 + TLS pin”设计；但 **2027-10-25 换证时必须同步更新 `PROBE_TLS_PIN` 并重新部署探针**，否则探针会全部失联。

### 10.8 线上优先动作（按收益排序）

1. **先止血 CF 队列**：把 123 条积压按“昨日有产出”过滤后再处理，或临时把 CF 并发提到 128 / 超时降到 3 s；否则低谷期 CF 池永远落后一天以上。
2. **让退避覆盖 nightly 队列**：在 `network_auto_scans` 入队或消费处读取 `network_scan_backoff`（或直接用“近 7 天该网段产出”决定是否入队），可把 48 万次探测砍掉一大半。
3. **网段瘦身**：按近 7 天每网段产出排序，0 产出的直接停用；187 → 60–80 个能显著降低每晚负载，也减少 CF 积压。
4. **修飞书通知链路**：换可通的 webhook 出口（或用代理），否则 DNS 变更长期静默。
5. **SSH 加固**：改密钥登录、`ufw` 只放 22/8443、可选 fail2ban；这台机器是公网代理节点，收益立竿见影。
