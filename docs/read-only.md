# 只读运行模式

通过 `--read-only` 或 `CONFHUB_READ_ONLY=true` 启用，默认关闭。适用于连接数据库副本的配置读取节点；数据复制由 MySQL 主从、Canal 等外部设施负责，ConfHub 不创建复制链路。

## 行为

- 保留控制台登录、配置与完整主版本历史查看、版本 diff、beta/规则查看和灰度试算，原有用户权限范围继续生效。
- 保留匿名 HTTP GET 和 WebSocket 订阅，按本地数据库中的规则及客户端标签计算生效配置。控制台显示“只读模式”，隐藏管理写入口，正文不可编辑，配置详情每 5 秒刷新。
- 管理 API 拒绝配置发布、beta 修改、回退、转全量、规则修改、批量移动/删除、组织和用户管理写入、密码修改等请求，返回 `403`。登录、退出和灰度试算的 POST 请求仍可用。
- 不初始化数据库、迁移表结构或创建管理员，只查询 schema 版本；缺少迁移标记、版本不一致或 dirty 时启动失败。`migrate` 与只读模式不能同时启用。
- 不执行历史/事件清理、维护租约、在线客户端采集或数据库审计写入。`/api/admin/clients`、`/api/admin/client-tags` 返回 `403`，控制台隐藏在线客户端入口；管理员仍可查询数据库中已有的审计记录，本节点的登录、读取和试算不会新增审计记录。
- 使用副本中已有的用户、密码和授权，不需要设置初始 admin 密码。只读与普通模式使用不同名称的登录 Cookie，避免同一主机不同端口间覆盖会话。

应用级只读限制在 HTTP 与存储接口同时实施。建议另外为实例使用只有 SELECT 权限的数据库账号，复制进程使用自己的独立账号。

## 直接启动

在可写端初始化、迁移数据库并创建用户，将所需表完整复制到副本后启动：

```sh
export CONFHUB_DATABASE=mysql
export CONFHUB_DSN='confhub_reader:password@tcp(replica:3306)/confhub'
export CONFHUB_JWT_SECRET='replace-with-a-random-secret-of-at-least-32-bytes'
./confhub --read-only
# 等价：CONFHUB_READ_ONLY=true ./confhub
```

`--read-only=false` 可覆盖环境中的 `CONFHUB_READ_ONLY=true`。需要修改配置或用户时访问可写端；授权、密码变更和账号禁用到达副本后，在本节点的后续请求中生效。

## Docker Compose

复制 `.env.example` 为 `.env`，设置 `CONFHUB_DATABASE`、`CONFHUB_DSN` 和 `CONFHUB_JWT_SECRET`。DSN 指向外部已初始化的数据库副本：

```sh
docker compose -f compose.readonly.yaml up -d --build
# 可选：三个只读实例，共用该副本。
docker compose -f compose.readonly.yaml --profile ha up -d --build
```

此模板只启动 ConfHub，不创建数据库。应用默认限制为 1 CPU/1 GiB，端口与资源参数沿用 `.env.example`。

## 数据库复制要求

需要同步 `namespaces`、`config_groups`、`configs`、`config_versions`、`config_beta`、`gray_rules`、`change_stream`、`change_events`、`users`、`user_grants` 和 schema 版本标记；审计查询还依赖 `audit_logs`。保持与可写端相同的表结构，首次复制应建立一致快照，后续保留源事务原子性、提交顺序、删除和清理水位。配置与变更事件分开落库可能导致缓存或订阅错过变更。

只读实例沿用现有变更流轮询及通知机制，无需可写端额外发送应用消息。延迟由数据库复制延迟、本地轮询和推送组成。复制中断而本地数据库仍可用时继续服务已有配置；恢复后消费事件或通过清理水位补偿。健康检查仅反映本地服务可用性，不代表复制已追平；复制延迟、源切换与重新初始化由外部设施监控和处理，不能在运行中将副本回退到更旧的事件水位。

## 验证范围

真实 PostgreSQL SELECT-only 账号验证进程启动、登录、权限范围、管理读接口、灰度试算、HTTP GET、主/beta WebSocket 更新、规则关闭和删除通知；存储测试验证管理及后台写入拒绝和 schema 校验。浏览器测试以可写/只读两个进程共享数据库，验证只读 UI、主/beta 刷新、历史、试算、Cookie 隔离及明暗窄屏可访问性。

测试入口为 `python3 tests/run.py --coverage-gates`，新增用例为 `cmd/main/readonly_integration_test.go`、`internal/storage/readonly_integration_test.go` 和 `frontend/e2e/readonly.spec.ts`。这些测试不替代真实 MySQL/Canal 主从复制验收；本阶段仍仅验收真实 PostgreSQL。

### 本次验证（2026-10-28）

- SELECT-only PostgreSQL 真实进程测试通过，包含登录、权限范围、外部禁用账号后会话失效、主/beta 读取与订阅、规则关闭、删除通知及无审计/后台写入。首次专项测试发现身份读取的共享行锁不适用于只读账号，改为只读一致性快照后通过；可写模式保留原有行锁。
- 完整运行器除浏览器层外的 16 层通过，包括数据库 race/vet、Go/Python SDK、32 写入者/100 订阅客户端系统回归、权限/审计/批量操作、六类故障及独立连接模式。
- 前端 50 个单元测试、类型和格式检查、生产构建通过。浏览器首次完整运行 29/30，新增只读测试因目标配置不在第一页超时；补充搜索定位后完整重跑，新增只读用例及另外 28 个场景通过。已有长标签键盘用例在重跑时出现一次选择断言失败，未修改该用例，单独复跑通过。保留失败记录，不将完整运行器报告改写为 17/17。
- 后续定位并修复标签自动补全的时序缺陷：前缀查询切换时短暂清空建议，导致方向键被忽略；列表位置变化也可能改变选中标签。修复前独立重复 12 次复现 3 次。补充 3 个组件回归测试后，53 个前端单测、类型/格式检查、生产构建、完整浏览器 30/30 及原键盘用例独立重复 12/12 全部通过。新增记录在 `test-results/readonly-20261028/keyboard-fix/`，原失败报告保留。
- 覆盖率门槛通过：后端 86.96%，业务规则 100%，Go SDK 87.95%，Python SDK 91.56%。Docker 镜像 `confhub:main-readonly` 构建、只读 Compose 参数解析通过；已检查 375px 深色截图，横屏和明暗主题 axe 检查通过。

报告位于忽略目录 `test-results/readonly-20261028/`：`full/layers.json`、`full/coverage-summary.json`、`browser-final.log`、`keyboard-recheck.log`、`image-build-final.log`，首次浏览器失败与重跑 trace 也保留于该目录。测试容器、角色、schema 和进程按各运行器清理，既有云边试用部署未变更。
