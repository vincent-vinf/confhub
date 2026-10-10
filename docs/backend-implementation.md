# 后端实现与验证

范围依据 [requirements.md](requirements.md)、[architecture.md](architecture.md) 与 docs/adr 下的架构决策。本次实现 Go 服务端及必要的单元、集成测试；React 页面与客户端 SDK 已完成；不执行压测。

## 已确认的测试边界

- 配置管理/灰度解析公共接口：版本、目标、回退、格式与乐观锁行为。
- 真实 PostgreSQL 存储公共接口：事务、迁移、持久日志和清理行为。MySQL 保留实现支持，按用户要求不运行其集成测试。
- HTTP/WebSocket 公共协议：认证、跨实例推送、订阅恢复、删除及同名重建。

测试检查调用者可观察的结果；不 mock 内部方法，不依赖内部表查询来判断业务成功。慢查询、连接失败及 dirty 状态仅在外部数据库边界注入。

## 完成核对

| 需求 / 交付 | 实现证据 | 验证证据 |
| --- | --- | --- |
| Gin 后端与 flag/env 启动 | `cmd/main`、`internal/settings`、`internal/server` | Go 构建/vet、三实例 Compose 启动、CLI flag 覆盖 env |
| Namespace / Group / 配置名与默认组织 | 迁移、组织与配置管理 API | 非空组织删除拒绝、配置读写测试 |
| 递增不可变版本、描述、原文无变化不发布 | 事务 Save、Version、History | 保存/读取/历史/no-op 测试 |
| 全量主版本与配置唯一 beta | Save、SetRules、Resolve | 全量保存不影响 beta；灰度原位覆盖、编号不变、无历史 |
| 灰度 AND、eq/in、首条命中、排序/停用/删除/试算 | Resolve、完整规则列表替换、simulate API | 首条匹配、缺失标签、停用回落、条件校验测试 |
| 回退与提升新版本，保留其他目标 | CopyVersion、版本操作/来源记录 | 全量回退保持 beta、灰度回退拒绝、唯一 beta 转全量及主历史测试 |
| 统一乐观锁与删除重建身份隔离 | 配置 UUID 和 revision 校验 | 两编辑者竞争、旧身份请求拒绝、HTTP 409 |
| 历史数量、引用保护、短期维护租约、日志水位 | Cleanup、维护 worker | 近期主版本保留、beta 复制来源主历史可清理且 beta 保留、双租约拒绝 |
| PostgreSQL / MySQL 与 golang-migrate | GORM 方言驱动、两套迁移 SQL、独立迁移池、migrate 子命令 | PostgreSQL 全部集成；幂等迁移、业务池存活、dirty 拒绝；MySQL 仅静态审查 |
| admin 首次初始化、纯 JWT、密码修改、Cookie 来源保护 | InitializeAdmin、认证与密码 API | 重启不覆盖密码、新密码登录/旧密码拒绝、旧 JWT 保持有效、非法 JWT 和跨来源请求拒绝 |
| 客户端匿名 HTTP GET 与完整 WebSocket 推送 | client config/watch API | 未登录读取、创建前缺失、发布、删除、同名重建推送 |
| 独立广播消费、有界缓存/待发送状态、长连接容量限制 | Hub、stateCache、Session | 两实例消费、实际 HTTP 在 A 发布经 B WebSocket 接收；灰度无影响不通知、订阅上限与释放容量 |
| 订阅竞态、故障退出就绪、恢复与日志缺口补偿 | 快照流序号、独立 watchdog、补偿消费 | 迟到初始读取不覆盖新快照、数据库错误/黑洞、连接关闭、恢复获取最新状态、日志过期补偿 |
| 心跳、超时与慢连接关闭 | WebSocket 10s Ping、30s 静默、5s 写超时；到期 context 优先 | 已过期读取 deadline 不被待发送数据覆盖；代码审查确认独立心跳时钟 |
| 操作日志、静态目录和错误路由 | JSON slog 操作日志、OpenRoot 静态服务、健康接口 | Compose 操作日志、静态页面回退与 API/资源 404 测试 |
| Docker / Compose 单实例与三实例 | Dockerfile、compose.yaml、compose.mysql.yaml | 镜像构建、两个 Compose 配置校验、PostgreSQL 三实例健康与功能检查 |
| 单元和必要集成测试、代码审查、提交 | 27 个顶层测试及子用例、[backend-review.md](backend-review.md) | 完整 race 测试通过，审查缺陷修复后复查无阻断问题 |

SDK 负责采集/覆盖 sys.ip、sys.hostname、自定义标签保留名检查、地址切换、磁盘缓存及 Python asyncio；本次后端接受标签并独立记录连接源地址。前端负责编辑 diff、格式高亮、格式化和用户确认；后端提供不可变历史、目标状态、确认字段和并发校验。

## 已执行验证

2026-10-09：

- `make build`、`go build ./...`、`go vet ./...`、`git diff --check` 通过。
- 使用用户指定镜像 `registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17`，所有真实数据库测试使用独立临时 schema。
- 完整 `go test -mod=readonly -race -count=1 -cover ./...` 通过，没有数据竞争报告。宿主机为 Go 1.27.1、关闭 CGO 且没有 gcc，因此完整 race 检查在 `registry.cn-hangzhou.aliyuncs.com/bodesi/golang:1.25`（Go 1.25.14，CGO=1）中执行，连接同一 PostgreSQL 测试实例。
- 包覆盖率：config 77.9%，server 58.2%，storage 64.3%，syncer 83.9%。覆盖率不能替代上表的行为证据；启动与参数由部署功能检查验证。
- 最终二进制 CLI 冒烟验证通过：flag 覆盖 DATABASE/DSN/LISTEN 环境变量、就绪 200、WebSocket 握手 101、默认 JSON 日志记录连接来源地址、SIGINT 优雅退出。
- Docker 镜像构建成功；PostgreSQL 和 MySQL Compose 配置校验通过。
- 三应用实例加 PostgreSQL 的临时 Compose 项目启动成功，三个 `/health/ready` 均为 200。实际 HTTP 检查验证共享管理 JWT、跨实例缓存收敛、全量发布保持灰度固定版本、永久删除。
- 未执行 MySQL 集成测试、压测或性能/RSS 测量，不把资源或 P99 目标视为已验证。

## 代码审查修复

初审发现 MySQL INSERT 语法错误、查询超时导致就绪失效滞后、持续待发送消息可能挤占心跳。全部修复，两个审查轴复查均无剩余阻断问题。保留一项非阻断设计建议：以后可将 CopyVersion 的位置参数整理为带显式操作类型的请求结构。

## 灰度模型调整（2026-10-10）

见配置级唯一 beta 规范及 ADR 0007：schema 4 的 config_beta 每配置一份可修改正文；规则仅保存元数据。首次创建规则在事务内从所选主版本复制，随后 beta 编辑只覆盖当前内容和修订；转全量不选择规则，全量回退保持 beta。删除最后一条规则清除 beta，关闭保留。旧 schema 1/2/3 拒绝兼容，使用空数据库重建。

通知及 Go/Python SDK 去重增加规则身份、正文及格式，既保证同名 beta 更新可见，又抑制无关规则、全量及描述变化。测试使用指定 PostgreSQL 镜像和隔离数据库；MySQL 仅维护相应 SQL，未运行真实 MySQL 测试。

## IP 区间与在线连接（2026-10-10）

新增 `ip_range` 并复用到配置读取、模拟与 WebSocket 路由。`net/netip` 校验完整起止地址、同族及数值顺序，包含端点；IPv4 映射 IPv6 统一处理，zone 不接受。

presence worker 和配置同步 worker 独立运行。成功升级后记录临时连接身份，成功发送后更新无正文的订阅信息。共享数据库 lease、fingerprint 和标签索引支持多实例只读查询；只有变化客户端写入，插入/删除使用有大小上限的批次。过期查询过滤与后台物理清理分开，不参与配置 change stream 或 mutation 锁。当前 schema 4 包含连接表；旧 schema 1/2/3 均拒绝兼容。

验证覆盖真实 PostgreSQL 的事务、lease 过期、断连删除、去重/字面前缀/大小写/空格、建议上限及内置名称保留、旧 schema 拒绝；HTTP/WebSocket 覆盖管理员权限、跨实例显示、版本更新、取消订阅及断连。指定 PostgreSQL 17 镜像用于所有数据库验证。MySQL 只提供迁移和适配器，按已确认边界不运行 MySQL 集成测试。

审查补充：PostgreSQL 标签索引使用 BYTEA、MySQL 使用 VARBINARY，以保留合法 JSON 标签中的 NUL、大小写与空格；prefix 以二进制 LIKE 的字面转义查询，JSON 快照用 utf8mb4 存储。真实 PostgreSQL 回归验证特殊标签与普通连接可同时展示并查询，不能使实例续租失败。

此次全量回归：`python3 sdk/test-integration.py --full` 通过（Go/Python 双实例 SDK、服务端 PostgreSQL race 与 vet）；审查修正后 presence/suggestions/迁移与跨实例客户端接口的针对性 race 回归通过。未执行压测。

## 配置级唯一 beta 验证（2026-10-10）

`python3 sdk/test-integration.py --full` 通过：真实 PostgreSQL 的完整后端 race/vet、Go SDK 与 Python 10 条测试、双实例同名 beta 更新和离线缓存。前端 25 个单测及全部 18 条 Playwright 流程通过，包含置顶编辑、旧主版本复制、多规则共享、删除最后规则后重新复制，以及已有冲突、回退、IP 区间和在线客户端行为。TypeScript、Prettier、mypy、ruff、SDK vet 与 diff 检查通过。MySQL 未运行真实数据库测试，不执行压测。

审查发现规则冲突恢复时 beta 可能已被其他管理员删除。新增浏览器回归先复现失败，再修正为保留编辑草稿并重选来源；元数据操作明确终止。该回归及两个受影响流程通过（共验证 19 条不同浏览器流程），修正后 TypeScript 通过。


## GORM 存储重构（2026-10-10）

应用常规 CRUD、联表查询、批量写入、Upsert 和行锁使用 GORM 1.31.1，PostgreSQL/MySQL 方言驱动为 1.6.0。公开 Store 接口及业务模型保持原有契约，存储行模型单独映射 schema 4；不使用 AutoMigrate，不增加数据库表或变更迁移文件。golang-migrate 继续负责空库初始化、显式升级、dirty 检查及独立迁移连接。

全局序号计数行的排他锁、读已提交变更事务、可重复读快照、维护租约和在线客户端事务边界保持原有设计。时间字段显式保留微秒整数，编号禁止自动递增；更新使用 map 或明确字段，保留空字符串、0 和 false。在线客户端继续按连接数及字节数分批写入，稳定快照只续租。

仅数据库时钟和 BYTEA/VARBINARY 前缀条件保留方言表达式，集中在 dialect.go。占位符转换和手写 Upsert 已移除，使用 GORM TranslateError 映射常见约束错误，再转成公开业务错误。ORM 查询日志关闭，不记录配置正文或密码哈希。当前显式模型足够小，不引入额外 Gen 生成流程。

新增公共存储接口回归验证清空主/beta 正文与描述，以及在线客户端后续批次失败后回滚已写入批次；使用 Clients/Version/Snapshot/TagSuggestions 检查结果，不读取内部表作为业务断言。

验证：PostgreSQL 后端 race、迁移、SDK、三实例 32 写入者/100 客户端/10 配置/10 轮并发、随机及六项故障恢复通过，覆盖率门槛通过，后端 83.78%。前端 44 个单测通过；修复浏览器测试辅助函数的下拉关闭/焦点等待后，全部 21 个浏览器流程通过。`make build`、vet、mypy、ruff、类型及格式检查通过。原失败与后续回归记录分别保留，详见 [测试运行指南](testing.md#gorm-重构验证2026-10-10)。按已确认边界未运行真实 MySQL 或压测。
