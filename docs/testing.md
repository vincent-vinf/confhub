# 测试运行指南

本文说明 [测试计划](testing-plan.md) 已落地的入口。测试依据公开业务和协议，不以覆盖率替代关键行为验证。PostgreSQL 是本阶段唯一真实数据库验收后端，不宣称 MySQL 或容量/资源指标已通过。

## 环境准备

需要 Go 1.25、C 编译器（race）、Docker、Python 3.10+、Node/npm；浏览器回归还需要 Chromium。数据库镜像固定为 `registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17`。

Python SDK 的 dev 依赖包含 coverage、mypy 和 ruff。使用已有 venv，或通过以下命令创建：

```sh
uv venv sdk/python/.venv
uv pip install --python sdk/python/.venv/bin/python -e 'sdk/python[dev]'
npm --prefix frontend ci
npm --prefix frontend exec playwright install chromium
```

已有 Python 环境也可使用 `pip install -e 'sdk/python[dev]'`，并通过运行器 `--python` 或 Makefile 的 `SDK_PYTHON` 指定解释器。缺少数据库、SDK 依赖或浏览器时，不将未执行测试视为通过。

## 分层入口

| 命令 | 范围 |
| --- | --- |
| `make test-unit` | 无数据库业务规则/启动参数，以及 Go/Python SDK 单元测试 |
| `make test-integration` | 存储、同步、HTTP 集成；要求显式 `CONFHUB_TEST_POSTGRES_DSN` |
| `make test-race` | 后端 race；要求显式测试 DSN |
| `make sdk-check` | Go SDK/系统程序 vet、Python mypy/ruff |
| `make test-fuzz` | IP 单点包含性质、名称/正文/标签健壮性，各 5 秒、2 个 worker |
| `npm --prefix frontend run test:coverage` | 前端逻辑单测及 V8 覆盖率 |
| `make test-system` | 对提供的运行中实例执行 Go 系统测试程序 |
| `make test-full` | 临时 PostgreSQL、三个真实服务、所有层及覆盖率门槛 |

服务端单元测试只包括无需数据库的边界；现有同步测试包含真实数据库场景，仍在集成层执行。`make sdk-unit` 的 Go 部分使用 `-short` 排除真实服务集成，Python 使用 `test_client.py`；完整 SDK 集成由隔离环境传入实际地址后运行。

## 隔离完整回归

```sh
python3 tests/run.py --coverage-gates
# 与 make test-full 等价。可固定报告目录，首次运行应使用空目录：
python3 tests/run.py --report-dir test-results/acceptance --coverage-gates
```

运行器创建随机名称和宿主端口的 PostgreSQL 容器，不挂载持久化部署目录。先初始化 schema，再并行启动三个带 race 和 coverage 的 Go 服务进程。各实例通过运行器拥有的独立 TCP 数据库代理连接同一数据库，以支持单节点隔离和数据库故障注入。测试数据使用唯一 namespace，正常及失败退出清理本次数据；脚本最后关闭进程/代理并删除测试容器。

这套环境验证真实进程和数据库链路，服务进程也受 race 检查。数据库实例、代理和故障控制器均为本次运行所有，不操作现有 Compose 服务。

默认执行 `unit,integration,system,frontend` 四层，系统套件执行 `business,protocol,concurrency,random,presence,faults`。默认并发为 32 写入者、100 订阅客户端、10 配置、10 轮，随机种子为 20261010。这是正确性回归，不是容量压测。

系统层之后还执行一次连接模式的 `business,protocol` 回归：仅传入实例地址和凭据，移除故障控制器环境变量，以验证同一 Go 程序可直接测试运行中的系统。该报告单独放在 `connected-system/`。

针对性运行：

```sh
python3 tests/run.py --layers unit
python3 tests/run.py --layers integration
python3 tests/run.py --layers system --scenarios business,protocol
python3 tests/run.py --layers system --scenarios concurrency,random \
  --writers 32 --clients 100 --configs 10 --rounds 10 --seed 7
python3 tests/run.py --layers system --scenarios faults
python3 tests/run.py --layers frontend
```

`--coverage-gates` 要求同时包含 integration 和 system 层。范围不完整的专项测试只报告自己实际执行的内容，不冒充完整验收。

## 测试运行中的系统

从仓库根目录执行：

```sh
export CONFHUB_SYSTEM_ADDRESSES='http://127.0.0.1:8080,http://127.0.0.1:8081,http://127.0.0.1:8082'
export CONFHUB_SYSTEM_PASSWORD='测试环境的管理员密码'
make test-system
unset CONFHUB_SYSTEM_PASSWORD
```

地址和密码也可由终端的已有环境提供，不需要写进配置文件。Go 程序默认使用两种 SDK 验证业务、协议、并发、随机操作和在线客户端；至少要求两个实例地址。跨实例目标通过选择不同实际端点实现，不以单实例结果代替。

自定义场景、规模、解释器及报告位置：

```sh
go -C tests/system run -race . \
  --scenarios business,concurrency,random \
  --writers 32 --clients 100 --configs 10 --rounds 10 \
  --seed 20261010 --timeout 35s \
  --python ../../sdk/python/.venv/bin/python \
  --python-worker ../python_worker.py \
  --report-dir ../../test-results/connected
```

程序仅通过管理 HTTP API 创建自己的 namespace/group/config，通过正式 Go SDK 及 Python SDK JSON-lines worker 获取和订阅。不引用服务端内部包、不读取数据库作为业务断言，也不修改全局密码或停止外部服务。连接模式默认不包含故障套件；强行选择 faults 而没有隔离控制器时，程序在操作数据前明确失败。

请使用测试部署或允许创建/删除测试数据的环境。中断或失败后的清理有独立结果，残留 namespace 可从报告定位；清理失败返回非零退出码。

## 场景与断言

- 业务：组织隔离、原文保真、跨实例发布、从旧主版本复制共享 beta、beta 连续覆盖、描述变化不推送、规则重排/停用/重开、全量回退、转全量、历史分页、IP 边界、删除重建及旧基准拒绝。
- 协议：认证、来源、确认字段、格式、退役字段、分页、未知 API/资源、匿名客户端、WebSocket 非法操作、单连接多配置订阅及取消。
- 并发：同基准主/beta 写入单赢家、全量与规则竞争、beta 与最后规则删除竞争、删除与旧编辑竞争；不同配置并行、连续发布/100 客户端收敛、慢回调及 SDK 生命周期竞态。
- 随机：固定种子的 60 步操作序列，用独立内容账本和标签样例核对普通/灰度客户端，不复用生产解析器。
- 在线：仅 WebSocket 入列表、跨实例已发送信息、内置标签/字面值建议、断连最终移除。
- 故障：真实慢 WebSocket 写入超时、实例异常退出与租约过期、数据库不可用与缓存、断线期间连续发布、实际日志清理后恢复、磁盘缓存重启、缺少 Pong 超时、删除后离线缓存不复活。
- 存储专项：32 个 beta 编辑者只产生一次变更、数据库事件写入失败时完整回滚、清理与历史复制/回退竞争只产生合法原子结果。

快速连续发布允许合并通知，不要求收到每个中间版本。回退产生新主版本，删除重建产生新身份并允许编号重置，beta 内容变化不依赖版本号；所有客户端最终必须收敛到应生效内容，旧快照不能覆盖新状态。

故障检测与传播有明确窗口。测试先确认退出就绪再检查离线缓存；在线列表等待快照/租约，不套用配置通知延迟。日志过期由隔离控制器确认数据库中的变更日志实际清理完成，然后恢复节点。慢连接测试缩小 TCP 接收缓冲并停止读取，正常 SDK 继续收到配置，检查慢连接在心跳超时之前被关闭。

## 覆盖率与报告

Go 使用语句覆盖率，Python 使用语句/行覆盖率。Go 的 `backend-merged.out` 合并数据库/协议测试与真实运行服务生成的 coverage 数据，同一源码块取最大命中数，保留所有未命中块，不将百分比平均。后端分母包含 `cmd/main` 和有可执行语句的生产 `internal` 包，不统计测试辅助包、测试代码或第三方依赖。

门槛为后端 ≥80%、业务规则 ≥90%、Go/Python SDK 各 ≥85%。前端 V8 单测统计 `src/lib/api.ts/config.ts/format.ts` 的行/分支；页面和组件通过真实 Playwright 回归，尚未计入这份单测覆盖率。前端暂不设数字门槛，不将逻辑覆盖率称为全前端覆盖率。

报告位于 `test-results/run-*`，主要文件：

- `layers.json/layers.xml`：各层结果、耗时、失败状态。
- `system/system.json/system.xml`：系统场景、配置规模、随机种子、测试 namespace、代码 HEAD 和清理结果。
- `backend.out/process.out/backend-merged.out/sdk.out`、`python-coverage.json`、`coverage-summary.json`：覆盖率原始数据及统计。
- `frontend-coverage/`：逻辑覆盖率；浏览器报告和 trace 仍使用 `frontend/playwright-report/`、`frontend/test-results/`。
- 各层、三个实例及 Python worker 日志；日志不记录管理员密码或非测试配置正文。

失败返回非零退出码。没有自动重跑覆盖失败记录；需要复现时保留原报告，并使用相同种子与参数指定新目录。报告目录在 Git 中忽略。

事务提交至 SDK 的 P99 ≤300ms、RSS 和大规模容量仍未验收，本次运行不将正确性超时或发布响应时刻当作性能达标依据。

## 2026-10-10 执行记录

完整运行 `python3 tests/run.py --coverage-gates` 通过，报告目录为 `test-results/run-20261010-070001`。使用指定 PostgreSQL 17 镜像和三个真实服务实例，32 写入者、100 客户端、10 配置、10 轮并发，以及默认随机种子。业务、协议、并发、随机、在线客户端和六项故障子场景均通过；Go race、vet、Python mypy/ruff、前端类型检查、构建及格式检查通过。

| 统计范围 | 已执行结果 |
| --- | --- |
| 后端（含真实进程覆盖率） | 82.46%，1439/1745 条语句 |
| 业务规则 | 100%，111/111 条语句 |
| Go SDK | 86.72%，307/354 条语句 |
| Python SDK | 91.81%，415/452 行 |
| 前端三个逻辑模块 | 行覆盖率 79.25%，分支覆盖率 91.94% |

前端 44 个单测和 21 个真实浏览器流程通过。Python 完整运行执行了当时的 12 个单元测试及 1 个真实服务集成测试；随后补充订阅数量边界，重新执行全部 13 个单元测试并追加到同一覆盖率数据，因此最终验证了 14 个不同 Python 测试。上表 Python 数值为追加后的统计。

两项 fuzz 分别执行约 18.5 万和 16.6 万次，均通过。最终脚本新增的连接模式回归另以 `--layers system --scenarios business,protocol --rounds 1 --clients 6 --configs 3` 验证通过，报告目录为 `test-results/run-20261010-070612`；其系统层和独立连接层均通过，不能将这个小规模专项报告替代上面的完整回归。

两次运行均正常清理测试数据、服务进程及临时 PostgreSQL 容器。报告保留在本机忽略目录，可按上述命令重新生成。

## GORM 重构验证（2026-10-10）

重构后的 `python3 tests/run.py --coverage-gates` 报告为 `test-results/run-20261010-072618`。指定 PostgreSQL 17 镜像、三个实例、默认 32 写入者/100 客户端/10 配置/10 轮，以及业务、协议、随机、在线、六项故障与独立连接模式均通过。后端 race/vet、两种 SDK、14 个 Python 测试、44 个前端单测、类型、构建和格式检查通过；另执行 `make build` 验证无 CGO 构建。

| 统计范围 | 本次覆盖率 |
| --- | --- |
| 后端 | 83.78%，1395/1665 条语句 |
| 业务规则 | 100%，111/111 条语句 |
| Go SDK | 86.16%，305/354 条语句 |
| Python SDK | 91.59%，414/452 行 |

新增的公共存储回归覆盖清空主/beta 正文和描述，以及在线客户端多批次事务中后续校验失败时回滚已写入和删除的记录。

首次浏览器回归为 20 通过、1 失败，原层报告保持失败状态，trace 保存在该目录的 `frontend-failure/`。trace 确认填写规则名称时下拉菜单尚未恢复焦点，表单仍为 inert；请求本身已包含空名称，存储返回与请求一致。测试辅助函数改为等待菜单关闭及焦点恢复，并增加规则保存名称断言后，重新执行全部 21 个浏览器流程通过，日志为 `test-results/gorm-frontend-after.log`。不将首次失败记录覆盖成通过。

本次继续仅验收真实 PostgreSQL；MySQL 维护 GORM 方言支持，未运行实库测试。容量、资源和 P99 指标仍未测量。

## 移除订阅数量上限（2026-10-10）

服务端、Go/Python SDK 统一移除单客户端 10 个订阅的固定上限。专项报告为 `test-results/subscriptions-unlimited-20261010`，执行 `--layers integration,system --scenarios protocol,concurrency --writers 4 --clients 6 --configs 16 --rounds 2` 通过：后端 PostgreSQL race/vet、两个 SDK 集成、协议与并发以及独立连接模式均通过。协议验证单条 WebSocket 连接注册 32 个订阅后仍可增加订阅；SDK 同时验证多配置订阅、重复订阅拒绝、取消及并发关闭。

`bodesi-pkg/nacos` 使用本地新版 SDK 的 race、三实例集成和调用方编译验证通过；32 个配置共用一个 SDK 客户端及一条有效 WebSocket 连接，包含断线恢复、取消后离线读取和替换监听。SDK 发布前，适配库使用本地 module 替换进行联调。

## Go SDK 订阅与适配层简化（2026-10-12）

Go SDK 新增 `SubscribeWithOptions`，合并初始读取和订阅注册，支持跳过初始通知、要求配置存在以及成功读取后替换已有订阅；原 `Subscribe` 的默认通知行为保持不变。新增非阻塞 `Stop` 和等待关闭完成的 `Wait(ctx)`，`Close(ctx)` 组合两者。回调生命周期由 SDK 统一管理，每个订阅减少一个用于取消传播的常驻 goroutine。

`bodesi-pkg/nacos` 移除独立订阅表、回调队列、后台执行循环和客户端 HTTP 定时轮询，核心适配文件从 193 行缩减至 72 行，两个运行文件合计从 373 行缩减至 246 行。保留原内容变化通知和参数映射，初版曾临时保留旧轮询方法、配置字段和 flag 的兼容入口；后续按要求已将这些入口完全删除，见下方记录；断线依靠 WebSocket 重连后的最新快照恢复。

验证通过：

- Go SDK 全部 `go test -race ./...` 与 `go vet ./...`，新增单次初始读取、读取与订阅之间更新不丢失、失败/取消替换保留旧订阅、回调内 Stop、Wait 超时及最终退出测试。
- `python3 sdk/test-integration.py`：指定 PostgreSQL 17 镜像和两个独立实例，Go SDK race 及 Python 14 个测试（含真实服务集成）通过。
- `python3 nacos/run-integration.py /home/bodesi/repo/confhub`：三个独立实例，适配库全部 race 和真实服务集成通过；覆盖跨实例推送、灰度、回退、删除重建、并发读取及单连接 32 个订阅恢复。新增验证旧轮询参数不会增加 HTTP 请求、回调内替换自身订阅可继续收到更新。
- 适配库 `config`、`service/config-reload` 的 race 检查及相关包 vet 通过。

测试环境均已清理；SDK 与适配库仍使用本地 module 替换联调，正式发布前需更新 SDK 远程版本并移除该替换。

现有 `platform-api` 的五个使用方包（`akf_bootstrapper`、`appdep`、升级 resolver/runtime 和 `pkg/edge`）编译通过，未修改其源码或模块文件，检查使用临时 modfile 同时指向本地适配库和新版 SDK。首次构建因根分区空间不足失败；改用内存盘保存临时构建文件与缓存后，通过 `go test -exec=/usr/bin/true -vet=off -run=^$` 仅验证编译链接，未执行这些使用方的测试；相关临时文件已清理。

### 删除失效接口（2026-10-12）

按要求删除 `SetConfigWatchPollInterval`、`config.Options.NacosWatchPoll`、`-nacos-watch-poll`，一并删除默认值、flag 绑定和失效的兼容测试，适配库运行代码降至 241 行。调用方需移除方法调用、字段赋值及部署参数；本次不修改调用方。保留并验证不执行 HTTP 定时轮询及 WebSocket 恢复后的最新快照通知。

删除后 `go test -race ./nacos ./config ./service/config-reload`、对应包的 `go vet` 和差异格式检查通过。

### 合并适配层（2026-10-12）

删除 `nacos/confhub.go` 中的中间结构 `confhubConfig` 和构造、读取、监听、取消、关闭转发方法，统一到 `nacos.Client`。Client 直接持有 Go SDK 客户端，公开方法直接调用 SDK；删除仅用于适配的配置键转发和独立客户端校验方法，命名空间默认值直接由 SDK 处理。运行代码从两个文件 241 行降至单个 `nacos/nacos.go` 的 186 行。

`go test -race ./nacos ./config ./service/config-reload`、相关包 vet 和差异格式检查通过，覆盖空 namespace、首次通知过滤、替换、回调内关闭及替换、断线恢复和并发生命周期。
