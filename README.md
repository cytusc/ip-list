# IP Guard

基于 **Next.js + shadcn/ui、Go、PostgreSQL、Redis** 的 IP 过滤配置管理系统。

目前实现管理员登录/退出、节点增删改查、网段增删改查、搜索和状态筛选。网段支持 IPv4/IPv6 CIDR、所属节点、允许/阻止策略和启停。删除节点前必须移除或迁移其网段。

已接入 mihomo v1.19.33 内核，用于批量筛选可用于代理节点的 IP。尚未实现规则下发或流量过滤，节点启用状态不表示在线状态。

## 筛选可用 IP

1. 在节点页选择「导入 VLESS 节点」，每行一条链接，支持 HTTPUpgrade、XHTTP、gRPC、WS 和 TCP。
2. 配置要测试的 IPv4/IPv6 网段。
3. 打开「连通性测试」，选择节点和网段，开始测试。系统逐个替换节点服务器地址为网段 IP，保留端口、UUID、SNI、Host 和传输参数。
4. 通过 mihomo 代理访问检测网址并返回延迟即视为可用，不额外限制 HTTP 状态码；可按延迟查看结果并复制可用 IP。连接失败或超时记为失败。

默认并发 5、单 IP 超时 5 秒；单次最多 4096 个去重 IP，覆盖所选网段全部地址，不做抽样。支持停止和刷新恢复，每轮最长 30 分钟，结果保存在 Redis 中 24 小时。网段的允许/阻止策略不影响测试。这里测量连通延迟，不测下载带宽。

检测任务只解析和校验一次节点链接，各 IP 使用独立代理实例。工作协程按实际 IP 数量限制，结果采用有界缓冲和预分配内存。Redis 每秒检查停止请求并续租；进度变化时保存快照，无变化时每 10 秒更新心跳，结束时保存最终结果。

已有安装更新后执行 `npm run db:init`，保留已有配置并创建可用 IP 结果表。

## 可用 IP 列表

在「可用 IP」按 IP 合并查看各节点测通的传输协议、延迟、对应节点及最近测通时间。支持 IP/节点搜索、协议筛选、分页和复制筛选后的 IP；页面每 10 秒刷新，也可手动刷新。

每轮检测结束后，批量将每个节点/IP 的最近检测结果保存到 PostgreSQL，不受 Redis 任务快照 24 小时过期影响。后续复测失败时移除该节点对应的可用记录，其他节点测通的协议仍保留；未测试的协议不显示。停止时仅保存已经完成的结果，未完成的探测不改变旧记录。列表表示各节点配置下最近一次的检测结果，不表示持续在线，也不推断其他节点配置可用。

停用或删除节点后隐藏其结果，修改节点链接后旧结果失效，需重新测试。更新前的任务不会自动回填，需要重新检测。

## 自动巡检

在「连通性测试」选择巡检方式后点击「开始巡检」。推荐间隔 5 分钟、每隔 12 轮全量扫描；初始仍为单次检测，不会自动开启。

- 首轮扫描全部所选网段，日常轮次只复测之前可用的 IP；第 13、25…轮重新全量扫描，发现新增或恢复的 IP。全量频率设为 1 可每轮扫描全网段。
- 无可用 IP 时下一轮自动全量扫描；已失效 IP 会在后续全量扫描中重新检测。
- 每轮结束后等待指定间隔，不重叠执行，不补跑积压轮次。巡检期间占用现有单任务锁。
- 页面显示轮次、扫描范围、下次执行时间、首次可用/恢复/失效以及上次延迟。表格和复制仅包含当前轮已测结果，不将旧结果冒充当前可用状态。
- 可在测试或等待期间停止，刷新恢复配置和任务状态，离开页面仍继续运行。巡检使用启动时的节点与网段配置，修改配置后请停止并重新启动。
- 每轮使用相同的连通性标准，不增加评分、状态码限制或额外重试。服务重启后不会自行重启巡检，需要手动启动；停止后的最近结果保留 24 小时。

`POST /scans` 增加 `interval_minutes`（0 为单次，自动巡检范围 1–1440）和 `full_every`（1–288）。返回增加 `input`、`round`、`scope`、`started_at`、`finished_at` 和 `next_run_at`；`waiting` 表示等待下轮，可通过原取消接口停止。无需新增表或数据库迁移。

## 本地启动

需要 Node.js 22+、Go 1.26+、Docker Compose；也可以直接使用已有 PostgreSQL 17+ 和 Redis 7+。

```sh
cp .env.example .env
# 编辑 .env：设置 ADMIN_PASSWORD，至少 12 个字符，不超过 72 字节
docker compose up -d --wait
npm ci
npm run db:init
```

分别在两个终端启动后端、前端：

```sh
npm run api:dev
```

```sh
npm run dev
```

访问 http://localhost:3000，使用 `.env` 的 `ADMIN_USERNAME` 和 `ADMIN_PASSWORD` 登录。默认用户名为 `admin`，没有默认密码。重复执行初始化不会覆盖已有管理员密码。

Go 使用 godotenv 加载根目录 `.env`；Next.js 自动读取该文件。初始化后可从 `.env` 移除 `ADMIN_PASSWORD`，除非需要再次执行初始化。

## 结构和所用库

- `src/app`：Next.js 页面和服务端 API 代理。
- `src/components/ui`：shadcn/ui 原生组件，交互基于 Radix UI。
- `backend`：Go HTTP API，使用标准库路由。
- `backend/schema.sql`：`users`、`nodes`、`networks`、`ip_results` 四张表。`ip_results` 仅保留每个节点/IP 的最近检测结果；节点名称、手动节点地址端口、同一节点的 CIDR 均有唯一约束，导入节点允许同一地址端口使用不同传输。
- pgx：PostgreSQL 连接池和参数化查询。
- go-redis：Redis 连接；SCS + goredisstore：24 小时登录会话，登录时轮换令牌，退出时删除会话。
- ulule/limiter：Redis 登录限流，同一用户名每 15 分钟最多尝试 10 次，成功后重置。
- validator：通用字段校验；Go `net/netip`：CIDR 解析和网络边界校验。
- bcrypt：密码哈希。
- mihomo：使用现成的链接转换、代理传输和 URLTest；HTTPUpgrade 做兼容映射，不自行实现协议。

API 默认为 `127.0.0.1:8081`，浏览器通过 Next.js 服务端操作访问，Cookie 为 HttpOnly / SameSite=Lax。修改 `APP_ORIGIN` 时需要与前端访问来源一致。生产环境设置 `APP_ENV=production`、正确的 `APP_ORIGIN`，并通过 HTTPS 访问前端；后端保持仅内网可访问。Docker Compose 默认数据库密码仅用于本机开发。

## API

| 方法         | 路径             | 用途                     |
| ------------ | ---------------- | ------------------------ |
| POST         | `/auth/login`    | 用户名、密码登录         |
| POST         | `/auth/logout`   | 退出登录                 |
| GET          | `/auth/me`       | 当前管理员               |
| GET / POST   | `/nodes`         | 节点列表 / 新增          |
| PUT / DELETE | `/nodes/{id}`    | 编辑 / 删除节点          |
| GET / POST   | `/networks`      | 网段列表 / 新增          |
| PUT / DELETE | `/networks/{id}` | 编辑 / 删除网段          |
| POST         | `/nodes/import` | 批量导入 VLESS 链接 |
| POST         | `/scans` | 开始批量测试 |
| GET          | `/scans/latest` | 最近一次任务 |
| GET          | `/scans/{id}` | 进度与结果 |
| POST         | `/scans/{id}/cancel` | 停止测试 |
| GET          | `/available-ips` | 按 IP 汇总已测通协议、延迟及对应节点 |
| GET          | `/health`        | PostgreSQL、Redis 连通性 |

除登录和健康检查外均需登录；写请求需要 `Origin` 与 `APP_ORIGIN` 一致。错误返回 `error`，校验错误另有 `fields`。尚未定义重叠网段的规则优先级，将在实现流量过滤时处理。

## 检查

```sh
npm run lint
npm run typecheck
npm run build
npm test
go -C backend vet ./...
# 对比逐 IP 解析与任务复用配置的 CPU / 内存开销，不包含网络延迟
go -C backend test -run '^$' -bench BenchmarkProbeSetup -benchmem
```

真实 PostgreSQL / Redis 集成测试（数据库名必须以 `_test` 结尾）：

```sh
TEST_DATABASE_URL=postgresql://ipfilter:ipfilter_local@localhost:5432/ipguard_test \
TEST_REDIS_URL=redis://localhost:6379/15 npm test
```

浏览器端到端测试（先启动 Go API，使用当前 `.env` 的管理员账号）：

```sh
npx playwright install chromium
npm run test:e2e
```

测试覆盖登录失败和限流、会话退出、节点/网段增删改、IPv4/IPv6 边界、重复网段、关联删除保护和移动端导航。另覆盖真实本地 VLESS HTTPUpgrade、XHTTP、gRPC 传输、替换 IP、延迟返回、HTTP 非 2xx 连通、批量导入、任务互斥、取消和超时。浏览器测试包含导入、测试、刷新恢复和停止，会创建临时配置并清理。

## 生产构建

```sh
npm run api:build
npm run build
# 使用进程管理器分别运行，提供正确的环境变量
./bin/ipguard
npm start
```

## 参考项目

- [ShellCrash](https://github.com/juewuy/ShellCrash/blob/master/scripts/menus/8_tools.sh)：经代理访问目标并测量耗时。
- [zashboard](https://github.com/Zephyruso/zashboard/blob/main/src/api/clash.ts)：mihomo delay 测试；参考其 `src/assembly/proxies/latency.ts` 的有限并发和进度反馈。
- [mihomo v1.19.33](https://github.com/MetaCubeX/mihomo/tree/v1.19.33)：内嵌 Go 内核，采用其 GPL-3.0 许可。

### CF 优选

在「CF 优选」添加 HTTPS 目标地址（例如 `https://sub.bycn.de/`），进入地址详情选择已启用的网段并开始检测。该功能与代理节点检测独立，不需要 VLESS 节点模板；复用网段配置，每轮最多 4096 个去重 IP、32 并发，支持停止和刷新恢复进度。

检测使用 Go 标准库直接连接候选 IP，保留网址原始 Host、TLS SNI 和证书校验，不使用环境 HTTP 代理。HTTP 2xx / 3xx 计为可用，不跟随跳转；403、5xx、证书错误或超时均不计为可用。仅检测可用性，不额外判断 Cloudflare 响应头，不测量延迟或带宽。

每个地址的详情页按 IP 地址排序展示可用 IP、HTTP 状态与检测时间，支持搜索和复制。PostgreSQL 保留每个地址/IP 的最新检测结果；复测失败会移除可用记录，未检测或停止时尚未完成的 IP 保留旧结果。Redis 保存 24 小时的任务进度，服务中断需重新开始检测。升级后运行 `npm run db:init` 创建 `cf_targets`、`cf_results` 表，再重启 Go API。
