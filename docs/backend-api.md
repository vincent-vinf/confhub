# ConfHub 后端协议 v1

所有 JSON 使用 UTF-8。单份正文上限 1 MiB；版本号、修订号和流序号均为整数，但含义不同。管理接口与客户端接口共享一个 Gin HTTP 服务。

## 管理认证

`POST /api/admin/login` 提交 `{"username":"admin","password":"…"}`，获得默认 2 小时有效的 HttpOnly、SameSite=Strict Cookie。Cookie 路径为 `/api/admin`。所有管理接口均要求此 Cookie，登录除外。

所有管理写入（包括登录）要求同源 `Origin` 或 `Referer`；命令行调用也应发送 `Origin: http://host:port`。HTTPS 代理部署设置 `--cookie-secure=true`，并保留外部 Host。后端不信任任意转发头。

- `POST /api/admin/logout`：清除浏览器 Cookie，返回 204。
- `POST /api/admin/password`：`{"old_password":"…","new_password":"…"}`。密码为 8–72 字节。修改密码不撤销已签发 JWT。

## 组织与配置

| 方法 | 路径（均在 `/api/admin` 下） | 用途 |
| --- | --- | --- |
| GET / POST | `/namespaces` | 列出 / 创建命名空间，创建正文为 `{"name":"…"}` |
| DELETE | `/namespaces/:namespace` | 删除空命名空间 |
| GET / POST | `/namespaces/:namespace/groups` | 列出 / 创建分组 |
| DELETE | `/namespaces/:namespace/groups/:group` | 删除空分组 |
| GET | `/namespaces/:namespace/groups/:group/configs` | 配置列表，支持 `after` 名称游标、`limit`（默认 100，最大 200） |
| GET / PUT / DELETE | `/namespaces/:namespace/groups/:group/configs/:name` | 读取编辑状态 / 保存发布 / 永久删除 |

下文用 `CONFIG` 表示一份配置的完整管理路径。

保存正文：

```json
{
  "expected_id": "配置身份；首次创建省略",
  "expected_revision": 7,
  "content": "port: 8080\n",
  "format": "yaml",
  "description": "可选说明",
  "rule_id": "可选；指定时只更新对应灰度目标",
  "confirmed": true
}
```

首次创建修订号为 0。后续保存必须携带 GET 返回的 `id` 与 `revision`。全量和灰度共享配置修订号，任一并发变更都会要求重新比较、确认。前端切换历史 diff 对象不能替换这两个字段。

保存、规则修改、回退、提升和删除要求 `confirmed:true`；这是提交协议，正文 diff 由前端展示。创建命名空间/分组无需正文 diff。删除组织同样要求确认。

保存返回 `{"state":{…},"changed":true,"sequence":42}`。正文与当前目标原文相同返回 `changed:false`、`sequence:0`，不生成版本或日志事件。此时不修改描述或格式；若需要修改声明格式，必须同时修改正文。

`GET CONFIG` 返回身份、修订、全量版本、版本计数、规则列表和当前全量/规则目标的正文。`versions` 按版本号索引。版本正文不可修改。

声明格式支持 `text/json/yaml/toml/xml/properties/ini`。发布前服务器校验语法，不展开应用占位符或转换正文。名称为 1–128 字节，不能包含斜杠或首尾空白。

## 历史、回退与灰度

| 方法 | 路径 | 正文 / 参数 |
| --- | --- | --- |
| GET | `CONFIG/versions` | `before` 为排他版本游标，`limit` 默认 100，最大 100；返回元数据及全量/规则引用关系 |
| GET | `CONFIG/versions/:version` | 返回包含正文的单份历史版本 |
| POST | `CONFIG/rollback` | `expected_id/expected_revision/source_version/rule_id?/confirmed` |
| POST | `CONFIG/promote` | `expected_id/expected_revision/source_version/confirmed`，目标为全量 |
| PUT | `CONFIG/rules` | `expected_id/expected_revision/confirmed/rules`，原子替换整个有序列表 |
| POST | `CONFIG/simulate` | `{"tags":{"env":"gray"}}`，返回命中规则与有效配置 |

规则结构：

```json
{
  "id": "客户端生成的唯一规则 ID，最多 36 字节",
  "name": "规则名称",
  "enabled": true,
  "target_version": 3,
  "conditions": [{"tag":"env","operator":"in","values":["gray","staging"]}]
}
```

列表先后顺序就是匹配顺序；规则内部使用 AND。`eq` 只接受一个值，`in` 接受值集合；缺失标签不匹配。规则最多 100 条，每条最多 32 个条件。停用规则继续保留目标版本引用。新增、排序、停用和删除都通过替换列表完成。删除一条规则后继续匹配后面的规则。

回退复制历史正文生成新版本，自动描述来源；灰度回退只移动指定规则目标。提升灰度内容为全量也生成新版本。与当前目标正文完全相同的操作不产生重复版本。全量修改不改变固定灰度目标。

## 客户端读取

`GET /api/client/config?namespace=public&group=DEFAULT_GROUP&name=service&tags=…`

`namespace/group` 省略时采用 `public/DEFAULT_GROUP`。`tags` 为 URL 编码的 JSON 字符串映射，如 `{"sys.ip":"10.0.0.1","sys.hostname":"node-a","env":"gray"}`。服务器不鉴权；客户端标签仅用于路由。内置标签由 SDK 采集，连接来源地址不等同于 `sys.ip`。

有效状态结构：

```json
{
  "sequence": 42,
  "id": "配置实例 UUID",
  "key": {"namespace":"public","group":"DEFAULT_GROUP","name":"service"},
  "revision": 7,
  "version": 3,
  "content": "port: 8080\n",
  "format": "yaml",
  "rule_id": "命中时返回",
  "deleted": false
}
```

不存在时 HTTP 404 返回 `deleted:true`、业务键与读取水位。身份和流序号用于抵御晚到结果；灰度版本号可能下降，同名重建版本号从 1 开始。HTTP/推送可在正常传播窗口内暂时读取旧缓存。

## WebSocket

连接 `/api/client/watch?tags=…`，同一连接固定使用此标签上下文，最多订阅 10 份配置。无需 Cookie 或令牌；携带浏览器 Origin 时必须同源。

```json
{"op":"subscribe","key":{"namespace":"public","group":"DEFAULT_GROUP","name":"service"}}
```

注册订阅后立即推送当前有效状态；不存在也明确推送删除状态。消息直接使用上述有效状态结构，没有额外 envelope。后续只有有效版本或存在状态改变时推送完整正文。连续变更允许合并为最终状态，每个配置最多保留一个待发送快照。

```json
{"op":"unsubscribe","key":{"namespace":"public","group":"DEFAULT_GROUP","name":"service"}}
```

重复 subscribe 不新增订阅；unsubscribe 不要求配置仍存在。非法操作、非法标签或超出订阅限制关闭连接。协议使用 WebSocket Ping/Pong，每 10 秒 Ping、30 秒未响应关闭；客户端需持续读取并响应协议心跳。写入超过 5 秒关闭慢连接。

断线后客户端重新建立全部订阅，以首次快照补偿；服务端不重放每个历史版本。明确删除应清除客户端缓存，连接失败保留最后成功配置。SDK 的缓存与重连实现和用法见 [sdk.md](sdk.md)。

## 健康、错误与静态目录

- `/health/live`：进程响应返回 200。
- `/health/ready`：数据库同步正常返回 200，否则 503；启动同步尚未完成时为 503。
- 管理端 400：校验、缺少确认、组织非空；401：认证失败；403：来源不符；404：不存在；409：乐观锁冲突；500：内部故障。
- 客户端同步不可用返回 503。连续数据库同步失败默认 2 秒后关闭已有会话，恢复时重建缓存和消费水位。
- React 静态目录通过 flag/env 提供；页面路由可回退到 index.html，配置详情页中的 `.json`/`.yaml` 等名称同样支持直接访问与刷新。未知 API 和缺失资源返回 404。应用镜像在构建阶段生成并复制 React 页面，由 Go 直接提供。
