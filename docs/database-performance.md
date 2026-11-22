# 数据库性能检查（2026-10-28）

## 写入审计（2026-11-22）

- `ip_results` 是 `node_ip_pool` 的逐节点副本，运行时没有读取方；扫描结果改为只写共享池，schema 升级时回填旧库后删除该表。
- `ip_health_checks` 是逐次健康检查的追加日志，决策和页面只读 `ip_health_recovery` 的最新状态；删除写入、建表和索引，schema 升级时删除旧表。
- `engine_jobs` 完成/失败记录保留 7 天用于故障诊断，之后由 6 小时一次的维护任务清理。
- `dns_sync_runs` 历史只保留摘要；最新一条 `sync` 快照保留 `records`（用于匹配已应用的 DNS 记录），但移除 `candidates`，其余历史行的 `records`/`candidates` 全部移除。`dns_settings.report` 继续保留完整计划供 records/candidates 分页。
- `latency_probe_results_probe_ip_time_idx` 被 `(probe_id,ip,mode,tcp_port,checked_at)` 的测量索引覆盖，删除以减少最高写入量表的索引维护成本。
- `node_scan_rounds`、`cf_target_rounds`、`ip_health_recovery`、`node_ip_pool`、`cf_results` 仍被列表、详情、决策或统计读取，保留。

## 宕机恢复及慢查询复查（2026-11-20）

恢复阶段先保障 DNS、心跳和接口：清理、探针压缩、归属补全和历史证书复检
使用最多一个连接的独立维护池，分别延后 5、1、3、10 分钟启动；
维护连接设置 `lock_timeout=2s`、`statement_timeout=120s`。
这是数据库连接隔离，仍共享主机的 CPU、磁盘和数据库锁。

网段总体进度不再对每个网段执行 `UNION` 去重，改为节点池计数加上
不在节点池中的黑名单计数。IPv4、IPv6、重叠项及排队复检语义保持不变。
同一进程的并发轮询共用一次计算，继续缓存 10 秒；调用者取消只停止自己的等待，
共享计算最多运行 10 秒。不同 API 实例仍各自计算。

在生产 189 个启用网段上，交替执行两轮 `EXPLAIN (ANALYZE, BUFFERS)`：
旧查询 578 / 569 ms，新查询 317 / 271 ms，均无 shared reads。
这只是当时数据和缓存条件下的观测，不是接口延迟承诺。

事故中应用记录的 `commit` 超时约 60 秒，PostgreSQL 对应提交约 95 秒；
同时存在 journald watchdog 超时。这说明需要继续定位数据库和主机停顿，
现有证据不能确定是磁盘、锁还是宿主机调度问题。正常节奏的长 checkpoint
写入阶段也不能单独证明磁盘故障。

### 持续诊断

发布包包含独立的 `ip-list-db-diagnostics.service`。部署时校验并原子安装
Python 脚本到 `/usr/local/sbin/ip-list-db-diagnostics`，以 postgres 用户通过
本机 peer 认证访问 `ipguard`；不读取应用凭据。每 5 秒采样一次，查询最多
运行 2 秒，子进程最多等待 4 秒。正常时每分钟记录一次；数据库操作超过
2 秒、采样间隔超过 10 秒或采样失败时立即记录。瞬时等待可能被采样遗漏。

```sh
journalctl -u ip-list-db-diagnostics --since '1 hour ago' --no-pager
journalctl -u ip-list-worker --since '1 hour ago' --no-pager | rg '数据库查询|数据库连接等待'
```

诊断 JSON 含 PID、事务和查询时长、wait event、阻塞 PID，以及 `/proc` 的
CPU ticks（第 8 项为 steal）、I/O pressure、diskstats 和 load；计数器是累计值，
分析时比较相邻采样。没有 SQL 正文、参数和连接错误详情；操作只区分
COMMIT、ROLLBACK 和普通 statement。应用慢日志的 PID 可与同一时段采样关联。
服务自身不写数据库，也不使用应用连接池。主机整体停顿时它同样无法采样，
恢复后的间隔告警只能证明采样中断，不能直接证明宿主机故障。

生产开启以下可热加载设置，先备份 `postgresql.auto.conf` 再执行：

```sql
ALTER SYSTEM SET track_io_timing = on;
ALTER SYSTEM SET track_wal_io_timing = on;
ALTER SYSTEM SET log_lock_waits = on;
SELECT pg_reload_conf();
```

这些是集群级设置，增加计时和锁等待日志开销；未关闭同步提交，未扩大前台连接池。
PostgreSQL 自身的锁等待日志可能含 SQL，按数据库日志现有权限管理。
检查 `pg_stat_io`、`pg_stat_wal` 的写入／同步计时，以及 PostgreSQL 17 的
`pg_stat_checkpointer`；不能把累计时间当作一次事件的耗时。
回退时恢复备份的设置并 reload；回退到不含诊断脚本的旧版本前，停止并禁用
诊断服务。监控安装失败会在发布日志告警，不触发应用回滚，须单独检查服务状态。

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
新数据库初始化通过 `backend/internal/ipguard/schema.sql` 建立相同索引。

最新探针结果仍按 `checked_at DESC, id DESC` 排序，限定最近 30 天；
不改变离线探针、无采样、检测失败、ICMP/TCP 模式隔离及历史采样的业务含义。

## 检查后保留的部分

- DNS 候选及故障佐证主查询实测约 31 ms；保留现有探针掉线保护和多目标佐证规则。
- 探针上传时过期历史匹配查询走时间索引，约 0.08 ms；没有为当前规模引入额外清理任务。
- 主要表的 autovacuum / autoanalyze 在运行，死元组未显示积压；没有执行 VACUUM FULL。
- 没有持续锁等待或主机资源饱和证据。连接池保持现有配置，避免在小内存主机盲目扩容。
- DNS 的会话咨询锁执行期间占用一个连接，仍需结合连接等待日志判断是否需要独立连接池。

## 后续慢查询定位

API 与 Worker 记录超过 `DB_SLOW_QUERY_THRESHOLD` 的 SQL 和连接获取操作，默认 `500ms`；
失败操作无论耗时都记录。配置值使用 Go duration，如 `1s`，必须大于零。

```sh
journalctl -u ip-list-worker --since '1 hour ago' --no-pager | rg '数据库查询|数据库连接等待'
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
