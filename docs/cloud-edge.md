# 云边配置同步

云端 ConfHub 管理配置，边缘 ConfHub 使用自己的 PostgreSQL/MySQL 镜像提供 HTTP GET 和 WebSocket。同步全部当前主配置、beta 与有序灰度规则；规则在边缘按原客户端 tags 匹配。云端的主版本历史、管理账号和审计不向边缘复制。边缘提供需要本地用户名/密码登录的只读控制台，可查看已同步的配置、beta、灰度规则、本地在线客户端及同步状态；管理员账号和 JWT 密钥在边缘独立配置，不从云端同步，断云时仍可登录。边缘必须设置 `CONFHUB_JWT_SECRET`，首次初始化通过 `CONFHUB_ADMIN_PASSWORD` 创建本地 admin，后续启动不会覆盖已有密码。同一边缘共享数据库的实例应使用相同 JWT 密钥。edge 模式禁止配置、规则、组织及账号管理写入；登录、退出和不修改配置的标签试算仍可调用，其他 HTTP 写请求返回 403，存储层也拒绝管理写入。上游镜像导入、连接状态、登录审计和内部清理继续工作。

## 同步与通信

同步流程只依赖一个双向消息接口：

```go
type Conn interface {
    Send(context.Context, []byte) error
    Recv(context.Context) ([]byte, error)
    Close() error
}
```

云边通信使用 Protobuf v2，消息类型、同步记录和配置聚合由 [replication.proto](../internal/replication/wire/replication.proto) 定义；协议及升级细节见 [Protobuf 说明](cloud-edge-protobuf.md)。已有 WebSocket、NATS 与用于测试/嵌入的内存实现。快照、增量、水位通知和确认都经同一个实例传递，不依赖额外的 HTTP 下载。新增实现只需满足完整消息、取消、关闭及有界队列的契约；业务同步逻辑不引用 WebSocket 或 NATS。

首次同步导出一致快照。后续按云端持久游标读取完整变更区间，将同一配置的多次变更合并为当前最终状态；不保证回放所有短暂中间内容。日志过期或增量超过 1,000 个键时重新全量。完整替换会清除云端已不存在的本地配置。

配置记录使用 varint 长度前缀的 Protobuf 流，单条记录最多 16 MiB，读取长度后先检查上限再分配正文内存。每条传输消息最多 128 KiB，正文以 48 KiB 分片发送，每 8 个分片等待接收确认。分片正文使用 bytes 字段直接传输，SHA-256 摘要使用 32 字节二进制字段。分片确认只用于流控；校验顺序、长度与 SHA-256 后，边缘才开始应用。增量在事务内提交配置、来源游标和本地广播事件；全量先写入私有镜像代次，最后用短事务原子切换活动指针、来源游标和通知水位。网络全量暂存于私有临时文件，失败保留旧镜像；Linux 上临时文件立即 unlink，进程崩溃会释放文件空间。当前每批上限 1 GiB/100,000 条记录，传输时限 2 分钟；超过上限失败并显示同步异常，不跳过数据推进游标。运行环境需为暂存留出磁盘空间。

每个云端进程只有一个共享 Publisher 按配置的轮询间隔读取源水位，再向 WebSocket/NATS 的全部同步会话广播变化，数据库轮询次数不随会话数增长。同步会话每 2 秒交换缓存水位/收讫信息；空闲 15 秒无对端消息则重建。断线退避带抖动，上限 10 秒；重连重新核对持久游标。云端每种传输每个实例最多接收 128 个同步会话。NATS 使用 Core NATS，应用层保证续传和校验，不依赖 JetStream 的交付确认；会话采用独立 subject，只有创建会话的请求在云端实例间使用 queue group。NATS 每会话使用容量为 8 的有界发送队列，由单独的写任务交给 Broker 客户端批量发送，数据分片不再逐条等待 Flush 的往返确认；建立订阅时仍保留 Flush 屏障。Send 成功仅表示进入本地队列，收片确认和数据库提交确认仍由同步协议负责。底层 socket 写入最长等待 10 秒。Broker 连接使用 2 秒 ping、最多 2 个未响应 ping，避免静默断网时按默认的分钟级心跳等待恢复。

## 多实例与故障

同一边缘的多个应用实例共享本地数据库，通过数据库租约选出一个同步任务。租约默认 30 秒，每三分之一租期续期，世代编号防止旧任务覆盖新任务。全量以不超过 128 条记录或约 4 MiB 正文的批次写入私有代次，单条聚合记录不会拆开。暂存写入不持有租约行锁或可见变更流锁，续期可独立进行；每批前检查租约，最终切换再锁定并核验世代和基线。中途失去租约的旧任务不能激活镜像。暂存失败不改变当前可见内容，后台按清理批量回收废弃代次。新镜像通过 `state_proto` 保存 Protobuf，PostgreSQL 使用 BYTEA，MySQL 使用 LONGBLOB。

全量切换通过持久通知水位触发所有边缘实例刷新缓存和订阅，不在切换事务中逐条写通知。Hub 分批刷新并保留进度，健康读取期间持续保持就绪；刷新期间的新发布仍按后续游标处理。旧代次保持不可见，按有限批次删除，不在切换事务中清空整个镜像。

所有边缘实例消费同一个本地变更流。配置 ID、主版本号、beta 与规则来自云端，本地通知序号独立持久递增；边缘重启不会重置它。SDK 地址池只放同一个共享数据库的边缘实例，不混入云端或独立边缘数据库。

| 故障 | 行为 |
| --- | --- |
| 没有完整镜像 | `/health/ready` 返回 503，读取/订阅不可用 |
| 云端断连，已有完整镜像 | 本地继续提供读取/订阅，包括新客户端及应用重启后的读取 |
| 云端恢复 | 增量追平；游标过期则保留旧镜像服务直到全量替换完成 |
| 活动同步实例退出 | 其他实例接管；正常退出释放租约，异常退出等待租约过期 |
| 本地数据库不可用 | 按现有 Hub 存储故障策略退出就绪并关闭连接 |
| 来源身份改变或云端游标倒退 | 拒绝应用，状态显示 `source_changed_rebuild_required`，需显式重建 |

云端断连期间，云端删除、规则关闭和新发布暂不在边缘生效。发布成功表示云端提交成功，不表示所有边缘已应用。应用多实例不能消除数据库单点，数据库 HA 应由部署设施提供。

只读控制台的“同步状态”页通过需要登录的 `GET /api/admin/edge/status` 读取状态。现有运维接口 `GET /api/edge/status` 保留，返回共享镜像的来源/游标、首次同步状态、最近应用时间、租约，以及该实例的同步连接状态。接口仅包含运维元数据，可通过网络访问策略限制；不包含配置正文或凭据。`connected` 表示该实例有上游会话，备用实例可以为 false；`mirror.ready` 和 `/health/ready` 才表示本地服务可用。`last_success` 为最近应用批次时间，低更新频率时应结合连接及观察水位判断新鲜度。

来源数据库重建/备份恢复不能悄悄切换数据集。需要重建边缘镜像时，备份后按运维步骤重新初始化本地数据库；本地通知序号重置也需要同步清理业务 SDK 的旧缓存/会话或更换服务地址。自动来源切换和客户端跨独立镜像切换不在一期范围内。

## 启动参数

flag 优先于环境变量，所有环境变量前缀为 `CONFHUB_`。常规数据库、缓存和通知参数继续生效。

| flag | 环境变量后缀 | 默认/要求 |
| --- | --- | --- |
| `--role` | `ROLE` | `standalone`；可选 `cloud`、`edge` |
| `--replication-transport` | `REPLICATION_TRANSPORT` | `websocket`；可选 `nats` |
| `--replication-url` | `REPLICATION_URL` | 边缘必填；NATS 模式云端也必填 |
| `--replication-token` | `REPLICATION_TOKEN` | 云端/边缘必填，至少 32 字节 |
| `--replication-subject` | `REPLICATION_SUBJECT` | `confhub.replication`，不能包含通配符 |
| `--replication-lease` | `REPLICATION_LEASE` | `30s`，至少 `3s` |

WebSocket 地址例：`wss://cloud.example/api/replication/watch`；NATS 地址例：`tls://user:password@broker.example:4222`。云端/边缘的同步 token 必须一致，不能使用管理员 JWT 代替。生产跨网使用 TLS；NATS 配置 Broker 认证和 subject 权限，禁止未授权读取/发布会话 subject。示例的共享密码适合本地演示，生产应配置机器账号和权限。

边缘页面隐藏新建、批量修改、发布、回退、规则修改、组织管理、用户管理和密码修改入口，直接访问相应管理写接口也会被拒绝。边缘“本地版本”只展示当前全量版本和 beta，不展示或查询云端历史；目录从活动镜像中提取，不会显示已删除配置所在的遗留目录。灰度标签试算使用本地镜像，在线客户端只展示当前边缘共享数据库中的连接。

普通客户端仍使用现有 GET/WebSocket 和 Go/Python SDK，只将地址改为本地边缘。空 namespace/group 目录不精确镜像，按已有配置的键建立所需组织。云端自动补全暂不收集边缘客户端标签，云端仍可手动填写标签规则；完整客户端汇总和标签摘要上报留待后续。

## 两套 Docker Compose

模板分别位于 [云端](../deploy/cloud/compose.yaml) 和 [边缘](../deploy/edge/compose.yaml)，各自有本地 PostgreSQL、独立命名卷。云端 `ha` profile 启用第二个实例，边缘默认两个实例。应用默认 1 CPU/1 GiB，数据库 1 CPU/2 GiB；NATS 可选容器限制 0.5 CPU/256 MiB。

先构建镜像并复制示例，分别替换随机密码、JWT 密钥及同步 token：

```sh
docker build -t confhub:dev .
cp deploy/cloud-edge.env.example .env.cloud-edge
docker network create confhub-cloud-edge
docker compose --env-file .env.cloud-edge -f deploy/cloud/compose.yaml --profile ha up -d
docker compose --env-file .env.cloud-edge -f deploy/edge/compose.yaml up -d
```

云端控制台默认 `http://127.0.0.1:18080`，第二个云端实例 18081；边缘只读控制台及客户端地址为 18090、18091，使用各自本地 admin 登录。演示模板共用环境变量文件中的初始密码和 JWT 密钥，实际部署可分别指定云端与边缘环境文件。模板端口只绑定本机，实际跨机器部署应调整端口/代理、TLS 和 `CONFHUB_EDGE_REPLICATION_URL`；示例 external network 只用于同一 Docker 主机的云边演示，跨机器不要求共享 Docker 网络。

改用 NATS 时在示例环境中设置：

```dotenv
CONFHUB_REPLICATION_TRANSPORT=nats
CONFHUB_CLOUD_REPLICATION_URL=nats://confhub:REPLACE_NATS_PASSWORD@confhub-nats:4222
CONFHUB_EDGE_REPLICATION_URL=nats://confhub:REPLACE_NATS_PASSWORD@confhub-nats:4222
```

启动云端时再加 `--profile nats`。当前 schema 为 9；已有 schema 4/5/6/7/8 数据库应先停止旧应用，用新镜像执行 `migrate`，再启动新应用；正常启动不隐式升级已有 schema。schema 7 的边缘镜像在升级后即使断云也可继续读取旧配置，下一次成功同步时转换为代次存储，旧表数据随后分批清理。schema 8 的 JSON 镜像仍可在断云时读取，新导入或更新的记录改写为 Protobuf；未更新的旧记录保留只读兼容，全量替换后由旧代次清理一并回收。云端和边缘须协调升级至 v2，旧 JSON v1 不能继续同步；普通业务 SDK 无需升级协议。同步 token 通过环境变量传入，不放在 URL 查询参数中。

## 测试入口

```sh
# 首次会构建镜像，需要 Docker 和已安装的 Python SDK。
make test-cloud-edge
# 复用指定的本地镜像，减少构建时间：
CONFHUB_EDGE_TEST_IMAGE=confhub:dev make test-cloud-edge
# 输出标准 Go JSON 测试报告：
CONFHUB_RUN_CLOUD_EDGE=1 go -C tests/cloudedge test -race -json -count=1 -timeout=15m ./...
```

Go 测试程序自行创建两个隔离 Compose 项目、随机端口/凭据及网络，分别运行 WebSocket 和 NATS；结束删除自身的容器、卷与网络，不读取部署 `.env`。故障注入仅针对这些自建项目。测试中的 2 秒日志保留、3 秒租约用于快速验证故障恢复，生产默认值不受影响。

手动运行数据库/协议测试时，通过 `CONFHUB_TEST_POSTGRES_DSN` 指定可创建隔离 schema 的 PostgreSQL，NATS 额外通过 `CONFHUB_TEST_NATS_URL` 指定测试 Broker。`make test-full` 会创建自己的 PostgreSQL 和 NATS，包含同步协议及存储集成测试；完整云边 Docker 故障测试使用独立的 `make test-cloud-edge`。

验证结果和范围见 [云边测试记录](cloud-edge-testing.md)。MySQL 提供迁移与 GORM 适配，按此前约定只运行 PostgreSQL 数据库集成测试；云边 WAN 时延与容量压测尚未进行。
