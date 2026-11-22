# Rust 混编改造说明

本文件记录 A/B/C 三阶段的落地结果、边界和验证命令。目标不是全量替换，而是把常驻进程收敛为“Rust 控制面 + Go mihomo worker”，去掉常驻 Node 和 Go 控制面。

## 进程拓扑

| 进程 | 语言 | 端口 | 职责 |
| --- | --- | --- | --- |
| `control` | Rust | 3000（生产）/ 8088（开发） | 静态 `out/`、登录/退出/会话、全部 `/api` 鉴权（探针 Bearer 除外）、CRUD、聚合查询、DNS 计划、探针上报、扫描/CF 队列、探针下载 |
| `ipguard-worker` | Go | 127.0.0.1:8082 | `engine_jobs` 消费、mihomo 扫描/证书复查/健康复查/测速、DNS 同步与定时维护；只监听环回内部接口 |
| `latency-probe` | Rust | 无 | 物理网卡绑定的 ICMP/TCP 延迟探针 |

Rust 控制面是唯一的浏览器入口。它自行校验 Redis 会话，不再把 Cookie 转发给 Go；只有必须调用 mihomo 的少数动作通过环回端口转发，并注入 `X-Internal-Auth`、`X-Internal-User-ID`、`X-Internal-Username`。Go worker 的 `protected` 中间件只信任这组内部断言。

## A：控制面/数据面拆分 + Rust 探针

- `backend/internal/ipguard/`：原 Go 业务代码统一为 `package ipguard`，`cmd/worker` 与可选的本地调试入口共用。
- `backend/cmd/worker`：`Open` → `EnableLocalEngine` → `StartBackground` → `RunEngine`，独占全部扫描、证书复查、健康复查、测速、DNS/维护协程。
- `engine_jobs` 队列使用 `FOR UPDATE SKIP LOCKED`；控制面只 `INSERT` 入队，worker 启动时把残留 `running` 置为 `failed`。
- Rust 探针位于 `backend/rust/latency-probe`，实现 ICMP（系统 ping）与 TCP 握手、物理网卡精确匹配、TLS SPKI pin、批量上报和心跳；Windows 构建产物约 2.6 MB。

## B：Next 静态化 + Rust 网关

- `next.config.ts`：`output: "export"`、`trailingSlash: true`、`images.unoptimized: true`。
- 删除 `src/app/api/**` 与 `src/app/probe-downloads/**`。
- 全部 console 页面改为客户端取数；动态路由 `[id]` 改成 `/cf/detail?id=`、`/probes/detail?id=`、`/latency-probes/detail?id=&ip=`。
- `actions.ts`/`dns-actions.ts` 改为客户端函数；`api.ts` 使用同源 `/api` 与 `credentials: include`。
- 生产由 Rust `control` 直接托管 `out/`，不再启动 Node。

## C：Rust 接管控制面

Rust `backend/rust/control/src/rest.rs` 已原生接管：

- 认证：bcrypt 校验 `users`；Redis 会话 `ipguard:session:<hex>` 24 小时；HttpOnly/SameSite=Lax；登录限流 15 分钟 10 次；`/api/auth/login|logout|me`。
- 连接池：`deadpool-postgres 0.14` 复用 PostgreSQL 连接，`CONTROL_DB_POOL_SIZE` 默认 8；登录使用 `prepare_cached`，避免每请求握手。
- 配置 CRUD：节点列表/新增/修改/删除/批量导入，网段列表/新增/修改/删除、协议池恢复，黑名单列表/新增/删除。
- 聚合：证书异常、可用 IP（含 checks/speed/geo 聚合与链接生成）、IP 归属地批量查询。
- CF：检测目标 CRUD、共用池、扫描进度、启动/取消、按目标启动。
- 探针：CRUD、密钥轮换、部署配置、历史查询、`/api/probe-agent/targets|results|heartbeat`；AES-256-GCM 保存凭据，Bearer 只比对 SHA-256 哈希。
- DNS：配置读写、记录/候选/历史读取、计划入队；华为云凭据仅由 Go worker 在同步阶段读取。
- 扫描：生成 job JSON、Redis 进度/锁、`engine_jobs(kind='node_scan'|'cf_scan')` 入队；扫描记录与详情查询。

Go worker 只保留 mihomo 相关动作：

- `POST /available-ips/speed-test`
- `POST /networks/{id}/recheck`
- `GET /health`
- `engine_jobs` 消费和全部后台维护；DNS 实际同步仍由 worker 执行。

Rust 的 `fallback` 会把未原生实现的 `/api` 请求转发到 `WORKER_URL`。当前仅上述内部动作走这条路径；其余接口不应再依赖 Go 控制面。

数据库初始化不再需要独立 Go API：`ipguard-worker -init` 只执行 schema 迁移和管理员初始化后退出，不启动任何监听。发布包因此只包含 `bin/control` 与 `bin/ipguard-worker`。

## 内存

Windows 实测空闲：

- Rust `control`：WorkingSet 约 9.9–11.3 MB，Private 约 3.3 MB。
- 同机 Next standalone：WorkingSet 64.1 MB，Private 81.2 MB。
- Go worker 常驻主要用于 mihomo 与扫描期临时对象；扫描瓶颈是 RTT，CPU 占比通常低于 1%。

## 验证

```sh
npm run typecheck
npm run lint
npm run build
go -C backend build ./...
go -C backend vet ./...
go -C backend test ./internal/ipguard
node scripts/rust-build.mjs test -p control --release
node scripts/rust-build.mjs clippy -p control --release -- -D warnings
node scripts/rust-build.mjs build -p control --release
node scripts/rust-build.mjs build -p latency-probe --release
```

静态网关冒烟：

```sh
CONTROL_ADDR=127.0.0.1:18088 WEB_DIR=out WORKER_URL=http://127.0.0.1:8082 \
REDIS_URL=redis://localhost:6379/0 DATABASE_URL=postgresql://... ./backend/rust/target/release/control
curl -s http://127.0.0.1:18088/health
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18088/login/
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18088/api/auth/me   # 401
```

2026-11-21 本地验证结果：

| 检查 | 结果 |
| --- | --- |
| `npm run typecheck` | 通过 |
| `npm run lint` | 通过，0 warning |
| `npm run build` | 通过，17 个静态路由输出到 `out/` |
| `go -C backend build ./...` | 通过 |
| `go -C backend vet ./...` | 通过 |
| `node scripts/rust-build.mjs test -p control --release` | 5 passed |
| `node scripts/rust-build.mjs clippy -p control --release -- -D warnings` | 通过，0 warning |
| `npm run control:build` | 通过 |
| Rust SQL 校验（PGlite PostgreSQL 17 兼容内核） | 24 条代表性查询全部通过 |
| 网关冒烟 | `/health` 200、`/login/` 200、无会话 `me` 401、Redis 假会话 `me` 200 |
| 旧前端路径 | `/api/cf/scan`、`/api/cf/pool`、`/api/latency-probes/:id/history`、`/api/available-ips/locations` 均命中 Rust 原生处理器 |

Rust SQL 覆盖节点、网段、证书异常、可用 IP、归属地、DNS、探针列表/历史/目标、CF 目标/池、扫描记录/详情、`engine_jobs`。真实数据库端到端仍需在具备 PostgreSQL/Redis 的环境执行。

## 部署前必须配置

`/etc/ip-list.env` 增加：

```sh
CONTROL_INTERNAL_TOKEN=<openssl rand -hex 32>
```

`ip-list-worker`、`ip-list-web` 两个单元共享该文件。`ip-list-web` 的 `ExecStart` 指向 `bin/control`，`WEB_DIR=/opt/ip-list-current`；worker 的 `INTERNAL_ADDR` 固定为 `127.0.0.1:8082`。发布包不再包含 Node 运行时或 Go 控制面二进制。
