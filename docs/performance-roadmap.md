# 性能与低配置路线

目标：**保留旧 Go 控制面全部功能**，在不牺牲行为等价的前提下降低延迟、内存和部署配置要求。

## 当前生产基线（2026-11-23）

| 指标 | 数值 |
| --- | --- |
| 生产主机 | 2 vCPU / 2 GB RAM（Intel Xeon Platinum 8260） |
| `ip-list-web` RSS | 约 268 MB（压力后峰值约 516 MB） |
| `ip-list-worker` RSS | 约 97 MB |
| `/health` | ~2 ms |
| `/api/lists/available-ips` | ~60–90 ms |
| `/api/lists/networks` | 首次 ~2.3 s，热态 ~140 ms |
| `/api/lists/scan-records` | 优化前 ~1.3–2.2 s；去掉列表中的 `results` 大字段后 SQL 约 8 ms |

## 已完成的优化

1. `scan-records` 列表不再返回每条记录的完整 `results` 数组，只返回摘要；旧 Go 列表本来就不返回该字段。该字段只在记录详情中读取。
2. `scan-records` 的 `network_progress` 改成 10 秒进程内缓存，避免每 2 秒轮询都执行重查询。
3. Rust release 构建从 `opt-level="s"` 改为 `opt-level=3`、`lto="fat"`、`codegen-units=1`；CI 增加 `target-cpu=x86-64-v2`，让编译器生成更宽的 SSE/POPCNT 指令，同时保持 x86_64 兼容性。
4. `available-ips` 的 DNS 状态、筛选、排序、测速门槛已按旧 Go 行为补齐；分页列表契约已统一。

## 关于“汇编”

整个 Web 控制面**不建议**手写成汇编：

- 当前端点耗时主要由 PostgreSQL 查询、Redis、网络 RTT 决定，不是 CPU 指令吞吐；
- 手写汇编会失去 Rust 的内存安全、错误处理和可维护性，回归风险远大于收益；
- 真正有效的是“编译器生成更好的汇编”：`target-cpu`、LTO、PGO/BOLT、`opt-level=3`，以及对实测热点做局部 SIMD/内联汇编。

只有在 profiling 证明某个纯计算热点占主导时，才对该函数做汇编级优化；目前没有这样的热点。若目标是“把整个系统改用汇编语言重写”，需要先明确这是否是硬性约束，因为它会改变项目的风险模型和交付周期。

## 下一阶段

1. **性能回归**：部署后测 `scan-records`、`available-ips`、`networks`、`certificate-issues` 的 p50/p95，对比本基线。
2. **内存目标**：先观察优化后 RSS；若仍偏高，再评估 `mimalloc`、`MALLOC_ARENA_MAX`、静态链接等方案，而不是直接上汇编。
3. **配置收敛**：列出真正必要的环境变量；能默认的默认，能合并的合并。手工测速/网段重检目前仍依赖 Go worker，若要去掉这一层，需要单独评估把 gRPC 测速和重检迁到 Rust。
4. **验收**：每次改动先跑 `scripts/parity-check.py`，再跑性能基线；功能等价不通过，不进入性能比较。

## 性能预算（建议）

| 端点 | p95 目标 |
| --- | --- |
| `/health` | < 20 ms |
| `/api/lists/available-ips` | < 150 ms |
| `/api/lists/scan-records` | < 300 ms（热缓存） |
| `/api/lists/networks` | < 300 ms（热态） |
| 控制面 RSS | 先降到 < 200 MB，再评估是否可到 < 120 MB |

这些目标用于防止“优化”变成没有度量标准的猜测。
