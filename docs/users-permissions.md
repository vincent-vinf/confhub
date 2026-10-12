# 多用户与管理权限

权限覆盖控制台及 `/api/admin` 管理 API。客户端 HTTP GET 和 WebSocket 保持无需鉴权，用户授权不约束 SDK 的有效配置读取。客户端凭据与订阅鉴权留待单独实现。

## 授权模型

用户包含不可变用户名、启用状态、系统管理员标记、客户端查看权限和配置范围授权。不提供公开注册、自定义角色、用户组或单份配置授权。

| 权限 | 操作 | 范围 |
| --- | --- | --- |
| config.read | 列表、全量/beta 正文、规则、历史、diff、匹配模拟 | 授权的 namespace/group |
| config.write | 创建、保存发布、beta 编辑、规则变更、回退、转全量、删除和移动；包含读取 | 授权的 namespace/group |
| clients.read | 全部在线连接、订阅信息、标签名和值补全 | 全局，可单独授予普通用户 |
| organization.manage | 创建/删除 namespace/group，仍只删除空组织 | 系统管理员 |
| audit.read | 全部管理操作审计、筛选及详情 | 系统管理员 |
| users.manage | 创建、更新权限、启停、重置密码和删除用户 | 系统管理员 |
| 本人账号 | 当前用户信息、本人改密和退出 | 已登录用户 |

配置授权用 `{namespace,group,access}` 表示，access 为 read/write。空 namespace 和空 group 表示全部空间；指定 namespace、空 group 表示该空间所有现有及未来分组；两者均指定表示一个分组。namespace 为空时 group 必须为空。名字按精确字符串匹配，`*` 没有通配语义。同一范围不接受重复授权，最多 200 条；多个范围取并集，默认拒绝。

例如：

```json
{
  "enabled": true,
  "system_admin": false,
  "clients_read": false,
  "grants": [
    {"namespace":"dev","group":"","access":"write"},
    {"namespace":"prod","group":"apps","access":"read"}
  ]
}
```

组织和配置列表只返回可读范围。批量删除检查全部来源；移动检查全部来源与目标的写权限，任一项拒绝则不执行任何变更。匹配模拟为只读 POST，不按 HTTP 方法粗略区分权限。

客户端连接可能包含多个范围的订阅，标签属于连接，因此 clients.read 单独授予全局权限。无此权限仍可手动输入灰度标签，内置标签名仍可建议，不能读取在线标签值。

系统管理员拥有全部权限。初始化的 admin 禁止删除、禁用或降级，保证始终有可用管理员；可以修改其密码。其他管理员由已有管理员创建，不可修改用户名。授权按 namespace/group 名字绑定，删除组织不删除授权，重建同名组织会继续匹配原授权。

## 认证及一致性

继续使用默认 2 小时的 HS256 JWT、HttpOnly/SameSite=Strict Cookie 和同源写入检查，无刷新令牌及会话表。JWT sub 为用户 ID，并携带 auth_version。每个管理请求从数据库事务内读取一致的用户及授权快照，禁用用户或版本不符返回 401，无权限返回 403。

权限更新在事务提交后，各实例下一次请求使用新权限；已开始的请求使用其进入鉴权时的快照。本人改密、管理员重置密码和启停用户会递增 auth_version，旧令牌在所有实例失效，重新启用不会恢复旧令牌。本人改密后清除 Cookie，要求重新登录；普通权限变更无需重新登录。退出仅清除本浏览器 Cookie。

用户资料有内部 revision；更新、重置密码和删除携带 expected_revision，并发冲突返回 409。授权替换和账号更新在同一事务提交，不向前端展示 revision。密码为 8–72 字节，使用 bcrypt；用户名为 1–64 个 ASCII 字母、数字、下划线、点或连字符，首字符必须是字母或数字，区分大小写。

控制台登录时获取当前身份，之后每 15 秒以及窗口恢复焦点时刷新；权限变化会刷新组织和配置查询。前端隐藏未授权入口，后端独立检查直接请求。

## 管理 API

路径均以 `/api/admin` 为前缀，用户操作要求同源请求。

| 方法 | 路径 | 正文及响应 |
| --- | --- | --- |
| GET | /me | 当前 User，仅需登录 |
| GET | /users | `after` 用户名游标，limit 默认 25、最大 100；返回 User 数组 |
| POST | /users | username/password 及 enabled/system_admin/clients_read/grants；返回 User |
| PUT | /users/:id | enabled/system_admin/clients_read/grants、expected_revision、confirmed:true；返回 User |
| POST | /users/:id/password | password、expected_revision、confirmed:true；返回 changed:true |
| DELETE | /users/:id | expected_revision、confirmed:true；返回 deleted:true |

User 返回 id、username、enabled、system_admin、clients_read、revision、grants；不返回密码摘要或认证版本。PUT 原子替换全部账号属性与授权，省略布尔值为 false、省略 grants 为清空授权。POST 创建时同样默认禁用且无授权；控制台默认启用、配置权限为空，由管理员选择授权。上述用户管理接口仅系统管理员可调用。

## 数据库升级

schema 5 新增 users/user_grants，复制 schema 4 的 admin 密码摘要并移除旧 admin 表。配置、历史、beta 和规则不变。当前空数据库初始化自动迁移至 schema 6（增加审计表）；已有 schema 4/5 需要停止旧实例，使用新二进制执行 `confhub migrate`，再启动新实例。旧 admin JWT 缺少用户身份和认证版本，升级后需要重新登录。

schema 1/2/3 仍拒绝迁移，按此前策略重建。PostgreSQL/MySQL 均提供 migrate 文件；集成验证只使用 PostgreSQL。
