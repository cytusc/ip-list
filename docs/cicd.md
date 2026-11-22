# GitHub Actions 构建与生产部署

推送到 `main` 后，[Build and deploy](https://github.com/cytusc/ip-list/actions/workflows/cicd.yml) 自动检查、构建、发布到生产服务器。也可以在 Actions 页面选择 Run workflow 手动运行。其他分支的手动运行只构建，不部署。

## 构建产物

流水线在 Ubuntu 24.04 x64 上运行前端 lint、类型检查、Go 单元测试和 vet、Rust 控制面测试，再构建 Go worker、Rust 控制面、六个平台的延迟探针和 Next.js 静态导出。依赖真实数据库的测试需要额外的测试环境变量，默认不运行这部分集成测试。

发布包包含 `out/` 静态页面、Rust 控制面、Go worker、探针下载程序和安装脚本，不再包含 Node.js 或 Go 控制面。不会包含 `.env`。Actions 的 Artifacts 中保留发布包及 SHA-256 校验文件 30 天，文件名包含完整提交 SHA。

服务器只接收、校验和解压发布包，不执行 `npm install`、`next build`、`go build` 或 `cargo build`。

## GitHub 配置

仓库 Settings → Secrets and variables → Actions 中配置以下 secrets：

| 名称 | 内容 |
| --- | --- |
| `DEPLOY_HOST` | 生产服务器地址 |
| `DEPLOY_USER` | 当前部署脚本需要 root |
| `DEPLOY_PORT` | SSH 端口，默认 22 |
| `DEPLOY_SSH_KEY` | 专用 SSH 私钥，公钥写入服务器 authorized_keys |
| `DEPLOY_KNOWN_HOSTS` | 已核对的服务器 SSH 主机公钥记录 |

仓库变量 `PRODUCTION_URL` 设置生产站点地址。生产部署使用 `production` environment；可以在该环境中添加审批或分支限制。

## 服务器约定

部署脚本适用于现有 Linux x64 + systemd 安装：

- `ipguard` 用户和组已存在。
- `ip-list-worker.service`、`ip-list-web.service` 由发布包安装/启用；worker 单元随包提供，旧的 `ip-list-api.service` 会被停止并禁用。
- `/etc/ip-list.env` 保存运行配置和密钥，包括 `DATABASE_URL`、`REDIS_URL`、`APP_ORIGIN`、`APP_ENV=production`、固定的 `PROBE_CREDENTIAL_KEY`、`PROBE_TLS_PIN`（自签证书时）、`CONTROL_INTERNAL_TOKEN`（`openssl rand -hex 32`）和可选的 `CONTROL_DB_POOL_SIZE`（默认 8）。
- 如果旧环境文件仍设置 `API_URL=http://127.0.0.1:8081`，请改为 `http://127.0.0.1:3000/api`，或删除该变量使用默认值；否则夜间扫描会指向已停用的 Go API。
- `CONTROL_INTERNAL_TOKEN` 必须同时提供给 Rust 控制面和 Go worker；Go 只信任携带该值的内部请求，浏览器 Cookie 不再传给 Go。
- 数据库升级使用 `ipguard-worker -init`，一次性执行后退出；环境文件仍需提供有效 `ADMIN_USERNAME`、`ADMIN_PASSWORD`，已有管理员密码不会被覆盖。
- 初始化/迁移默认允许 5 分钟，可在环境文件设置 `DB_INIT_TIMEOUT=10m` 调整；worker 普通启动仍使用 10 秒连接超时。
- PostgreSQL 在本机运行，可通过 `postgres` 系统用户执行 `pg_dump`。
- 已安装 `curl`、Python 3、PostgreSQL 客户端和 `flock`。
- Go worker 监听 `127.0.0.1:8082`，Rust 控制面监听 `127.0.0.1:3000`，Nginx 提供外部 HTTPS。Go worker 不暴露公网端口。

既有 systemd 的 `EnvironmentFile` 和 Xray 配置继续生效。部署通过 `release.conf` drop-in 指定发布目录及启动命令。

## 发布和恢复

1. 发布包传到 `/var/tmp/ip-list-cicd/<run-id>-<attempt>/` 并校验 SHA-256。
2. 新版解压到 `/opt/ip-list-releases/<commit-sha>/`。
3. 对比发布包的数据库指纹（`backend/internal/ipguard/schema.sql`、`web.go` 和 `bootstrap.go`）。变化时才备份，PostgreSQL 备份保存到 `/opt/ip-list-backups/cicd/deploy-<time>-<id>/database.dump`。
4. 停止 Web、worker 和旧的 API；数据库指纹变化时，用新 worker 的 `-init` 执行初始化/迁移；指纹相同则跳过备份和迁移。旧发布包没有指纹时，首次升级仍执行一次备份和迁移。
5. 原子更新 `/opt/ip-list-current` 链接，重启 worker 和 Web 两个服务。
6. 检查服务状态、worker `/health` 和控制面 `/health`、`/login/`。失败时自动恢复之前的链接和 systemd drop-in，并启动旧应用。

首次部署前的 `/opt/ip-list` 源码安装保留；第一次部署失败可直接恢复原服务。自动恢复只切回应用版本，**不会自动还原数据库**。数据库变更需要兼容旧版应用；不兼容的迁移应安排维护和人工恢复。数据库备份包含敏感数据，目录仅允许 root 访问。

每次部署前后自动清理：

- 发布目录只保留 `/opt/ip-list-current` 和 `/opt/ip-list-previous` 指向的当前及上一版；失败的新版本在恢复旧版后删除。
- CI/CD 数据库备份只保留最近 3 份完整备份，残缺备份自动删除；其他目录中的历史备份不受此规则影响。
- 上传目录在流水线成功或失败退出时删除；取消运行遗留的上传包和解压目录超过 24 小时后，在下次部署时清理。

发布串行执行，服务器另用 `flock` 防止重复部署。服务器采用 systemd 运行预构建产物，不产生 Docker 镜像或容器。

需要手动部署或回退到保留的构建时，从对应 Actions 运行下载 artifact，解压其中的 tar.gz 和 sha256 到同一目录，在服务器运行：

```sh
bash scripts/deploy-release.sh <完整提交SHA> /path/to/ip-list-<完整提交SHA>.tar.gz
```

`deploy-release.sh` 和同目录的 `cleanup-deployments.py` 必须一起获取；发布包的 `scripts/` 中包含两者。手动回退会比较数据库指纹，变化时执行初始化/迁移，需先确认版本兼容。

查看当前版本及运行状态：

```sh
cat /opt/ip-list-current/REVISION
systemctl status ip-list-worker ip-list-web
journalctl -u ip-list-worker -u ip-list-web -f
```
