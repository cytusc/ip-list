# Rust 控制面功能等价基线

验收标准：**旧 Go 控制面的功能全集**。Rust 实现只有在字段、筛选、排序、状态和错误语义与旧实现一致时才视为完成；接口返回 `200` 或 SQL 能执行不算完成。

基线来源：`299611f^` 的 `backend/main.go` 与 `backend/internal/ipguard`。

## 对照矩阵

| 功能域 | 旧 Go 行为 | Rust 实现 | 验证 |
| --- | --- | --- | --- |
| 健康检查 | `/health` 检查 PostgreSQL/Redis，失败 503 | `main.rs::health` 执行 `SELECT 1` + Redis `PING` | 生产 200，`{"status":"ok"}` |
| 认证 | login/logout/me，Redis 会话 | 原生 Rust，Cookie `ipfilter_session` | 登录/鉴权通过 |
| 节点/网段 | 列表、创建、更新、删除、导入、网段恢复 | `rest.rs` 原生处理器 | 列表分页对象、CRUD 路由齐全 |
| CF | targets CRUD、触发扫描、pool、scan summary | `rest.rs` 原生处理器 | `/cf-scan`、`/cf-pool`、`/cf-targets` 通过 |
| 可用 IP | 探针延迟、DNS 状态、地区/服务商/搜索、排序、测速门槛 | `available_ip_data` 重写为 Go 等价 SQL + DNS 报告解析 | 生产 DNS 筛选与排序逐项通过 |
| 证书异常 | 分页列表 | `certificate_issues` | 200，分页对象 |
| DNS | 配置读写、preview/sync、records/candidates/history | `rest.rs` 原生处理器 | 列表与模式路由通过 |
| 扫描 | 启动、latest、详情、取消、scan records、网段进度 | `rest.rs` 原生处理器 | `network_progress` 生产为对象 |
| 探针 | 列表、创建、更新、删除、token、deployment、history、probe-agent | `rest.rs` 原生处理器 | 分页对象、探针详情通过 |
| 黑名单 | 查询、添加、删除 | main + `rest.rs` | 分页对象 |
| 手工测速/网段重检 | Go worker 内部接口 | Rust 未注册，走 `/api` fallback 代理到 Go worker | 非法 IP 测速返回 422，路由可达 |

## 契约说明

- `/api/lists/*` 统一返回 `{items,total,page,pageSize}`，前端 `usePagedList` 依赖此结构。
- 旧 Go 直链接口保留裸数组 + `X-Total-Count`/分页头，例如 `/api/available-ips`、`/api/dns/records`。这是兼容层，不是新前端的主路径。
- `available-ips` 的 `node_dns_status`、`url_dns_status` 只接受 `all`/`synced`/`not_synced`/`disabled`/`unknown`（空字符串等同未筛选），非法值返回 422。
- `available-ips` 的 `sort`：`probe_delay`、`checked_at`、`speed_test`；`direction` 为 `asc`/`desc`，无有效测量值的行置后。

## 已知架构风险

1. 手工测速与 `/networks/{id}/recheck` 仍依赖 Go worker fallback；worker 不可用时这两个功能不可用。
2. `scan-records` 的网段进度查询较重，Rust 控制面使用 10 秒进程内缓存；冷启动首次请求仍可能约 1–2 秒。
3. 控制面聚合 SQL 仍是从 Go 实现翻译过来的；没有共享 SQL/视图，未来容易漂移。
4. 当前没有在 CI 中运行真实 PostgreSQL/Redis 的端到端契约测试；`scripts/parity-check.py` 是部署后/本地联调用的 live checker，不是 CI 替代品。

## 验收命令

```sh
PARITY_BASE_URL=http://127.0.0.1:3000 \
ADMIN_USERNAME=... ADMIN_PASSWORD=... \
python3 scripts/parity-check.py
```

检查覆盖：health、全部 `/api/lists/*` 分页契约、可用 IP 的 DNS 筛选与探针排序、扫描网段进度、手工测速路由、旧直链接口兼容性。
