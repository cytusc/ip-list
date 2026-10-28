# 数据库性能检查（2026-10-28）

生产 PostgreSQL 17，主机 2 核、约 2 GiB 内存。以下是实际数据上的
`EXPLAIN (ANALYZE, BUFFERS)` 单次观测，并非持续压测或延迟承诺。

## 已处理的瓶颈

| 查询 | 优化前 | 优化后 | 原因及处理 |
| --- | --- | --- | --- |
| 可用 IP 页的完整探针查询 | 10,656 ms，约 314 万次 shared buffer hits | 33 ms，约 1,754 次 buffer accesses | 全局 DISTINCT ON 被嵌套循环重复执行 50 次；改为按探针、IP、模式及端口索引查询，LIMIT 1 |
| 节点池 CIDR 查询（返回 2,048 行） | 526 ms，约 23,933 次 buffer accesses | 35 ms，约 1,716 次 buffer accesses（缓存命中） | 原先并行扫描全部 623,025 行；增加 GiST inet_ops 网段索引 |
| CF 池 CIDR 查询（返回 2,048 行） | 未保留同条件基线 | 2.6 ms，43 次 buffer accesses | 增加 GiST inet_ops 网段索引 |

网段查询首次使用新索引、发生磁盘读取时约 456 ms，随后缓存命中约 35 ms；
前后缓存条件不同，不能把时间差全部归因于索引。确定的改善是避免全表扫描并降低读取量。
节点池、CF 池新增索引分别约 18 MiB、5.6 MiB，会增加写入及存储成本。
两个索引已在生产使用 `CREATE INDEX CONCURRENTLY` 建立，避免普通建索引阻塞写入。
新数据库初始化通过 `backend/schema.sql` 建立相同索引。

最新探针结果仍按 `checked_at DESC, id DESC` 排序，限定最近 30 天；
不改变离线探针、无采样、检测失败、ICMP/TCP 模式隔离及历史采样的业务含义。

## 检查后保留的部分

- DNS 候选及故障佐证主查询实测约 31 ms；保留现有探针掉线保护和多目标佐证规则。
- 探针上传时过期历史匹配查询走时间索引，约 0.08 ms；没有为当前规模引入额外清理任务。
- 主要表的 autovacuum / autoanalyze 在运行，死元组未显示积压；没有执行 VACUUM FULL。
- 没有持续锁等待或主机资源饱和证据。连接池保持现有配置，避免在小内存主机盲目扩容。
- DNS 的会话咨询锁执行期间占用一个连接，仍需结合连接等待日志判断是否需要独立连接池。

## 后续慢查询定位

API 记录超过 `DB_SLOW_QUERY_THRESHOLD` 的 SQL 和连接获取操作，默认 `500ms`；
失败操作无论耗时都记录。配置值使用 Go duration，如 `1s`，必须大于零。

```sh
journalctl -u ip-list-api --since '1 hour ago' --no-pager | rg '数据库查询|数据库连接等待'
```

查询日志包含规范化 SQL 的 SHA-256 前 16 个十六进制字符、路由、耗时及错误类别；
连接等待日志还包含当前占用连接数、总数和上限。后台任务标记为 `background`。
日志不记录 SQL 正文、绑定参数、错误详情或请求查询参数，避免泄露节点链接和凭据。
QueryTracer 耗时包含结果读取时间；连接池排队耗时单独记录。

本次没有启用需重启 PostgreSQL 的 `pg_stat_statements`，因此不能提供历史 SQL
累计耗时排行榜。排查依据是现有超时日志、执行计划及系统统计。

部署前使用真实管理员登录态请求，`/networks`、`/nodes`、`/latency-probes`、
`/dns` 分别约 26、6、10、8 ms，而 `/available-ips` 约 9,278 ms；
这说明当时的主要瓶颈在完整可用 IP 查询，而不是所有配置接口或 Redis 全面不可用。

## 验证

回归覆盖最新失败覆盖旧成功、相同时间按 ID 决胜、过期记录、检测模式隔离、
探针掉线保护、DNS 记录保护、增量池隔离，以及慢日志参数脱敏与连接池耗尽超时诊断。
真实数据库测试使用独立 `_test` 数据库，Redis 测试使用独立实例。

`go test -race -count=1 -skip 'TestNetworkMetadataLookup/partial' ./...`
在配置独立 PostgreSQL / Redis 后通过 167 项测试，`go vet ./...` 通过。
未排除该子测试的运行会复现既有的 `wrong resource` 断言：其 mock 仅接受网段首地址，
而实现会继续查询下一子网地址。本次未修改该测试或元数据查询逻辑。
