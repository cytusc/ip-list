# IP Guard

基于 **Next.js + shadcn/ui、Go、PostgreSQL、Redis** 的 IP 过滤配置管理系统。

目前实现管理员登录/退出、节点增删改查、网段增删改查、搜索和状态筛选。网段支持 IPv4/IPv6 CIDR、启停、ASN 和服务商信息。网段独立维护，节点测试和 CF 检测复用同一网段池，运行任务时选择对应网段。

已接入 mihomo v1.19.33 内核，用于批量筛选可用于代理节点的 IP。尚未实现规则下发或流量过滤，节点启用状态不表示在线状态。

## 筛选可用 IP

1. 在节点页选择「导入 VLESS 节点」，每行一条链接，支持 HTTPUpgrade、XHTTP、gRPC、WS 和 TCP。
2. 配置要测试的 IPv4/IPv6 网段。
3. 打开「连通性测试」，选择节点和网段，开始测试。系统逐个替换节点服务器地址为网段 IP，保留端口、UUID、SNI、Host 和传输参数。
4. 通过 mihomo 代理访问检测网址并返回延迟即视为可用，不额外限制 HTTP 状态码；可按延迟查看结果并复制可用 IP。连接失败或超时记为失败。

默认并发 5、单 IP 超时 5 秒；单次最多 4096 个去重 IP，覆盖所选网段全部地址，不做抽样。支持停止和刷新恢复，每轮最长 30 分钟，结果保存在 Redis 中 24 小时。这里测量连通延迟，不测下载带宽。

检测任务只解析和校验一次节点链接，各 IP 使用独立代理实例。工作协程按实际 IP 数量限制，结果采用有界缓冲和预分配内存。Redis 每秒检查停止请求并续租；进度变化时保存快照，无变化时每 10 秒更新心跳，结束时保存最终结果。

已有安装更新后执行 `npm run db:init`，保留已有配置并创建可用 IP 结果表。

## 可用 IP 列表

在「可用 IP」按协议与传输方式查看共用 IP 池，例如 `VLESS / XHTTP`、`VLESS / gRPC`、`VLESS / HTTPUpgrade` 各自独立。同池所有已启用节点均可直接生成节点链接，新增节点无需重新扫描。检测延迟来自最近一次成功检测的样本节点。支持 IP / ASN / 服务商搜索、协议筛选、分页和复制，页面每 10 秒刷新。

每轮结束后将各协议/IP 的最新状态保存到 PostgreSQL；复测失败仅移除对应池中的 IP，未检测地址保留原状态。停止时只保存已完成结果。修改、停用或删除样本节点不删除共用池；生成链接时使用当前启用节点的配置，保留认证、端口、SNI、Host 和传输参数。

列表通过 IPPure 基础信息接口异步查询当前页 IP 的归属地，展示国家或地区、国旗及省市信息。成功结果在 Redis 缓存 24 小时，失败缓存 1 分钟；查询失败显示「未知归属地」，不影响列表和节点链接。

## 自动巡检

在「连通性测试」选择样本节点与网段，默认间隔 5 分钟、每 12 轮全量扫描，点击「开始巡检」后由后台持续执行。不同协议池独立运行，可切换测试节点配置其他池，也可以选择单次检测。

页面按**检测记录**展示：每轮检测保存为一条记录，列出节点与协议池、轮次与范围、开始时间、可用 / 已测、可用率、耗时与状态；点开记录可查看该轮逐 IP 的结果、延迟与首次可用 / 恢复 / 失效变化，并可复制可用 IP。正在运行的一轮在列表中实时更新进度，巡检等待下一轮时顶部横幅显示下轮时间。记录保留最近 7 天，逐 IP 明细只为每池最近 30 轮保留，更早的记录仅保留汇总。

- 首轮扫描全部所选网段，日常轮次只复测可用 IP；第 13、25…轮重新全量扫描。全量频率为 1 时每轮扫描全部地址。
- 只有整段完整扫描且全部失败时，才在对应协议池中自动禁用网段。取消、中断或仅复测可用 IP 不触发整段禁用；其他协议和 CF 不受影响。
- 页面展示当前池禁用的空网段，可点击「恢复扫描」。后台后续轮次重新读取网段状态；修改 CIDR 后旧禁用状态失效。
- 升级初始化时，会一次性检查历史池：整段地址均有检测结果且无可用 IP 的，补齐对应协议池的禁用记录。手动恢复后再次初始化不会重新应用历史禁用；网段列表可查看并筛选「协议池已禁用」。
- 每池每轮结束后等待指定间隔，不重叠执行、不补跑积压轮次。同一协议池共用任务锁，不同池可独立巡检。
- 巡检配置和进度快照持久化到 PostgreSQL，离开页面继续运行，服务重启后自动恢复。原样本已删除或停用时，恢复过程选用同池其他已启用节点。显式停止会关闭该池的自动恢复。
- 页面显示轮次、范围、下轮时间及首次可用 / 恢复 / 失效变化。所有网段被禁用后等待恢复，仍可停止巡检。

`POST /scans` 的 `interval_minutes` 为 0 时单次检测，否则范围 1–1440；`full_every` 范围 1–288。`GET /scans/latest?protocol=vless%2Fxhttp` 获取对应池的最新任务，`GET /scan-records` 返回按轮次倒序的检测记录与仍在进行（或等待下一轮）的任务，`GET /scan-records/{id}` 返回单轮记录及该轮结果。`DELETE /protocol-networks/{id}?protocol=vless%2Fxhttp` 恢复该池中的网段。升级后运行 `npm run db:init`，创建共用池、协议网段状态、巡检配置与检测记录表，合并原有结果并保留最新状态。

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
| GET          | `/scan-records` | 按轮次的检测记录列表与进行中的任务 |
| GET          | `/scan-records/{id}` | 单轮检测记录及该轮结果 |
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

测试覆盖登录失败和限流、会话退出、节点/网段增删改、IPv4/IPv6 边界、重复网段、独立网段与节点删除和移动端导航。另覆盖真实本地 VLESS HTTPUpgrade、XHTTP、gRPC 传输、替换 IP、延迟返回、HTTP 非 2xx 连通、批量导入、任务互斥、取消和超时。浏览器测试包含导入、测试、刷新恢复和停止，会创建临时配置并清理。

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

在「CF 优选」配置多个 HTTPS 检测域名（例如 `https://sub.bycn.de/`）。通过列表顶部「网段与检测配置」统一选择检测域名、已启用网段、超时与并发数，执行检测，结果汇入所有域名共用的 IP 池；新增域名直接复用现有 IP，无需重新检测，删除域名也不会删除共用 IP。

域名列表按表格展示可用 IP、上轮可用率、最后检测时间、添加时间，并提供「查看 IP」入口；添加域名在弹窗中完成。统一检测任务只有一个，进度单独放在「检测进度」页（`/cf/progress`），展示状态、进度、统计、配置摘要、本轮共用 IP 池与停止操作，启动检测后自动跳转到该页。域名详情页（`/cf/[id]`）只展示该域名自己的数据：可用 IP、上轮与累计可用率、检测轮次、最近 12 轮记录，以及该域名实际返回 2xx/3xx 的 IP 列表；共用池中由其他域名测出的 IP 不计入该域名列表。

该功能与代理节点检测独立，不需要 VLESS 节点模板；复用网段配置，不受 4096 IP 数量限制，重叠网段自动去重，按需生成待测地址，最多 32 并发，支持停止和刷新恢复进度。任务不设总运行时限，持续到全部地址检测完成或手动停止；单个 IP 仍按配置的请求超时执行。升级时执行 `npm run db:init`，原有各域名的结果按 IP 合并，采用最新检测状态。

进度页展示的任务按以下顺序确定：先取最近一次启动的检测；若它所属的域名已被删除，则回退到最近一次已完成检测（Redis 列表 `ipfilter:cf:done` 保留最近 5 条），仍无可用任务时显示空态。运行中的任务超过 50 秒没有心跳会标记为中断，避免服务重启后一直停在「检测中」。

检测使用 Go 标准库直接连接候选 IP，保留网址原始 Host、TLS SNI 和证书校验，不使用环境 HTTP 代理。HTTP 2xx / 3xx 计为可用，不跟随跳转；403、5xx、证书错误或超时均不计为可用。仅检测可用性，不额外判断 Cloudflare 响应头，不测量延迟或带宽。

每轮结束后，同一批结果写入三处：共用 IP 池（`cf_ip_pool`）、该域名的独立结果（`cf_results`）与轮次统计（`cf_target_rounds`），并更新 `cf_targets.last_checked_at`。因此域名详情既能列出「该域名返回的 IP」，也能按轮次给出可用率；旧数据没有轮次记录时，可用率回退为按域名结果计算。PostgreSQL 保留每个地址/IP 的最新检测结果；复测失败会移除可用记录，未检测或停止时尚未完成的 IP 保留旧结果。Redis 保存 24 小时的任务进度，服务中断需重新开始检测。升级后运行 `npm run db:init` 创建 `cf_targets`、`cf_results`、`cf_target_rounds` 表并回填 `last_checked_at`，再重启 Go API。

网段的 ASN 和服务商留空时会在保存时自动获取（RIPEstat 路由 ASN 与所属机构，Redis 缓存 24 小时），也可手动填写。查询失败不影响保存，并提示重试；修改 CIDR 会清除旧归属并重新查询。私有网段不查询，多 ASN 或查询结果无法覆盖整个网段时不自动填充。归属信息会展示在代理可用 IP 和 CF 地址详情中；重叠网段优先采用有信息的最具体网段。可用 IP 列表可生成对应协议池中所有已启用节点的分享链接，保留认证、SNI 和传输参数，仅替换连接 IP。

相邻网段按各自 CIDR 独立保存，检测时只做重叠去重，不会把两个 `/23` 自动并成一个 `/22`；需要合并时先确认 SQL 里网段、协议池禁用行与历史记录三处都改到位，否则禁用状态会按 `(network_id, cidr)` 失配而失效。

升级运行后端 `-init` 会将旧网段迁移为独立配置，移除节点关联和访问策略；相同 CIDR 合并，保留备注和补充信息，任一副本启用则保留启用状态。若原任务选择了合并删除的网段，需重新选择。

本机启用 Clash 等 TUN 代理时，代理节点扫描可能经过已有代理，不能据此保证本地直连可用。可设置 `PROBE_INTERFACE=en0`（替换为实际物理网卡名）并重启 Go API，使用 mihomo 的网卡绑定功能指定检测出口；再重新扫描以更新旧结果。该配置不改变 CF 检测出口。
