# ConfHub

轻量配置中心，使用 Go/Gin、React、HTTP GET 与 WebSocket。支持不可变版本、全量/灰度发布、回退、乐观锁、单 admin JWT 登录，以及 PostgreSQL/MySQL 存储。

需求与架构见 [docs/requirements.md](docs/requirements.md) 和 [docs/architecture.md](docs/architecture.md)，接口见 [docs/backend-api.md](docs/backend-api.md)，控制台交互和验证见 [docs/frontend-implementation.md](docs/frontend-implementation.md)。Go/Python SDK 尚未实现。性能目标尚未压测。

## 启动

先创建可连接的空数据库。首次启动自动执行初始迁移并创建 admin；之后不会用启动密码覆盖已保存密码。

```sh
export CONFHUB_DSN='postgres://confhub:password@localhost:5432/confhub?sslmode=disable'
export CONFHUB_ADMIN_PASSWORD='initial-password'
export CONFHUB_JWT_SECRET='replace-with-a-random-secret-of-at-least-32-bytes'
make frontend-install frontend-build build
./bin/confhub --static-dir frontend/dist
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
cp .env.example .env
# 编辑 .env，替换数据库密码、admin 初始密码和 JWT 密钥。
# 每个值可分别用 openssl rand -hex 32 生成。
docker compose up -d --build
# 三个应用实例，共享同一数据库和 JWT 密钥：
docker compose --profile ha up -d --build
```

Compose 自动读取根目录 `.env`，并将其中的应用参数传入各实例；已导出的同名环境变量优先于 `.env` 中用于 Compose 插值的值。数据库类型和 DSN 由所选 Compose 文件生成。真实 `.env` 已被 Git 和 Docker 构建上下文忽略，只提交 `.env.example`。

默认端口 8080、8081、8082，可通过 `CONFHUB_PORT/PORT2/PORT3` 修改。数据库使用持久卷；单个数据库容器不提供数据库 HA。默认 PostgreSQL 镜像为 `registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17`。模板将数据库密码放入 DSN，使用不包含 URI 保留字符的密码；自定义密码可调整模板中的 DSN 并进行 URI 编码。

MySQL 使用 `docker compose -f compose.mysql.yaml …`，在 `.env` 中额外设置 `CONFHUB_DB_ROOT_PASSWORD`。本阶段按用户要求只运行 PostgreSQL 集成测试，MySQL 迁移/适配器尚未通过真实 MySQL 测试。

Docker 多阶段构建使用 Node 24 构建 React，将产物复制到镜像的 `/app/frontend`，由 Go 服务读取，未嵌入二进制。无需提前在本机生成 `frontend/dist`；运行时无需 Node 或第二个前端 HTTP 服务。

## 控制台开发

使用 Node 24 和 npm；依赖版本固定在 `frontend/package-lock.json`。

```sh
make frontend-install
# 先按启动步骤运行 Go 后端；开发时 Vite 将 API 和健康检查代理到它：
npm --prefix frontend run dev
# 后端不是默认 8080 时：
CONFHUB_DEV_BACKEND=http://127.0.0.1:8081 npm --prefix frontend run dev
```

浏览器访问 `http://127.0.0.1:5173`。同源认证代理保留浏览器 Host，不开启 `changeOrigin`。登录凭据保存在 HttpOnly Cookie 中；localStorage 只保存主题偏好。

控制台包含配置列表、内容编辑、发布前 diff/确认、历史查看/比较/回退、灰度规则及标签试算、命名空间/分组管理和密码修改。支持明暗主题、移动导航、JSON/YAML 可撤销格式化，以及七种配置格式高亮。

## 测试

```sh
make test-unit
# PostgreSQL 集成测试使用独立临时 schema，要求测试账户具有 CREATE SCHEMA 权限：
export CONFHUB_TEST_POSTGRES_DSN='postgres://confhub:password@127.0.0.1:15432/confhub?sslmode=disable'
make test-integration
make test-race
make vet
# 前端：
make frontend-check
npm --prefix frontend run format:check
npm --prefix frontend exec playwright install --with-deps chromium
make frontend-e2e
```

race 检查要求 CGO 和 C 编译器，`make test-race` 会显式启用 CGO；完整测试已在上述 Go 1.25 构建镜像中验证。

`make test` 执行全部测试；未设置测试 DSN 时数据库测试明确 skip。`make test-integration` 和 `make test-race` 要求 DSN 存在，防止误把跳过集成测试当作通过。测试数据库 DSN 使用 PostgreSQL URL 格式。

测试覆盖配置保存与固定灰度、回退、并发冲突、删除重建、组织约束、历史引用与日志清理，以及 HTTP 认证/确认和 WebSocket 跨实例同步、数据库故障和日志缺口补偿。没有压测脚本或负载容器。

前端端到端测试需要 Docker、Go、Node 和 Chromium，自动构建前端及 Go 服务，使用指定 PostgreSQL 17 镜像创建临时数据库，在 `127.0.0.1:18080` 测试真实 HTTP/数据库流程。数据库使用随机宿主端口，正常退出时删除测试容器；测试报告、截图及失败 trace 保存在被 Git 忽略的 `frontend/playwright-report` 和 `frontend/test-results`。Linux 截图环境需安装中文字体（例如 `fonts-noto-cjk`）。
