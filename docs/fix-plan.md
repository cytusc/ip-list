# 修复方案（基于 2026-11-16 代码复核 + 线上实测）

## 0. 结论先说

我按“是不是 bug”重新过了一遍代码，结论和第一印象不一样：

- **代码底子比预期好**。`go vet` 0 告警、`tsc --noEmit` 0 错误、`eslint src` 0 错误；扫描锁有独立的心跳续期（`scans.go:44-47` 的 Lua 脚本每 5 秒 `expire` 到 45 秒），不会出现“锁过期导致重复扫描”；DNS 有“监控离线 hold、全部删除前拦截、空池不删记录”三层兜底；探针上报有 2MB/10000 条上限、IP 白名单与模式校验。**这些都不是 bug，不要改。**
- **真正要改代码的只有 4 处**，其余是“扫什么、扫多少”的策略问题和数据治理问题。
- **CF 不是“变慢”，是结构性超卖**：187 个网段共 **740,608 个 IP**，节点侧 44,000 IP/小时需 17 小时（勉强赶上 24 小时窗口），CF 侧 11,700 IP/小时需 **63 小时**。也就是说，即使 CF 一秒不歇，也永远扫不完当晚的队列。**调并发最多把 63 小时压到 31 小时，仍然排不完——必须减量，不能只提速。**

---

## 1. 问题分三类，别混在一起修

| 类型 | 具体 | 修法 |
| --- | --- | --- |
| 代码缺陷 | 请求超时一刀切、退避没接上夜间队列、`url.` 记录不回收、数据无时间维度清理 | 改代码（第 2–3 节） |
| 策略/数据 | 187 网段只产出 46 个可用 IP、CF 队列超卖 2.6 倍、探针只有 1 个 | 改入队策略与网段池（第 4 节） |
| 运维 | root 密码登录、无防火墙、飞书出口不通 | 改配置（第 5 节） |

---

## 2. P0 修复（改代码，按收益排序）

### P0-1 请求超时按路由分级（10 秒 → 关键查询 30 秒）

现状：`main.go:273` 给所有请求 10 秒，而 `docs/database-performance.md:49` 记录 `/available-ips` 曾达 9,278 ms，线上慢日志里 `GET /probe-agent/targets` 也已到 3.46 s。余量只剩几百毫秒。

```go
// backend/main.go:273 附近
_, pattern := mux.Handler(r)
timeout := 10 * time.Second
switch pattern {
case "GET /available-ips", "GET /probe-agent/targets", "POST /probe-agent/results":
    timeout = 30 * time.Second
}
ctx, cancel := context.WithTimeout(r.Context(), timeout)
defer cancel()
ctx = context.WithValue(ctx, databaseRequestKey{}, pattern)
```

前端要同步改，否则后端等 30 秒、前端 12 秒就放弃了：`src/lib/api.ts:30` 的 `AbortSignal.timeout(12000)` 改成可传参，`/available-ips` 用 32 秒，其余保持 12 秒。

**验证**：`EXPLAIN (ANALYZE, BUFFERS)` 跑一遍 `availableIPCandidatesSQL` 的两个分支，确认 P95 < 5 秒；再人为把 `statement_timeout` 调低复现超时，确认返回 503 而不是前端“无法连接后端服务”。

---

### P0-2 让退避真正接上夜间队列（当前 0 行数据的根因）

现状：`usesDailyNetworkBackoff` 要求 `protocol=grpc && interval_minutes=1440`（`network_scan_backoff.go:9-11`），只有巡检路径会调用 `updateDailyNetworkBackoff`（`patrol.go:22`）。线上 `node_scan_schedules` 是空表、没有巡检，夜间全量走 `scripts/daily-scan.py` 的队列 —— **两条路互不相通，退避永远不会触发**。

两步改：

**① 入队时跳过未到期的网段**（`scripts/daily-scan.py` 的 `QUEUE_SQL`）：

```sql
WHERE n.enabled AND (p.protocol='grpc' OR NOT :'nodes_only'::boolean)
AND NOT EXISTS (
  SELECT 1 FROM network_scan_backoff b
  WHERE b.network_id=n.id AND b.protocol=p.protocol AND b.cidr=n.cidr
    AND b.next_scan_at > now()
)
```

**② 队列任务扫完后记录产出**（`automatic_scans.go` 的 `remove()` 之前调用）：

```go
// 新增：队列任务收尾时更新退避，只在整个网段排空后记一次
func (a *app) recordQueueBackoff(ctx context.Context, networkID, protocol, cidr string) error {
	table := "node_ip_pool"
	if protocol == "cf" {
		table = "cf_ip_pool"
	}
	_, err := a.db.Exec(ctx, `
WITH prev AS (
 SELECT COALESCE((SELECT no_output_streak FROM network_scan_backoff
  WHERE network_id=$1 AND protocol=$2 AND cidr=$3::cidr),0) AS streak
), outcome AS (
 SELECT EXISTS(SELECT 1 FROM `+table+`
  WHERE available AND ip <<= $3::cidr AND checked_at>now()-interval '24 hours') AS produced
)
INSERT INTO network_scan_backoff(network_id,cidr,protocol,no_output_streak,interval_days,next_scan_at,updated_at)
SELECT $1,$3::cidr,$2,
 CASE WHEN o.produced THEN 0 ELSE p.streak+1 END,
 CASE WHEN o.produced THEN 1 WHEN p.streak<2 THEN 1 WHEN p.streak=2 THEN 3
      WHEN p.streak=3 THEN 5 WHEN p.streak=4 THEN 7 ELSE 15 END,
 now()+make_interval(days => CASE WHEN o.produced THEN 1 WHEN p.streak<2 THEN 1
      WHEN p.streak=2 THEN 3 WHEN p.streak=3 THEN 5 WHEN p.streak=4 THEN 7 ELSE 15 END),
 now()
FROM prev p, outcome o
ON CONFLICT(network_id,protocol) DO UPDATE SET
 cidr=excluded.cidr,no_output_streak=excluded.no_output_streak,
 interval_days=excluded.interval_days,next_scan_at=excluded.next_scan_at,updated_at=excluded.updated_at`,
		networkID, protocol, cidr)
	return err
}
```

注意必须**在整个网段排空（`total == 0`）时记一次**，不能每批 4096 就记一次 —— 否则一个 /18 一晚上会 +4 次 streak，几天就被判死。

**为什么这条收益最大**：当前 187 个网段里有 22 个是 ≤/18，正是它们撑起了 74 万 IP 的夜间工作量；连续 0 产出会让它们按 1→3→5→7→15 天滑落，夜间扫描量能在两周内自然降到 1/3 左右。

---

### P0-3 CF 先减量、再提速（顺序不能反）

现状：`automatic_scans.go:179` 对 CF 也是 4096/批，`automaticScanSample` 把并发写死 16，`cf.go:388` 校验上限 32；实测 4096 个 IP 要 21 分钟（≈3.2 IP/s，相当于每个 IP 0.31 秒，已经接近单次 TLS 握手 + 5 秒超时的量级）。

**① 减量（必做）**：夜间只给 CF 排“近 7 天有产出”的网段，其余按 `last_checked_at` 轮转，每晚设配额：

```sql
-- daily-scan.py，替换 p.protocol='cf' 的适用范围
AND (p.protocol='grpc' OR n.id IN (
  SELECT id FROM (
    SELECT n2.id, row_number() OVER (ORDER BY max(p2.checked_at) NULLS FIRST) AS rn
    FROM networks n2
    LEFT JOIN node_ip_pool p2 ON p2.ip <<= n2.cidr AND p2.available
    WHERE n2.enabled GROUP BY n2.id
  ) q WHERE q.rn <= 30
))
```

**② 提速（可选，机器撑得住才做）**：`automaticScanSample` 里 CF 的 `input.Concurrency = 16` 改为 24，`Timeout` 5000 → 3000；`cf.go:388` 上限同步放到 32。**不要直接上 64**：这台机器 2 vCPU / 1.9 GB，还同机跑 xray、sing-box、ss-server 与面板本身。

按 24 并发 + 3 秒超时估算，CF 吞吐可到 18–20k IP/小时；配合 30 网段配额，单晚工作量约 15–20 万 IP，24 小时内能排完。

---

### P0-4 关闭网站优选后回收 `url.` 记录

现状已核实：`dns_huawei.go:154` 的过滤条件 `dnsCountry(prefix) != "" || (WebsiteEnabled && prefix=="url")` 一旦 `WebsiteEnabled=false`，`url.<域名>` 既不进 `desired` 也不进 `byKey`，于是永远不进计划 —— 不更新、也不删除，停在上次同步的 IP 上，且不再有 HTTPS 复查托底。

```go
// backend/dns_huawei.go:154
if !ok || (dnsCountry(prefix) == "" && prefix != "url") || (r.Type != "A" && r.Type != "AAAA") {
    continue
}
```

配合 `dnsDesired` 在 `WebsiteEnabled=false` 时不生成 `url.` 键，该记录会走 `dnsBuildPlan:197-204` 的 `len(r.Desired)==0 && managed[key]` → `Action: "delete"` 分支，被正常回收。

**必须补测试**（`dns_huawei_test.go`）：`WebsiteEnabled=false` + 已存在 `url.reiiv.com` 托管记录 → 期望 `action=delete`；`WebsiteEnabled=true` 且 `url.` 无合格 IP → 保持 `hold` 而不是 `delete`（避免把刚配好的回源记录删掉）。

---

## 3. P1 修复

### P1-1 给 `node_ip_pool` / `ip_results` 加时间维度保留

现状：`data_retention.go:19-29` 只按 `config_hash` 是否仍属于某启用节点来删，**没有任何按 `checked_at` 的清理**。线上 `node_ip_pool` 已 482,297 行 / 245 MB（10-27 起累计，扫过的每个 distinct IP 一行），照当前每晚 74 万 IP 的扫描面，这张表会持续膨胀。

```go
// backend/data_retention.go，加在现有两条 DELETE 之后
tag, err = tx.Exec(ctx, `DELETE FROM node_ip_pool
 WHERE checked_at < now()-interval '30 days' AND NOT available`)
if err != nil { return 0, err }
removed += tag.RowsAffected()
tag, err = tx.Exec(ctx, `DELETE FROM ip_results
 WHERE checked_at < now()-interval '30 days' AND NOT available`)
```

保留 30 天是因为失败样本要支撑“续跑”和“重复扫描跳过”；可用 IP 永不删（它们由证据链自己失效）。

### P1-2 飞书通知加一次重试

现状：`dns_notify.go:259-269` 单次 5 秒超时，失败只写 warning。线上 20:49 就真失败过一次（LAX → `open.feishu.cn` 不通）。建议失败后重试 1 次（间隔 2 秒），仍失败才写 warning；如果确认出口长期不通，就把 webhook 换成可通的域名/代理，而不是反复重试。

### P1-3 每轮扫描留一行“产出摘要”日志

现在的日志只有失败信息，运维看不到“这轮值不值得”。在 `runScanTargets` 收尾处加一行结构化日志：

```
job=<id> pool=<protocol> range=<cidr> total=4096 success=0 avg_delay=0ms elapsed=5m12s
```

这行日志是把“48 万次探测换 72 个 IP”变成趋势图的最低成本手段，也是后续判断退避阈值是否合理的依据。

### P1-4 网段体检脚本（不改代码）

给一个只读 SQL（放 `scripts/range-audit.sql`），按近 14 天产出排序，输出建议停用清单：

```sql
SELECT n.name, n.cidr::text, count(p.ip) FILTER (WHERE p.available) AS hits_14d
FROM networks n LEFT JOIN node_ip_pool p
  ON p.ip <<= n.cidr AND p.checked_at > now()-interval '14 days'
WHERE n.enabled GROUP BY n.id, n.name, n.cidr
ORDER BY hits_14d ASC, n.name LIMIT 60;
```

线上直接跑这条，就能拿到“先停哪 60 个网段”的清单。

---

## 4. 已核实“不是问题”的 5 处（不要动）

1. **扫描锁不会过期导致重复扫描**：`scans.go:44-47` 的 `scanControlScript` 每 5 秒心跳 `expire KEYS[1] 45`，与 `SetNX` 的 45 秒 TTL 配对，CF 21 分钟的轮次也在续期。线上 `cf_target_rounds` 的时间戳首尾相接、无重叠，实测印证。
2. **探针掉线不会清空 DNS**：`dns.go:274` 的 `dnsMonitorReady` 先判探针在线与新采样，`dnsBuildPlan:187` 把整轮置为 `hold`；`dns.go:552` 还有“全部解析都将被删除”的整轮拦截。空池只影响新增，不会变成记录删除。
3. **探针上报无越权风险**：`latency_probes.go:399-478` 有 2 MB / 10000 条上限、`DisallowUnknownFields`、IP 规范化与批内去重、模式/端口与探针配置比对（不一致直接 409），写入前还过 `probeTargetPoolSQL` 白名单与黑名单。
4. **SQL 无外部拼接注入面**：动态 SQL 只拼内部常量（视图名、`probeOnlineSQL`、排序片段），所有用户输入走参数；`kind` 走白名单函数。
5. **前端无 `any` 逃逸**：`src/` 内 `: any` / `as any` 计数为 0，`tsc --noEmit` 干净。

---

## 5. 运维侧（不改代码，但和代码同等重要）

1. **SSH**：root + 密码 + 无 ufw + 无 fail2ban，这台机同时是公网代理节点。改成密钥登录 + `ufw allow 22,8443/tcp` + `PermitRootLogin prohibit-password`。
2. **证书换期**：当前自签证书 2027-10-25 到期，`PROBE_TLS_PIN` 与它匹配。换证时**必须同步更新 `PROBE_TLS_PIN` 并重新部署探针**，否则所有节点探测会在 `tls: bad certificate` 上静默失败——这是最容易在两年后被忘掉的一个坑，建议写进 `docs/cicd.md` 的证书章节。
3. **探针单点**：只有 1 个 `LatencyProbe`，它决定 DNS 是否 `hold`。建议至少再加 1 个不同线路的探针，`DNS_PROBE_ID` 留空表示“任一在线即可”。

---

## 6. 上线顺序与回归清单

**第一批（低风险，当天可上）**：P0-1 超时分级、P1-2 通知重试、P1-3 日志、P1-4 体检脚本。
回归：`go -C backend test ./...`（需要测试库）、`tsc --noEmit`、部署后 `curl /health`、打开 `/available-ips` 观察耗时。

**第二批（改行为，需观察 1–2 天）**：P0-2 退避接队列、P0-4 `url.` 回收。
观察点：`network_scan_backoff` 从 0 行开始出现数据；次日 02:00 入队条数应从 187/172 降到 120 左右；`dns_sync_runs` 里 `url.reiiv.com` 在关闭网站优选时出现 `delete` 且 `applied=true`。

**第三批（吞吐与数据治理）**：P0-3 CF 配额与并发、P1-1 保留策略。
观察点：CF 队列水深（`network_auto_scans` 计数）在 24 小时后应趋近 0；`cf_target_rounds` 单轮耗时应从 21 分钟降到 10–13 分钟；`node_ip_pool` 行数应在 30 天后首次下降。

**每批都要跑**：`go vet ./...`、`go test ./...`、`npx tsc --noEmit`、`eslint src`，以及部署后 5 分钟内的 `/health`、`journalctl -u ip-list-api -p err`。

---

## 7. 明确不建议做的

- **不要为了“扫得更快”提高全局并发到 64+**：2 vCPU 上 xray 每 IP 起一个进程（`probe_xray.go:80`），64 并发意味着 64 个进程 + 64 个临时目录，会先把代理业务拖垮。
- **不要关掉 15 分钟健康门禁**：`current_node_ip_pool` 的可用 IP 会瞬间从 46 涨到几千个“看起来能用”的历史 IP，DNS 会把它们发出去，这是拿可用性换数字。
- **不要给 `node_ip_pool` 跑全表 VACUUM**：245 MB 表加 autovacuum 已经够用，手工全表 VACUUM 会在这台 1.9 GB 内存的机器上锁表数分钟。
- **不要用“重启服务”解决 CF 积压**：队列在 PG 里，重启只会让正在跑的那轮作废重来。
