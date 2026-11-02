# GitHub Actions 构建与生产部署

推送到 `main` 后，[Build and deploy](https://github.com/cytusc/ip-list/actions/workflows/cicd.yml) 自动检查、构建、发布到生产服务器。也可以在 Actions 页面选择 Run workflow 手动运行。其他分支的手动运行只构建，不部署。

## 构建产物

流水线在 Ubuntu 24.04 x64 上运行前端 lint、类型检查、Go 单元测试和 vet，再构建 Go API、六个平台的延迟探针和 Next.js standalone 服务。依赖真实数据库的测试需要额外的测试环境变量，默认不运行这部分集成测试。

发布包包含运行所需的 Node.js 22、Web 服务依赖、静态资源、Go 二进制、探针下载程序和安装脚本。不会包含 `.env`。Actions 的 Artifacts 中保留发布包及 SHA-256 校验文件 30 天，文件名包含完整提交 SHA。

服务器只接收、校验和解压发布包，不执行 `npm install`、`next build` 或 `go build`。

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
- `ip-list-api.service`、`ip-list-web.service` 已存在。
- `/etc/ip-list.env` 保存运行配置和密钥，包括 `DATABASE_URL`、`REDIS_URL`、`APP_ORIGIN`、`APP_ENV=production`、固定的 `PROBE_CREDENTIAL_KEY`。
- 数据库升级使用 API 的 `-init`，因此环境文件需提供有效 `ADMIN_USERNAME`、`ADMIN_PASSWORD`；已有管理员密码不会被覆盖。
- 初始化/迁移默认允许 5 分钟，可在环境文件设置 `DB_INIT_TIMEOUT=10m` 调整；普通 API 启动仍使用 10 秒连接超时。
- PostgreSQL 在本机运行，可通过 `postgres` 系统用户执行 `pg_dump`。
- 已安装 `curl`、Python 3、PostgreSQL 客户端和 `flock`。
- Go API 监听 `127.0.0.1:8081`，Web 监听 `127.0.0.1:3000`，Nginx 提供外部 HTTPS。

既有 systemd 的 `EnvironmentFile` 和 Xray 配置继续生效。部署通过 `release.conf` drop-in 指定发布目录及启动命令。

## 发布和恢复

1. 发布包传到 `/var/tmp/ip-list-cicd/<run-id>-<attempt>/` 并校验 SHA-256。
2. 新版解压到 `/opt/ip-list-releases/<commit-sha>/`。
3. PostgreSQL 备份保存到 `/opt/ip-list-backups/cicd/deploy-<time>-<id>/database.dump`。
4. 停止 Web 和 API，用新 API 执行数据库初始化/迁移。
5. 原子更新 `/opt/ip-list-current` 链接，重启两个服务。
6. 检查服务状态、API `/health` 和 Web `/login`。失败时自动恢复之前的链接和 systemd drop-in，并启动旧应用。

首次部署前的 `/opt/ip-list` 源码安装保留；第一次部署失败可直接恢复原服务。自动恢复只切回应用版本，**不会自动还原数据库**。数据库变更需要兼容旧版应用；不兼容的迁移应安排维护和人工恢复。数据库备份包含敏感数据，目录仅允许 root 访问，请按磁盘空间和备份策略定期归档。

成功部署后保留最近三个发布目录（当前及上一次版本一定保留）。数据库备份不自动删除。发布串行执行，服务器另用 `flock` 防止重复部署。

需要手动部署或回退到保留的构建时，从对应 Actions 运行下载 artifact，解压其中的 tar.gz 和 sha256 到同一目录，在服务器运行：

```sh
bash scripts/deploy-release.sh <完整提交SHA> /path/to/ip-list-<完整提交SHA>.tar.gz
```

脚本可从对应版本的仓库获取，发布包的 `scripts/` 中也包含一份。手动回退同样执行数据库初始化/迁移，需先确认版本兼容。

查看当前版本及运行状态：

```sh
cat /opt/ip-list-current/REVISION
systemctl status ip-list-api ip-list-web
journalctl -u ip-list-api -u ip-list-web -f
```
