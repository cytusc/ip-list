# IP Guard

基于 **Next.js + shadcn/ui、Go、PostgreSQL、Redis** 的 IP 过滤配置管理系统。

目前实现管理员登录/退出、节点增删改查、网段增删改查、搜索和状态筛选。网段支持 IPv4/IPv6 CIDR、所属节点、允许/阻止策略和启停。删除节点前必须移除或迁移其网段。

已接入 mihomo v1.19.33 内核，用于批量筛选可用于代理节点的 IP。尚未实现规则下发或流量过滤，节点启用状态不表示在线状态。

## 筛选可用 IP

1. 在节点页选择「导入 VLESS 节点」，每行一条链接，支持 HTTPUpgrade、XHTTP、gRPC、WS 和 TCP。
2. 配置要测试的 IPv4/IPv6 网段。
3. 打开「连通性测试」，选择节点和网段，开始测试。系统逐个替换节点服务器地址为网段 IP，保留端口、UUID、SNI、Host 和传输参数。
4. 通过 mihomo 代理访问检测网址并返回延迟即视为可用，不额外限制 HTTP 状态码；可按延迟查看结果并复制可用 IP。连接失败或超时记为失败。

默认并发 5、单 IP 超时 5 秒；单次最多 4096 个去重 IP，覆盖所选网段全部地址，不做抽样。支持停止和刷新恢复，任务最长 30 分钟，结果保存在 Redis 中 24 小时。网段的允许/阻止策略不影响测试。这里测量连通延迟，不测下载带宽。

检测任务只解析和校验一次节点链接，各 IP 使用独立代理实例。工作协程按实际 IP 数量限制，结果采用有界缓冲和预分配内存。Redis 每秒检查停止请求并续租；进度变化时保存快照，无变化时每 10 秒更新心跳，结束时保存最终结果。

已有安装更新后执行 `npm run db:init`，增加节点链接字段并调整唯一约束，不需要新增数据表。

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
- `backend/schema.sql`：仅 `users`、`nodes`、`networks` 三张表。节点名称、手动节点地址端口、同一节点的 CIDR 均有唯一约束；导入节点允许同一地址端口使用不同传输。
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
