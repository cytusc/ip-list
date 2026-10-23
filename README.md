# IP Guard

基于 **Next.js + shadcn/ui、Go、PostgreSQL、Redis** 的 IP 过滤配置管理系统。

目前实现管理员登录/退出、节点增删改查、网段增删改查、搜索和状态筛选。网段支持 IPv4/IPv6 CIDR、所属节点、允许/阻止策略和启停。删除节点前必须移除或迁移其网段。

当前版本管理配置，尚未接入 mihomo 内核、下发规则或实际过滤流量。节点启用状态不表示在线状态。

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
- `backend/schema.sql`：仅 `users`、`nodes`、`networks` 三张表。节点名称、节点地址端口、同一节点的 CIDR 均有唯一约束。
- pgx：PostgreSQL 连接池和参数化查询。
- go-redis：Redis 连接；SCS + goredisstore：24 小时登录会话，登录时轮换令牌，退出时删除会话。
- ulule/limiter：Redis 登录限流，同一用户名每 15 分钟最多尝试 10 次，成功后重置。
- validator：通用字段校验；Go `net/netip`：CIDR 解析和网络边界校验。
- bcrypt：密码哈希。

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
| GET          | `/health`        | PostgreSQL、Redis 连通性 |

除登录和健康检查外均需登录；写请求需要 `Origin` 与 `APP_ORIGIN` 一致。错误返回 `error`，校验错误另有 `fields`。尚未定义重叠网段的规则优先级，待 mihomo 接入时统一实现。

## 检查

```sh
npm run lint
npm run typecheck
npm run build
npm test
go -C backend vet ./...
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

测试覆盖登录失败和限流、会话退出、节点/网段增删改、IPv4/IPv6 边界、重复网段、关联删除保护和移动端导航。浏览器测试会创建临时配置，正常结束后清理。

## 生产构建

```sh
npm run api:build
npm run build
# 使用进程管理器分别运行，提供正确的环境变量
./bin/ipguard
npm start
```
