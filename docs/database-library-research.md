# PostgreSQL / MySQL 存储库调研

调研日期：2026-10-10。状态：已采纳 GORM 处理常规读写，并保留 golang-migrate 管理表结构。下文的候选比较及兼容点记录调研时的基线；实际实现见 [架构中的数据库访问与迁移](architecture.md#数据库访问与迁移)。未进行性能比较。

## 结论

**有专门的 Go 库处理数据库方言。就“减少自行维护 PostgreSQL / MySQL 兼容代码”这个目标，优先建议 GORM。** 它提供统一的查询、更新、Upsert、事务、行锁接口，并能将常见唯一键和外键错误转换为统一错误。Bun 与 goqu 也支持两种数据库，但在 Upsert、数据库函数或错误处理上需要区分覆盖范围，不能仅凭“支持多个数据库”认为业务代码完全可移植。[GORM 连接文档](https://gorm.io/docs/connecting_to_the_database.html)、[Upsert](https://gorm.io/docs/create.html#Upsert-On-Conflict)、[错误转换](https://gorm.io/docs/error_handling.html#Dialect-Translated-Errors)

这一建议基于兼容性需求。如果优先保持接近手写 SQL 的开发方式，Bun 可以作为另一候选；如果只想替换 SQL 构造和占位符处理，goqu 更贴近当前架构。没有必要为采用查询库而同时替换现有 `golang-migrate`。

## 调研时的项目兼容点

重构前项目使用 `database/sql` 与各数据库驱动，兼容层散落在以下位置：

- [database.go](../internal/storage/database.go)：`Store.query` 将 SQL 中所有 `?` 字符逐个改为 PostgreSQL 的 `$1`、`$2`；它没有识别字符串字面量、注释或 SQL 运算符。该行为只在项目遵守受限 SQL 写法时成立。
- [presence.go](../internal/storage/presence.go)：客户端实例的 Upsert 分别使用 `ON CONFLICT ... EXCLUDED` 与 `ON DUPLICATE KEY ... VALUES`；数据库时钟函数、二进制前缀匹配也有方言分支。
- [configs.go](../internal/storage/configs.go)：配置写入使用事务与 `FOR UPDATE`，并将驱动特定的约束错误转为业务错误。
- [migrations](../internal/storage/migrations)：PostgreSQL 与 MySQL 分别维护 SQL 迁移；字段类型、排序规则等需要精确控制。

所以要解决的是 **SQL 方言生成与部分错误归一化**，不只是占位符转换。

## 候选库比较

| 库 | 定位与两库支持 | 对本项目的帮助 | 主要边界 |
| --- | --- | --- | --- |
| **GORM** | ORM；官方支持 MySQL、PostgreSQL | 统一 CRUD、Upsert、行锁、事务；可启用常见约束错误转换 | 要把相关查询迁到生成接口；`Raw` 中的数据库函数仍需自己适配 |
| **Bun** | 接近 SQL 的 ORM；提供 `pgdialect`、`mysqldialect` | 查询构造、模型扫描、事务、行锁；可以包装已有 `*sql.DB` | 官方 Upsert 示例仍分别写 PostgreSQL / MySQL 的 `.On(...)`；未据此确认具有 GORM 等价的统一约束错误转换 |
| **goqu** | 多方言 SQL builder，明确不是 ORM | 引号、参数、CRUD SQL、冲突子句和行锁生成；可继续使用 `database/sql` | 不接管业务模型和数据库语义；更新表达式中的 `excluded` / `VALUES(...)` 仍可能需要方言适配 |
| **sqlx** | 对 `database/sql` 的扩展 | 结构体扫描、命名参数、`IN` 展开、`Rebind` | 不会把 PostgreSQL Upsert、函数、字段类型转换成 MySQL 语法；不能单独解决当前目标 |
| **Ent** | 通过 schema 与代码生成构建类型化 ORM；支持两库 | 生成 CRUD、事务；可开启 Upsert、行锁特性 | 需要引入 schema 定义与生成工作流，现有存储层改造较大 |
| **sqlc** | 从 SQL 生成类型化访问代码；支持两库的 Go 生成 | 降低手写扫描代码成本，检查 SQL 与参数 | 输入仍是数据库对应的 SQL，不是运行时跨数据库方言转换器 |

来源：[GORM](https://gorm.io/docs/connecting_to_the_database.html)、[Bun drivers](https://bun.uptrace.dev/guide/drivers.html)、[goqu 定位](https://doug-martin.github.io/goqu/)、[goqu dialects](https://doug-martin.github.io/goqu/docs/dialect.html)、[sqlx](https://jmoiron.github.io/sqlx/)、[Ent SQL integration](https://entgo.io/docs/sql-integration/)、[sqlc language support](https://docs.sqlc.dev/en/stable/reference/language-support.html)。

## 三个主要候选的实际覆盖范围

### GORM

1. **Upsert**：使用 `clause.OnConflict` 与 `clause.AssignmentColumns` 表达“冲突时将指定列更新为插入值”，由驱动生成 PostgreSQL 的 `ON CONFLICT` 或 MySQL 的 `ON DUPLICATE KEY UPDATE`。这能替代 `client_instances` 当前的两条 SQL 分支。[官方示例](https://gorm.io/docs/create.html#Upsert-On-Conflict)
2. **事务与行锁**：`Transaction` / `Begin` 可管理事务，`clause.Locking{Strength: "UPDATE"}` 表达 `FOR UPDATE`。现有读已提交写入事务、可重复读快照事务的隔离级别仍应显式保留。[事务](https://gorm.io/docs/transactions.html)、[行锁](https://gorm.io/docs/advanced_query.html#Locking)
3. **错误归一化**：启用 `gorm.Config{TranslateError: true}`，可以使用 `errors.Is(err, gorm.ErrDuplicatedKey)` 和 `gorm.ErrForeignKeyViolated`。本次也检查了本机缓存的 GORM MySQL / PostgreSQL 驱动 v1.6.0：MySQL 映射 `1062`、`1451`、`1452`，PostgreSQL 映射 `23505`、`23503`，覆盖项目已有的主要约束错误映射。这个能力不等于所有错误都统一，也不提供业务冲突策略。[错误文档](https://gorm.io/docs/error_handling.html#Dialect-Translated-Errors)、[MySQL driver source](https://github.com/go-gorm/mysql/blob/v1.6.0/error_translator.go)、[PostgreSQL driver source](https://github.com/go-gorm/postgres/blob/v1.6.0/error_translator.go)
4. **原生 SQL**：可以继续调用 `Raw` / `Exec`，但数据库特有表达式不会因此变成可移植表达式。仅将现有 SQL 放进 GORM 并不能消除方言分支。[SQL builder](https://gorm.io/docs/sql_builder.html)
5. **迁移**：提供 `AutoMigrate` 与 Migrator，也有 Atlas 集成；它们不要求取代现有版本化 SQL 迁移。官方文档明确讨论了从自动迁移转为版本化迁移的需求。项目有显式 schema 版本、dirty 状态与初始化规则，建议保留当前 `golang-migrate`。[迁移文档](https://gorm.io/docs/migration.html)

采用时还需要规避 ORM 的默认行为改变现有业务：例如 `Updates(struct)` 默认跳过零值，空字符串、`0`、`false` 的更新应明确指定字段或使用 map；不要让模型约定自动改动现有表名、时间字段或业务状态。[更新文档](https://gorm.io/docs/update.html#Updates-multiple-columns)

### Bun

Bun 可以通过 `bun.NewDB(sqldb, pgdialect.New())` 或 `mysqldialect.New()` 使用现有连接；`RunInTx` / `BeginTx` 接受 `*sql.TxOptions`，查询支持 `.For("UPDATE")`。[连接](https://bun.uptrace.dev/guide/drivers.html)、[事务](https://bun.uptrace.dev/guide/transactions.html)、[SELECT](https://bun.uptrace.dev/guide/query-select.html)

**Upsert 的边界必须说清楚**：官方示例在 PostgreSQL 下写 `.On("CONFLICT (id) DO UPDATE").Set("title = EXCLUDED.title")`，在 MySQL 下写 `.On("DUPLICATE KEY UPDATE")`。它的 `.Ignore()` 有跨数据库接口，但“忽略冲突”不等同于项目需要的“更新已有客户端实例”。因此不能承诺换成 Bun 就自动消除当前 Upsert 分支。[INSERT 文档](https://bun.uptrace.dev/guide/query-insert.html)

Bun 的官方错误处理示例仍展示应用侧处理 `sql.ErrNoRows` 与重复键错误；本次没有确认到与 GORM `TranslateError` 等价的公开统一约束错误契约。若选择 Bun，应继续用驱动错误码处理约束冲突，而不是照搬文档中通过错误文本判断的简单示例。Bun 也有 Go / SQL 迁移工具，但迁移系统可以保持现状。[错误示例](https://bun.uptrace.dev/guide/golang-orm.html)、[迁移](https://bun.uptrace.dev/guide/migrations.html)

### goqu

goqu 支持 MySQL、PostgreSQL 等 dialect，通过结构化表达式生成 SQL；需要正确注册 dialect，并使用 `.Prepared(true)` 生成 SQL 与参数，而不是依赖默认的值内联。它支持 `OnConflict(goqu.DoUpdate(...))`，文档说明 MySQL 对应 `ON DUPLICATE KEY UPDATE`，还提供 `ForUpdate` 与带 `sql.TxOptions` 的 `BeginTx`。[dialect](https://doug-martin.github.io/goqu/docs/dialect.html)、[API 与冲突示例](https://pkg.go.dev/github.com/doug-martin/goqu/v9)、[行锁](https://doug-martin.github.io/goqu/docs/selecting.html#forupdate)、[事务](https://doug-martin.github.io/goqu/docs/database.html)

但结构化子句不意味着任意表达式都能翻译。例如官方 `DoUpdate` 示例使用 `goqu.I("excluded.address")` 引用 PostgreSQL 的新行值，这个标识符本身不能视为跨数据库的新值引用 API。对于项目的 Upsert，需要检查两种 dialect 实际生成的 SQL；也需要保留数据库时钟和错误映射等少量明确适配。

## 任何库都不能自动抹平的差异

- **数据库时钟与租约语义**：项目读取数据库时间决定在线状态与租约。PostgreSQL 的 `now()` / `CURRENT_TIMESTAMP` 表示事务开始时间，`clock_timestamp()` 才随实际时间变化；不能为了统一函数名改变项目现有的时钟含义。Bun 官方也建议对数据库专属函数使用方言检查或 helper。[PostgreSQL 时间函数](https://www.postgresql.org/docs/current/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT)、[Bun 函数适配](https://bun.uptrace.dev/guide/drivers.html)
- **大小写、二进制字段与排序规则**：项目的 key、前缀匹配及唯一性要求需要与现有 `bytea` / `VARBINARY`、二进制排序规则和查询条件一致。这需要具体 schema 与查询设计，不能只靠默认字符串字段映射。[项目迁移](../internal/storage/migrations)、[presence.go](../internal/storage/presence.go)
- **Upsert 冲突目标与返回结果**：MySQL 在唯一键或主键冲突时触发更新，不提供 PostgreSQL 同等的指定冲突目标语义；官方建议避免在多个唯一索引的表上盲目使用该子句。其 affected rows 也区分插入、更新和无变化。统一 API 不代表两库所有细节都相同。[MySQL Upsert](https://dev.mysql.com/doc/refman/8.4/en/insert-on-duplicate.html)
- **隔离、锁竞争与快照一致性**：库可以提交隔离级别和生成行锁 SQL，但项目的全局变更序号、配置写入、租约争抢与快照读取是否正确，仍要通过真实 PostgreSQL / MySQL 并发集成测试验证。[现有配置事务](../internal/storage/configs.go)、[现有维护租约](../internal/storage/maintenance.go)

## 建议的实施边界

如果决定采用 GORM，建议保留外部 `Store` 接口和 `golang-migrate`，将内部常规 CRUD、Upsert、参数处理、约束错误处理迁到 GORM 的生成接口；将数据库时钟、二进制前缀匹配及必要的 schema 差异集中到少量方言 helper。先验证 `client_instances` Upsert 与 `change_stream` 的事务写入，再扩展到其余存储方法。两库均应验证零值更新、约束冲突、并发写入序号、重复心跳、租约竞争和快照一致性。

现已将组织、配置、版本、beta、规则、变更日志、管理员、维护租约和在线客户端的常规读写迁到 GORM 模型及查询构造器，移除自制占位符转换、手写 Upsert 和驱动错误码映射。数据库时间及二进制前缀表达式集中在 `dialect.go`，迁移 SQL 和 schema 版本保持原有管理方式。当前采用显式存储行模型，无额外代码生成步骤；后续若查询规模需要，可引入 GORM Gen，不将生成器或 AutoMigrate 作为运行时依赖。
