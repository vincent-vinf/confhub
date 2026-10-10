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
  "target": "beta",
  "confirmed": true
}
```

target 省略或为 `global` 时全量保存，`beta` 时覆盖唯一 beta；不接受旧 `rule_id` 字段。首次创建修订号为 0。后续保存必须携带 GET 返回的 `id` 与 `revision`。全量和灰度共享配置修订号，任一并发变更都会要求重新比较、确认。全量编辑切换主历史 diff 对象不能替换这两个字段；灰度仅提供当前内容与编辑内容的 diff。

保存、规则修改、回退、提升和删除要求 `confirmed:true`；这是提交协议，正文 diff 由前端展示。创建命名空间/分组无需正文 diff。删除组织同样要求确认。

保存返回 `{"state":{…},"changed":true,"sequence":42}`。全量正文和格式均相同返回 `changed:false`、`sequence:0`，不创建版本或事件，也不修改主版本描述；仅改格式可以发布新主版本。灰度正文、格式或描述有变化则原位覆盖 beta、递增修订并写入变更事件；不新增主版本或灰度历史，仅改描述不推送客户端。

`GET CONFIG` 返回身份、修订、全量版本、版本计数、规则列表和当前全量正文与配置级唯一 beta 正文。`versions` 只包含当前全量主版本，按编号索引；主版本正文不可修改。beta 保存在 `state.beta`，可覆盖且不引用 `versions`。

声明格式支持 `text/json/yaml/toml/xml/properties/ini`。发布前服务器校验语法，不展开应用占位符或转换正文。名称为 1–128 字节，不能包含斜杠或首尾空白。

## 历史、回退与灰度

| 方法 | 路径 | 正文 / 参数 |
| --- | --- | --- |
| GET | `CONFIG/versions` | `before` 为排他版本游标，`limit` 默认 100，最大 100；仅返回主历史元数据及当前全量标识 |
| GET | `CONFIG/versions/:version` | 返回包含正文的单份历史版本 |
| POST | `CONFIG/rollback` | `expected_id/expected_revision/source_version/confirmed`，仅全量，禁止 `rule_id` |
| POST | `CONFIG/promote` | `expected_id/expected_revision/confirmed`，读取唯一 beta 发布为全量，禁止 `rule_id` 和非零 `source_version` |
| PUT | `CONFIG/rules` | `expected_id/expected_revision/confirmed/rules/source_version`，原子替换整个有序列表；source_version 仅首次创建 beta 必填 |
| POST | `CONFIG/simulate` | `{"tags":{"env":"gray"}}`，返回命中规则与有效配置 |

规则列表更新的输入结构（仅元数据，不接受 `target_version` 或 `beta`）：

```json
{
  "id": "客户端生成的唯一规则 ID，最多 36 字节",
  "name": "规则名称",
  "enabled": true,
  "conditions": [{"tag":"env","operator":"in","values":["gray","staging"]}]
}
```

列表先后顺序就是匹配顺序；规则内部使用 AND。`eq` 只接受一个值，`in` 接受值集合，`ip_range` 接受两个有序的完整 IP 地址；缺失标签不匹配。规则最多 100 条，每条最多 32 个条件。停用规则保留共享 beta，重新开启继续使用。新增、排序、停用和删除都通过替换列表完成。删除一条规则后继续匹配后面的规则。

首次创建规则时提交顶层 `source_version`（正整数），服务端在同一事务内复制该保留主版本的正文及格式。缺少来源返回 400，不存在返回 404；已有 beta 时不得提交非零来源。规则仅有元数据，配置读取额外返回顶层：

```json
"beta": {
  "content": "port: 8081\n",
  "format": "yaml",
  "description": "灰度说明",
  "updated_at": "2026-10-10T12:00:00Z"
}
```

名称固定 beta，无创建来源编号。所有规则共享它，删除最后一条规则清除 beta；后续新增重新选择复制来源。beta 不占主历史配额，清理来源主历史不影响内容。

回退仅复制主历史正文生成下一个主版本，自动描述来源。灰度转全量读取唯一 beta，生成下一个主版本，保留规则和 beta；全量编辑不改变 beta。转全量主版本的 action 为 promote、source_version 内部为 0（JSON 可省略），界面来源显示 beta。正文与格式均相同时不产生重复主版本。

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
  "version": 0,
  "beta": true,
  "content": "port: 8080\n",
  "format": "yaml",
  "rule_id": "命中时返回",
  "deleted": false
}
```

不存在时 HTTP 404 返回 `deleted:true`、业务键与读取水位。身份和流序号抵御晚到结果；灰度使用 `beta:true/version:0`、非空 rule_id，全量使用 `beta:false/version:N`（N 为正整数）。同名 beta 原位编辑仍根据正文、格式等变化通知；同名配置重建从主版本 1 开始。HTTP/推送可在正常传播窗口内暂时读取旧缓存。

## WebSocket

连接 `/api/client/watch?tags=…`，同一连接固定使用此标签上下文，最多订阅 10 份配置。无需 Cookie 或令牌；携带浏览器 Origin 时必须同源。

```json
{"op":"subscribe","key":{"namespace":"public","group":"DEFAULT_GROUP","name":"service"}}
```

注册订阅后立即推送当前有效状态；不存在也明确推送删除状态。消息直接使用上述有效状态结构，没有额外 envelope。后续配置身份、有效版本、beta 标识、命中规则、正文、格式或存在状态改变时推送完整正文；配置级修订变化但有效内容未受影响时不推送。连续变更允许合并为最终状态，每个配置最多保留一个待发送快照。

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

## 数据库模型边界

当前 schema 4 仅支持空数据库初始化，不兼容 schema 1/2/3。启动和 migrate 均拒绝旧 schema，要求使用新的空数据库或由使用者重建，不自动删除或转换旧数据。

## 在线客户端（只读管理接口）

- `GET /api/admin/clients?after=<连接ID>&limit=25`：要求 admin 登录，返回 `{clients:[],next_after?:string}`。按连接 ID 游标分页，limit 1–100。连接 ID 每次重连重新生成，不代表机器身份。
- 每个客户端包含 `id/instance_id/source_address/connected_at/refreshed_at/tags/subscriptions`。`subscriptions` 项包含 `key/id/version/revision/beta/rule_id/deleted/sent`，没有配置正文。`sent:false` 表示尚未成功发送；`sent:true` 表示最近一次成功的 WebSocket 写入，不代表应用确认。beta 显示为 beta，规则 ID 仅说明首条匹配条件。
- `GET /api/admin/client-tags?tag=<标签名>&prefix=<前缀>`：返回所有有效实例中对应标签的去重值。省略 tag 返回标签名，始终包含符合前缀的 `sys.ip/sys.hostname`。大小写、空字符串和尾部空格保持字符串语义；prefix 为字面前缀（`%/_` 不是通配符），最多 100 条。继续输入前缀可发现初始列表之外的值。
- 在线仅指成功升级的 WebSocket 连接，包含暂未订阅的连接；HTTP GET 不记录。标签由客户端传入，远端地址单独展示。
- 实例每 5 秒同步连接快照，仅写入变化连接，数据库时钟租约 20 秒。正常断连下次同步移除；异常节点过期即不再查询到，每分钟分批清理。网页每 5 秒读取，正常断连约 10 秒内从页面移除。没有远程断开、修改标签等管理操作。

IP 区间条件示例：

```json
{"tag":"sys.ip","operator":"ip_range","values":["192.168.2.1","192.168.2.5"]}
```

包含两个端点，按 IP 地址数值比较；支持 IPv4/IPv6，同族且无 zone，起始不得大于结束。使用完整地址，缺失标签或非 IP 值不命中；可用于其他保存 IP 的标签。IPv4 映射 IPv6 地址统一按 IPv4 比较。保存、HTTP GET、WebSocket 和路由模拟使用同一解析逻辑。
