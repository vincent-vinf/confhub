# PostgreSQL 跨网络云边复制调研

核对日期：2026-10-28；依据 PostgreSQL 17 官方文档。本文是方案调研，尚未部署验证。

对于“云端可写、边缘只读、断网继续读取、恢复后追平”，推荐 **PostgreSQL 原生异步物理流复制 + ConfHub 只读模式**。不需要 Canal 或自研同步服务。

```text
云端 ConfHub → 云端 PostgreSQL
                    ↑ 边缘主动建立复制连接，接收 WAL
边缘客户端 → 只读 ConfHub → 边缘 PostgreSQL hot standby
```

异步复制不会等待边缘确认才提交云端事务；因此边缘网络故障不会阻塞云端发布，但边缘读取可能滞后。边缘初次通过 `pg_basebackup` 建立基础副本，随后接收、回放 WAL；连接故障后 PostgreSQL 会重试。Hot standby 支持本地只读查询。[流复制及重试机制](https://www.postgresql.org/docs/17/warm-standby.html)、[初始化工具](https://www.postgresql.org/docs/17/app-pgbasebackup.html)、[只读副本](https://www.postgresql.org/docs/17/hot-standby.html)

跨网络无需同一局域网：边缘需要能主动连接云端 PostgreSQL 的 TCP 地址。配置监听、复制账号及 `pg_hba.conf`；公网建议通过 VPN/专网，或配置 TLS 并使用 `sslmode=verify-full` 校验证书与主机名。[网络与账号](https://www.postgresql.org/docs/17/warm-standby.html#STREAMING-REPLICATION-AUTHENTICATION)、[TLS](https://www.postgresql.org/docs/17/libpq-ssl.html)

必须接受这些边界：

- **复制整个 PostgreSQL 实例**，包括配置、规则、用户及表结构，不能按 namespace 过滤；应为 ConfHub 使用独立实例。物理复制要求兼容的硬件架构及同一大版本，建议统一镜像版本。[规划约束](https://www.postgresql.org/docs/17/warm-standby.html#STANDBY-PLANNING)
- **断网追平有保留窗口**。每个边缘使用独立复制槽；离线期间主库积累 WAL。默认槽保留无上限，可能撑满磁盘，应监控并设置 `max_slot_wal_keep_size`。必要 WAL 已删除且归档也不可用时，需要重新做基础副本；不是无限期自动追平。[复制槽](https://www.postgresql.org/docs/17/warm-standby.html#STREAMING-REPLICATION-SLOTS)、[保留参数](https://www.postgresql.org/docs/17/runtime-config-replication.html)
- **低延迟受 WAN 和回放影响**，不能保证固定毫秒数；查询还可能因回放冲突被取消。[Hot standby 冲突](https://www.postgresql.org/docs/17/hot-standby.html#HOT-STANDBY-CONFLICT)

若只想同步部分表，可选 PostgreSQL 内置逻辑复制，但 DDL 和序列状态不随数据复制，需要额外维护，不是本场景最省事的选择。[逻辑复制限制](https://www.postgresql.org/docs/17/logical-replication-restrictions.html)

长时间离线可结合 WAL 归档；pgBackRest 提供备份、恢复和 WAL 归档能力，是复制链路的补充。[pgBackRest 官方说明](https://pgbackrest.org/)

当前 ConfHub 只读模式已关闭迁移及后台数据库写入，保留配置读取和本地事件轮询，适配上述部署方向；实际 PostgreSQL hot standby、断网及恢复链路仍需单独验收。
