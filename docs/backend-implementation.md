# 后端实现与验证

范围依据 [requirements.md](requirements.md)、[architecture.md](architecture.md) 与五份 ADR。本次实现 Go 服务端及必要的单元、集成测试；React 页面、客户端 SDK 和压测留在后续阶段。

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
| 全量与灰度独立目标 | Save、SetRules、Resolve | 全量保存不影响固定灰度；灰度编辑只移动规则目标 |
| 灰度 AND、eq/in、首条命中、排序/停用/删除/试算 | Resolve、完整规则列表替换、simulate API | 首条匹配、缺失标签、停用回落、条件校验测试 |
| 回退与提升新版本，保留其他目标 | CopyVersion、版本操作/来源记录 | 全量回退、灰度回退、提升及历史引用测试 |
| 统一乐观锁与删除重建身份隔离 | 配置 UUID 和 revision 校验 | 两编辑者竞争、旧身份请求拒绝、HTTP 409 |
| 历史数量、引用保护、短期维护租约、日志水位 | Cleanup、维护 worker | 近期与被引用版本保留、无引用旧版本删除、双租约拒绝 |
| PostgreSQL / MySQL 与 golang-migrate | 两套 SQL、驱动适配器、独立迁移池、migrate 子命令 | PostgreSQL 全部集成；幂等迁移、业务池存活、dirty 拒绝；MySQL 仅静态审查 |
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
