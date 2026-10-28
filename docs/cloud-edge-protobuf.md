# 云边 Protobuf 协议

云边协议帧、配置记录流、NATS 建连握手，以及边缘新镜像内容均使用 Protobuf。HTTP 管理接口、业务客户端 GET/WebSocket 和 Go/Python SDK 继续使用既有 API；JSON、YAML、TOML 等配置正文保留原始字符串，不改变换行、缩进或格式。

## 格式与边界

唯一的消息定义位于 [replication.proto](../internal/replication/wire/replication.proto)，生成的 Go 文件随源码提供。

| 数据 | 编码 |
| --- | --- |
| 同步会话消息 | `Frame`，每个 `Conn.Send` 对应一条完整二进制消息，版本号为 2 |
| 消息类型 | `FrameType` 枚举，包含水位、拉取、开始、分片、结束、收片确认、心跳收讫和应用确认 |
| 配置记录流 | `Record` 的 varint 长度前缀流；`state` 缺失表示删除，不能用空配置代替删除 |
| 配置聚合 | `State`，保留配置身份、主版本、唯一 beta、规则顺序及条件 |
| 分片与摘要 | `bytes` 字段；分片不使用 Base64，SHA-256 为原始 32 字节 |
| NATS 建连 | `OpenRequest`/`OpenReply`，独立校验版本号和同步 token |
| 边缘持久化 | `state_proto`：PostgreSQL BYTEA / MySQL LONGBLOB，使用同一 `State` 定义 |

版本号、修订号和游标使用 64 位整数，时间使用 `google.protobuf.Timestamp`，保留纳秒精度；非法时间戳不能进入镜像。Protobuf 未知字段遵循标准解码行为；删除字段时应保留原字段编号，不能将旧编号用于新的含义。不兼容变更需要提升协议版本。

沿用现有限额：消息 128 KiB、分片 48 KiB、每 8 片确认、每批 1 GiB/100,000 条记录、单次传输 2 分钟。单条记录最多 16 MiB；解码器先校验声明长度再分配内存，并拒绝截断、超长、重复键或记录数量不符的输入。传输摘要与长度验证完成后才应用；只有数据库提交完成才发送应用确认。Protobuf 不改变租约隔离、分代原子切换或失败保留旧镜像的行为。

## 升级已有部署

当前 schema 为 9。停止旧云端和边缘应用，用新二进制或镜像执行 `confhub migrate`，然后启动新云端和边缘。正常启动不会隐式升级已有数据库。

- 云边同步协议从 JSON v1 改为 Protobuf v2，不提供混合版本同步；云边需要协调升级。升级期间已有完整镜像的边缘仍能继续本地服务。
- schema 8 的 JSON 镜像原样保留，升级后即使断云也可读取。新导入或更新写入 `state_proto`，不再写入 `state_json`；未更新记录继续从旧 JSON 读取，后续全量替换会回收旧代次。
- schema 7 的旧镜像仍沿用既有离线兼容及首次同步转换机制。schema 4/5/6 的云端数据沿用已有迁移路径。
- 已写入 Protobuf 镜像后，schema 9 的向下迁移会拒绝丢弃数据；需要回退旧二进制时使用升级前备份。MySQL 向下迁移的 CHECK 校验要求 MySQL 8.0.16+。

## 重新生成代码

普通构建不依赖 protoc。当前生成工具版本为 protoc 36.2、protoc-gen-go v1.36.9：

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.9
make proto-generate
```

修改 `.proto` 后应一并更新生成文件，使用标准 Protobuf 工具生成，不手工编辑 `.pb.go`。测试入口与验证记录见 [云边测试记录](cloud-edge-testing.md)。
