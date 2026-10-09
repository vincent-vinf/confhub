# ConfHub

轻量配置中心后端，使用 Go/Gin、HTTP GET 与 WebSocket。支持不可变版本、全量/灰度发布、回退、乐观锁、单 admin JWT 登录，以及 PostgreSQL/MySQL 存储。

需求与架构见 [docs/requirements.md](docs/requirements.md) 和 [docs/architecture.md](docs/architecture.md)，接口见 [docs/backend-api.md](docs/backend-api.md)。当前交付为后端；React 页面和 Go/Python SDK 尚未实现。性能目标尚未压测。

## 启动

先创建可连接的空数据库。首次启动自动执行初始迁移并创建 admin；之后不会用启动密码覆盖已保存密码。

```sh
export CONFHUB_DSN='postgres://confhub:password@localhost:5432/confhub?sslmode=disable'
export CONFHUB_ADMIN_PASSWORD='initial-password'
export CONFHUB_JWT_SECRET='replace-with-a-random-secret-of-at-least-32-bytes'
make build
./bin/confhub
```

已有库升级需先停止旧版本或安排兼容升级，再运行同一二进制：

```sh
./bin/confhub migrate
```

使用 golang-migrate 的锁、版本和 dirty 管理；dirty 状态拒绝启动，不自动 Force。服务不会创建数据库或数据库账户。

## 参数

flag 优先于同名环境变量。不读取启动配置文件。轮询间隔至少 1ms，同步失败时限至少 2ms；轮询间隔不能超过失败时限的一半，以保证故障检测时限。

| flag | 环境变量（前缀 `CONFHUB_`） | 默认值 |
| --- | --- | --- |
| `--listen` | `LISTEN` | `:8080` |
| `--database` | `DATABASE` | `postgres`（或 `mysql`） |
| `--dsn` | `DSN` | 必填；MySQL 使用 `user:password@tcp(host:3306)/database` |
| `--admin-password` | `ADMIN_PASSWORD` | 首次初始化必填，8–72 字节 |
| `--jwt-secret` | `JWT_SECRET` | 服务启动必填，至少 32 字节，所有实例相同 |
| `--jwt-expiry` | `JWT_EXPIRY` | `2h` |
| `--cookie-secure` | `COOKIE_SECURE` | `false`，HTTPS 部署设置 true |
| `--static-dir` | `STATIC_DIR` | `/app/frontend` |
| `--poll-interval` | `POLL_INTERVAL` | `100ms` |
| `--sync-failure-timeout` | `SYNC_FAILURE_TIMEOUT` | `2s` |
| `--cache-bytes` | `CACHE_BYTES` | `67108864`（64 MiB） |
| `--history-limit` | `HISTORY_LIMIT` | `100`，旧引用版本额外保留 |
| `--cleanup-interval` | `CLEANUP_INTERVAL` | `1m`，每次最多清理 256 条历史/事件 |
| `--event-retention` | `EVENT_RETENTION` | `24h` |

## Docker Compose

```sh
export CONFHUB_DB_PASSWORD='replaceWithDatabasePassword'
export CONFHUB_ADMIN_PASSWORD='replace-with-admin-password'
export CONFHUB_JWT_SECRET='replace-with-a-random-secret-of-at-least-32-bytes'
docker compose up -d --build
# 三个应用实例，共享同一数据库和 JWT 密钥：
docker compose --profile ha up -d --build
```

默认端口 8080、8081、8082，可通过 `CONFHUB_PORT/PORT2/PORT3` 修改。数据库使用持久卷；单个数据库容器不提供数据库 HA。默认 PostgreSQL 镜像为 `registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17`。模板将数据库密码放入 DSN，使用不包含 URI 保留字符的密码；自定义密码可调整模板中的 DSN 并进行 URI 编码。

MySQL 使用 `docker compose -f compose.mysql.yaml …`，额外设置 `CONFHUB_DB_ROOT_PASSWORD`。本阶段按用户要求只运行 PostgreSQL 集成测试，MySQL 迁移/适配器尚未通过真实 MySQL 测试。

Docker 构建会将可选 `frontend/dist` 目录复制到镜像的 `/app/frontend`，由 Go 服务读取，未嵌入二进制。后端阶段目录为空，API 和健康接口仍正常运行。

## 测试

```sh
make test-unit
# PostgreSQL 集成测试使用独立临时 schema，要求测试账户具有 CREATE SCHEMA 权限：
export CONFHUB_TEST_POSTGRES_DSN='postgres://confhub:password@127.0.0.1:15432/confhub?sslmode=disable'
make test-integration
make test-race
make vet
```

race 检查要求 CGO 和 C 编译器，`make test-race` 会显式启用 CGO；完整测试已在上述 Go 1.25 构建镜像中验证。

`make test` 执行全部测试；未设置测试 DSN 时数据库测试明确 skip。`make test-integration` 和 `make test-race` 要求 DSN 存在，防止误把跳过集成测试当作通过。测试数据库 DSN 使用 PostgreSQL URL 格式。

测试覆盖配置保存与固定灰度、回退、并发冲突、删除重建、组织约束、历史引用与日志清理，以及 HTTP 认证/确认和 WebSocket 跨实例同步、数据库故障和日志缺口补偿。没有压测脚本或负载容器。
