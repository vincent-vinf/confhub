# Nacos 配置导入导出实现与验收

日期：2026-11-09。规格：[导入导出](nacos-import-export-spec.md)；决策：[整包事务](adr/0009-atomic-configuration-import.md)。

## 实现

- 配置列表提供导出选中、按当前 namespace/group/搜索导出全部；选中仍限当前页。读取用户及只读/edge 实例可导出，移动/删除和导入分别保持写权限。
- 导入选择 ZIP、现有 namespace 和冲突策略，展示新建/覆盖/跳过/不变/冲突及逐项错误。统一确认后发布。错误包或并发变更不写入任何配置、历史、分组或事件；修改目标、文件或策略都须重新预览。
- `.metadata.yml` 与正文一一对应，保留名称、原文、换行及描述；appName 忽略/导出为空。只交换当前全量，不交换历史、beta 或规则。覆盖复用普通保存，保留现有 beta/规则；相同正文和格式不创建重复版本。
- 预览无实例内草稿，确认可由另一实例处理。提交重新解析、授权并检查身份/修订；共用保存事务逻辑，提交后才唤醒订阅。未新增表结构迁移或启动参数。
- 归档解压/条数/正文/上传上限与规格一致。格式错误消息不回显解析器携带的正文片段。审计记录操作者、IP、时间、策略、目标分组和结果统计，不记录 ZIP/正文。

接口及字段见 [后端协议](backend-api.md#nacos-zip-导入导出)。

## 验证

- 分层运行器：`python3 tests/run.py --coverage-gates --report-dir test-results/nacos-import-export-20261109`。单元、真实 PostgreSQL/race、vet、Go/Python SDK、两种系统运行方式、覆盖率、前端单测/格式均通过。
- 新增系统套件 `archive` 在三个真实实例上，通过 Go/Python 正式 SDK 验证导入后普通和灰度 GET/通知、并发冲突及重试、三种策略、不变发布和筛选导出；隔离与连接模式均通过。
- 完整浏览器首轮 32/33：新增读取用户勾选断言假定目标配置在第一页，全套积累超过 25 条后找不到目标。trace 确认后改为先搜索目标，不增加超时；随后执行 `python3 tests/run.py --layers frontend --report-dir test-results/nacos-import-export-frontend-final-20261109`，typecheck、前端单测/格式和完整浏览器 **33/33** 全部通过；明暗窄屏截图已检查。
- 审核后的归档/存储/HTTP 专项 race 回归通过，包含 8 个并发导入仅一个成功、删除重建、回滚新分组与事件、错误汇总、审计和只读限制。
- `make test-nacos-interop` 使用临时 Nacos 2.5.1 与 PostgreSQL，通过真实 HTTP API 双向验证：6 种 Nacos 共有格式导出到 ConfHub；ConfHub 7 种格式（含 INI）导出到 Nacos，逐项比较正文/CRLF/格式/描述。注意 Nacos 普通发布 API 会将 INI 改成 text，而其 ZIP 导入保留 ini，因此单独验证出站 INI。独立脚本在审核修复后再次通过，自动删除测试容器。
- 工作区用户示例仅只读验证，共 12 份配置通过；没有输出正文，没有纳入提交。全部可提交测试均使用虚构数据。

覆盖率（整套初次运行，后续修复另有专项回归）：后端维护代码 83.62%，配置业务 100%，Go SDK 87.12%，Python SDK 91.56%，全部超过现有门槛。生成的 protobuf 单独统计，不计维护代码门槛。原始日志仅在忽略的 test-results 下。

## Standards

首次发现额外的 metadata 2 MiB 上限，修复并增加 600 项 × 4096 字节描述用例。复核发现底层 INI 错误可能携带原文，改为归档层固定校验消息，并增加虚构敏感标记不回显用例。再次审核通过，无剩余规范或安全发现。

## Spec

首次发现 metadata 额外上限、逐项预览错误不完整、审计缺少包内分组。已分别修复并增加归档/PG/HTTP 验证，前端同步呈现逐项原因；提交校验和权限未放宽。再次审核通过，无剩余需求发现。

两轴审核基于起点 `728a73a22ffdeac7b5591b5210c88f8fb38f95bd`：Standards 2 项、Spec 3 项，全部已解决。
