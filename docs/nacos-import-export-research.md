# Nacos 配置 ZIP 导入导出调研

调研日期：2026-11-09。研究对象为官方稳定标签 **2.5.1**（`3d2f10b2398411797b6b1501f811dc84bfbcbd2f`）与 **3.1.1**（`7bfadeaf34a40351473a167be99c2d12076c533f`），不将结论泛化到所有 2.x/3.x。本文保留调研时的范围：依据官方文档和固定标签源码，调研阶段未部署 Nacos。后续已完成实现及真实 Nacos 双向验收，见[实现与验收记录](nacos-import-export-implementation.md)。

## 核心结论

- Nacos 导出的是**当前正式配置正文和少量元数据**，不是完整环境备份：不携带源 namespace、历史版本、beta/gray 正文与规则、配置标签、用户和权限。
- 有两代 ZIP：旧 `export=true` 使用可选 `.meta.yml`；新 `exportV2=true` / v3 `export2` 使用 `.metadata.yml`，后者能保留 `type`、`desc`、`appName`。配置正文均放在 `group/dataId`。
- 导入将整个包写入**请求指定的目标 namespace**。同名冲突由 `ABORT`、`SKIP`、`OVERWRITE` 控制；**ABORT 停止后续项，但不回滚已经成功的项**。HTTP/业务成功响应也不代表所有项均成功，应检查计数与明细。
- **接口兼容性有版本和入口差异**：2.5.1 的 v1 导入支持两代 ZIP；3.1.1 保留的 v1 导入仍有兼容分支，但 v3 控制台及其 v3 管理端导入直接使用新格式解析器，不应承诺旧包可直接导入。

以上结论的实现依据见下文固定标签引用。

## HTTP API 与参数

### Nacos 2.5.1：v1 配置接口

默认服务上下文通常为 `/nacos`；源码中的 controller 路径为 `/v1/cs/configs`。[路径常量][v2-constants]

| 操作 | 方法与路径 | 主要参数 |
| --- | --- | --- |
| 旧格式导出 | `GET /nacos/v1/cs/configs?export=true` | `tenant`、`dataId`、`group`、`appName`、`ids` |
| 新格式导出 | `GET /nacos/v1/cs/configs?exportV2=true` | 同上 |
| 导入并发布 | `POST /nacos/v1/cs/configs?import=true` | multipart 字段 `file`；`namespace`、`policy`、可选 `src_user` |

导出源 namespace 参数叫 **tenant**，导入目标参数叫 **namespace**，不要混用。`policy` 是大小写敏感的枚举，默认 `ABORT`。源码对导出标 READ 权限、导入标 WRITE 权限。[导出实现][v2-export]、[导入实现][v2-import]

导出选择器的实际数据库行为：`ids` 非空时优先按 ID 选择，不再叠加 namespace、group 等筛选；否则按 namespace，并可选按 `dataId` 模糊匹配、group 和 appName 筛选。这不是所有条件取交集。[2.5.1 外置数据库查询][v2-select]

### Nacos 3.1.1：控制台与管理接口

官方 v3 控制台文档的 2.9/2.10 节声明：

| 操作 | 方法与路径 | 参数 |
| --- | --- | --- |
| 新格式导出 | `GET /v3/console/cs/config/export2` | `namespaceId`、`dataId`、`groupName`、`appName`、`ids` |
| 导入并发布 | `POST /v3/console/cs/config/import` | `namespaceId`、`policy`、multipart `file` |

文档声明 `policy` 默认 `ABORT`，支持 `SKIP`/`OVERWRITE`，响应 `data` 含 `succCount`/`skipCount`；并建议 ID 列表与 dataId/groupName 选择方式二选一。**文档导出 curl 示例出现 `groupId`，与参数表和源码的 `groupName` 不一致，实际应使用 `groupName`。** [官方 v3 控制台 API 文档](https://nacos.io/en/docs/v3.0/manual/admin/console-api/)

3.1.1 源码确认上述控制台路径、参数与 READ/WRITE、CONSOLE_API 权限标记。控制台导入额外有可选 `srcUser`；不要照搬 v1 的 `src_user`。[ConsoleConfigController][v3-console]

独立控制台模式将请求转发到服务端 `/v3/admin/cs/config/export`、`/v3/admin/cs/config/import`，服务端上下文默认 `/nacos`；内置控制台直接使用本地 handler。[远端转发实现][v3-remote]。因此 curl 的主机、端口和上下文应跟实际控制台部署一致，不能机械复用 v1 的 8848 `/nacos` 地址。

源码边界：上述导出 controller 将 `ids` 标为可选，却直接调用 `ids.removeAll(...)`。手工请求建议明确传 `ids=`（不按 ID 筛选）或具体 ID 列表；不把完全省略 `ids` 视为经过验证的稳定调用方式。[v1 导出][v2-export]、[v3 导出][v3-console]

### 调用参考

以下示例假设已在环境设置实际地址、namespace 和所需鉴权参数；不在示例写入凭证。v1 示例采用默认服务上下文，v3 的 `NACOS_CONSOLE_ORIGIN` 指向实际控制台入口。

```sh
# 2.5.1：按 namespace/group 导出新格式
curl -fG "$NACOS_SERVER_ORIGIN/nacos/v1/cs/configs" \
  --data-urlencode 'exportV2=true' --data-urlencode "tenant=$SOURCE_NAMESPACE" \
  --data-urlencode 'group=DEFAULT_GROUP' --data-urlencode 'ids=' \
  -o nacos-config.zip

# 2.5.1：导入到明确的目标 namespace
curl -f "$NACOS_SERVER_ORIGIN/nacos/v1/cs/configs?import=true" \
  -F "namespace=$TARGET_NAMESPACE" -F 'policy=SKIP' -F 'file=@nacos-config.zip'

# 3.1.1：控制台新接口，groupName 参数
curl -fG "$NACOS_CONSOLE_ORIGIN/v3/console/cs/config/export2" \
  --data-urlencode "namespaceId=$SOURCE_NAMESPACE" \
  --data-urlencode 'groupName=DEFAULT_GROUP' --data-urlencode 'ids=' \
  -o nacos-config.zip
curl -f "$NACOS_CONSOLE_ORIGIN/v3/console/cs/config/import" \
  -F "namespaceId=$TARGET_NAMESPACE" -F 'policy=SKIP' -F 'file=@nacos-config.zip'
```

## ZIP 布局、正文和 metadata

下载文件名为 `nacos_config_export_yyyyMMddHHmmss.zip`，响应设置 `Content-Disposition: attachment;filename=...`；ZIP 中没有额外包装目录，也不会按 type 自动添加扩展名。示意：

```text
nacos_config_export_20261109112754.zip
├── .metadata.yml
├── DEFAULT_GROUP/
│   ├── app.yaml
│   └── feature-switch
└── ANOTHER_GROUP/
    └── app.yaml
```

文件名、正文路径、解密导出以及 metadata 构造均在 [2.5.1 导出][v2-export] 与 [3.1.1 导出][v3-inner-export]，两个 metadata 文件名由[格式常量][v2-format-constants]定义。**配置文件名就是完整 dataId**；同 dataId 可以存在于不同 group。ZIP 条目正文使用 UTF-8 写入，导入统一按 UTF-8 解码；条目名使用 Java 默认 UTF-8 ZIP 流，未提供 GBK 等编码切换。[两个标签相同的 ZipUtils 实现][zip]

新格式 `.metadata.yml` 的实际字段形状如下，取自源码模型而非另外设计的交换协议。字段顺序没有业务含义；示例中字段值为虚构值：

```yaml
metadata:
- appName: example-app
  dataId: app.yaml
  desc: 示例应用配置
  group: DEFAULT_GROUP
  type: yaml
- appName: ''
  dataId: feature-switch
  group: DEFAULT_GROUP
  type: text
```

`metadata` 是列表，每项只有 `group`、`dataId`、`desc`、`type`、`appName` 五个模型字段。序列化省略 Java `null` 属性，但空字符串可以出现。文件路径携带 group/dataId，元数据再次携带两者，用来一一关联正文；`desc` 对应配置描述，字段名不是 `description`。`type` 是元数据字段，不能只根据扩展名推断。[模型][metadata]、[YAML 序列化与解析][yaml]

旧格式 `.meta.yml` **虽然后缀为 yml，内容实际为自定义键值行**，只记录非空 appName，例如：

```text
DEFAULT_GROUP.app~yaml.app=example-app
```

其 key 为 `group + '.' + dataId最后一个点替换为~ + '.app'`；无点的 dataId 保持原样。导出换行固定 CRLF。没有 appName 时整个 `.meta.yml` 可以不存在；旧格式不保存 type/desc。导入通过换行和 `=` 分割，不具备通用 properties/YAML 的转义能力，含 `=` 等特殊字符的值存在解析限制。[旧格式构造][v2-export]、[旧格式解析][v2-old-parse]

## 字段映射与不包含的数据

| Nacos 数据 | ZIP 表达与导入效果 |
| --- | --- |
| group / dataId | 路径 `group/dataId`；新 metadata 也有同名字段 |
| 正式配置正文 | 每个条目的 UTF-8 文本；加密插件参与时导出调用解密，导入调用目标环境加密 |
| type / 描述 / appName | 新格式对应 `type` / `desc` / `appName`；旧格式仅支持 appName |
| namespace / tenant | **不在 ZIP 中**，目标取导入请求参数；不会自动创建源 namespace |
| DB ID、创建/更新时间、作者、源 IP、MD5 | 不作为备份元数据携带；导入按目标环境写入记录 |
| 历史版本、beta/gray 内容和规则 | 不导出；仅从正式配置数据源取当前内容 |
| configTags、use、effect、schema | 新 metadata 模型没有这些字段，不随 ZIP 迁移 |
| 用户、角色、权限、服务发现数据 | 不在配置 ZIP 范围内 |

上述“未包含”是对 [导出字段的穷举][v3-inner-export] 与 [metadata 模型][metadata] 的源码观察，不是完整备份兼容性承诺。导入可能在目标 Nacos 生成新的历史记录，这不等于恢复源历史。[目标端新增配置及历史写入][v3-history]

namespace 还存在版本差异：2.5.1 默认 ID 为空字符串，并将空值、`public`、`null` 归一化为默认 ID；3.1.1 默认 ID 为 `public`，空值归一化为该默认 ID。迁移应保存“源环境显示的公共空间”与目标真实 ID 的对应关系，不能把两种存储约定混成一个字符串。[2.5.1 NamespaceUtil][v2-namespace]、[3.1.1 NamespaceUtil][v3-namespace]、[3.1.1 默认 ID 常量][v3-public]

## 冲突、部分成功和格式校验

| policy | 同一目标 namespace/group/dataId 已存在时 |
| --- | --- |
| `ABORT`（默认） | 首次冲突记入 `failData`，后续项记入 `skipData`；此前已写入项保留 |
| `SKIP` | 保留已有项，记入 `skipData`，继续后续项 |
| `OVERWRITE` | 更新已有项，并计入 `succCount` |

2.5.1 v1 逐项调用 publish；3.1.1 v3 使用持久化层 batch 方法，但内置/外置数据库实现仍逐项执行，**没有全 ZIP 事务回滚**。重复项和失败发生的位置因此可能影响最终写入集合。运行时错误也可能发生在若干项已成功之后。[2.5.1 批量循环][v2-batch]、[3.1.1 外置存储循环][v3-batch-external]、[内置存储循环][v3-batch-embedded]

响应应检查 `succCount`、`skipCount`、可选 `failData`、`skipData`、`unrecognizedCount` 和 `unrecognizedData`。`failData` 中的冲突项本身不计入 skipCount；ABORT 后尚未处理的项计入 skipCount。2.5.1 外层仍可能返回 `import success`，3.1.1 返回 Result.success，即使批量结果包含冲突。[v1 导入外层][v2-import]、[v3 导入外层][v3-inner-import]

新格式导入先要求 metadata 非空，每项 dataId/group/type 非空。正文路径必须按 `/` 切成两段；多层目录或 metadata 未声明的文件被列为未识别，metadata 声明但正文不存在的项也被列为未识别；其余项仍可导入。重复正文 key 在内存 Map 中后写覆盖前写，metadata 列表重复项未做独立拒绝。[2.5.1 新解析器][v2-new-parse]、[3.1.1 新解析器][v3-inner-parse]

格式兼容边界尤其需要实测入口：

- 2.5.1 和 3.1.1 **v1** 导入根据 `.metadata.yml` 与旧 `.meta.yml` 选择解析器。[2.5.1][v2-import]、[3.1.1][v3-v1-import]
- 3.1.1 **v3** 内置控制台与管理端直接调用新解析器；没有 metadata 时直接访问空对象，不能假定会返回友好的格式错误；旧 `.meta.yml` 也不会进入旧解析器。[内置控制台][v3-inner-import]、[管理端][v3-admin-import]
- 官方 v3.0 控制台文档曾声明兼容旧新导出包、未来可能移除旧格式；这是文档声明，不能覆盖上面的 **3.1.1 固定标签源码事实**。[官方文档](https://nacos.io/en/docs/v3.0/manual/admin/console-api/)

## 安全与健壮性边界

已看到的保护包括接口 READ/WRITE 权限注解、目标 namespace 存在性检查、新 metadata 必填字段检查，以及 v3 持久化层逐项 `ParamUtils.checkParam` 的名称/非空正文/最大内容长度检查。该长度用 Java 字符串长度与配置上限比较，不能理解为 ConfHub 的 UTF-8 字节限制。[v3 导入检查][v3-inner-import]、[批量检查][v3-batch-external]、[ParamUtils][param]

`ZipUtils` 全程在内存读取，未把条目解压到磁盘，因此不能仅凭路径含 `..` 就断言存在磁盘 Zip Slip。但该工具本身**没有显式 ZIP 总解压大小、条目数、压缩比或单条目大小上限，也没有路径规范化/重复名拒绝**；HTTP 上传层是否有额外限制取决于部署，不在本次源码片段结论内。工具只取遇到的首个元数据文件，两代 metadata 同包会受顺序影响；读 ZIP 的 IOException 会被记录并吞掉，可能返回已读部分，而不保证整包失败。[ZipUtils][zip]

YAML 使用 SafeConstructor 和自定义 ConfigMetadata 构造器，不是任意 Java 对象反序列化；但解析器依赖特定节点形状，不应把所有畸形 YAML 都视为可获得统一的业务错误。[YamlParserUtil][yaml]。加密配置导出会经过解密 handler，输出不能假定仍是数据库密文，ZIP 应按配置正文的敏感级别处理。[导出解密调用][v3-inner-export]

## 本次工作区样本

只读检查工作区中的 `nacos_config_export_20261109112754.zip`：共 13 个条目，包含 12 个配置正文与根目录 `.metadata.yml`；两个 group，路径与 metadata 的 group/dataId 一一对应；type 为 10 个 yaml 和 2 个 text。无扩展名的 dataId 也原样保留；metadata 无 namespace/tenant，appName/desc 为空或缺省。未记录配置正文或敏感字段值，未修改该包。这个样本印证新格式布局，不能单凭文件名确定来源版本。

## 对 ConfHub 的建议（尚未实现）

1. 优先兼容 `.metadata.yml` 新格式；如需旧格式，单独声明其 type/desc 丢失和 appName 解析限制。导出面向 Nacos 时使用新格式。
2. 显式选择目标 namespace，并映射 `Nacos group/dataId → ConfHub group/name`。ZIP 没有源 namespace，不能自动恢复；不要自动把旧环境空 ID 当作合法 ConfHub namespace。
3. 写入前预检全包：路径、重复 key、metadata 配对、UTF-8、配置数量、解压字节数和冲突。明确选择“整包事务”或“逐项成功”语义，不将 Nacos 的 ABORT 描述为自动回滚。
4. ConfHub 名称要求 1–128 UTF-8 字节，禁 `/`、反斜杠、NUL、CR/LF 和首尾空白；正文上限 1 MiB。先预检再呈现差异与失败明细。[现有名称与正文限制](../internal/config/types.go)
5. `type` 映射到 ConfHub format，`desc` 映射到 description；缺失/未知 type 应按明确规则选择 text 或拒绝，不能静默假定扩展名正确。当前支持 text/json/yaml/toml/xml/properties/ini。[现有格式校验](../internal/config/validation.go)
6. appName、configTags 与 SDK 灰度标签不是同一概念，不自动映射为 ConfHub 灰度规则；历史、beta、规则和权限需另行迁移，不能从此 ZIP 恢复。

## 固定标签源码索引

[v2-constants]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/constant/Constants.java#L115-L125
[v2-format-constants]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/constant/Constants.java#L281-L285
[v2-export]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/controller/ConfigController.java#L510-L600
[v2-import]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/controller/ConfigController.java#L614-L668
[v2-select]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/service/repository/extrnal/ExternalConfigInfoPersistServiceImpl.java#L1054-L1083
[v2-old-parse]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/controller/ConfigController.java#L680-L737
[v2-new-parse]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/controller/ConfigController.java#L749-L834
[v2-batch]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/controller/ConfigController.java#L906-L958
[v3-console]: https://github.com/alibaba/nacos/blob/3.1.1/console/src/main/java/com/alibaba/nacos/console/controller/v3/config/ConsoleConfigController.java#L305-L345
[v3-remote]: https://github.com/alibaba/nacos/blob/3.1.1/console/src/main/java/com/alibaba/nacos/console/handler/impl/remote/config/ConfigImportAndExportService.java#L79-L176
[v3-inner-export]: https://github.com/alibaba/nacos/blob/3.1.1/console/src/main/java/com/alibaba/nacos/console/handler/impl/inner/config/ConfigInnerHandler.java#L242-L270
[v3-inner-import]: https://github.com/alibaba/nacos/blob/3.1.1/console/src/main/java/com/alibaba/nacos/console/handler/impl/inner/config/ConfigInnerHandler.java#L274-L322
[v3-inner-parse]: https://github.com/alibaba/nacos/blob/3.1.1/console/src/main/java/com/alibaba/nacos/console/handler/impl/inner/config/ConfigInnerHandler.java#L334-L413
[v3-admin-import]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/controller/v3/ConfigControllerV3.java#L427-L509
[v3-v1-import]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/controller/ConfigController.java#L653-L702
[v3-batch-external]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/service/repository/extrnal/ExternalConfigInfoPersistServiceImpl.java#L337-L413
[v3-batch-embedded]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/service/repository/embedded/EmbeddedConfigInfoPersistServiceImpl.java#L345-L442
[v3-history]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/service/repository/extrnal/ExternalConfigInfoPersistServiceImpl.java#L128-L164
[zip]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/utils/ZipUtils.java#L102-L154
[metadata]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/model/ConfigMetadata.java#L27-L41
[yaml]: https://github.com/alibaba/nacos/blob/2.5.1/config/src/main/java/com/alibaba/nacos/config/server/utils/YamlParserUtil.java#L55-L139
[param]: https://github.com/alibaba/nacos/blob/3.1.1/config/src/main/java/com/alibaba/nacos/config/server/utils/ParamUtils.java#L54-L100
[v2-namespace]: https://github.com/alibaba/nacos/blob/2.5.1/common/src/main/java/com/alibaba/nacos/common/utils/NamespaceUtil.java#L31-L51
[v3-namespace]: https://github.com/alibaba/nacos/blob/3.1.1/common/src/main/java/com/alibaba/nacos/common/utils/NamespaceUtil.java#L36-L52
[v3-public]: https://github.com/alibaba/nacos/blob/3.1.1/api/src/main/java/com/alibaba/nacos/api/common/Constants.java#L191
