# Go / Python SDK

SDK 只读取配置，不使用 admin 账号或 JWT。需要提前在控制台发布配置。

## Go

独立模块位于 `sdk/go`，仅依赖 gorilla/websocket，不依赖服务端内部包。当前代码尚未推送或打版本标签，可在消费项目中本地接入：

```sh
go mod edit -require=gitlab.bodesitech.com/bodesi/confhub/sdk/go@v0.0.0
go mod edit -replace=gitlab.bodesitech.com/bodesi/confhub/sdk/go=/path/to/confhub/sdk/go
go mod tidy
```

```go
package main

import (
    "context"
    "fmt"
    "time"

    confhub "gitlab.bodesitech.com/bodesi/confhub/sdk/go"
)

func main() {
    client, err := confhub.New(confhub.Options{
        Addresses: []string{"http://192.168.20.101:8080"},
        Tags: map[string]string{"env": "production"},
        CacheDir: "./confhub-cache", // 省略时只使用内存缓存
    })
    if err != nil { panic(err) }
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
        defer cancel()
        if err := client.Close(ctx); err != nil { fmt.Println(err) }
    }()

    key := confhub.Key{Name: "service.yaml"} // 默认 public / DEFAULT_GROUP
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    value, err := client.Get(ctx, key)
    cancel()
    if err != nil { panic(err) }
    fmt.Println(value.Content, value.Version, value.Format, value.Source)

    lifetime, stop := context.WithCancel(context.Background())
    defer stop()
    if err := client.Subscribe(lifetime, key, func(ctx context.Context, value confhub.Snapshot) {
        if value.Deleted {
            fmt.Println("配置已删除")
            return
        }
        // 应用自行解析正文并替换自己的配置；耗时操作需响应 ctx.Done()。
        fmt.Println("配置更新", value.Version)
    }); err != nil { panic(err) }
    // 应用正常运行期间保持 client 与 lifetime 存活。
    time.Sleep(time.Minute)
    if err := client.Unsubscribe(key); err != nil { panic(err) }
}
```

`Get` 的错误可用 `errors.Is` 判断 `ErrNotFound`、`ErrUnavailable`、`ErrClosed` 和 context 取消。不存在时返回删除快照及 `ErrNotFound`。`Close(ctx)` 取消所有订阅、关闭连接，并在 context 时限内等待后台任务与回调退出；回调必须响应其 context，不要在回调里等待 `Close` 完成。异步缓存/回调异常通过 `client.Errors()` 的有界通道报告。

## Python

代码位于 `sdk/python`，要求 Python 3.10+，网络依赖为 aiohttp：

```sh
python -m pip install ./sdk/python
# 开发与验证：
uv venv sdk/python/.venv
uv pip install --python sdk/python/.venv/bin/python -e './sdk/python[dev]'
```

同步接口：

```python
from confhub import Client, Key, NotFound, Unavailable

def changed(value):
    if value.deleted:
        print("配置已删除")
    else:
        print("配置更新", value.version, value.content)

with Client(
    ["http://192.168.20.101:8080"],
    tags={"env": "production"},
    cache_dir="./confhub-cache",
) as client:
    key = Key("service.yaml")
    value = client.get(key)
    print(value.content, value.version, value.format, value.source)
    client.subscribe(key, changed)
    input("按 Enter 停止订阅\n")
    client.unsubscribe(key)
```

原生 asyncio 接口：

```python
import asyncio
from confhub import AsyncClient, Key

async def changed(value):
    if value.deleted:
        print("配置已删除")
    else:
        print("配置更新", value.version)

async def main():
    async with AsyncClient(
        ["http://192.168.20.101:8080", "http://192.168.20.102:8080"],
        tags={"env": "production"},
        cache_dir="./confhub-cache",
    ) as client:
        key = Key("service.yaml", namespace="public", group="DEFAULT_GROUP")
        value = await client.get(key)
        print(value.content)
        await client.subscribe(key, changed)
        await asyncio.sleep(60)
        await client.unsubscribe(key)

asyncio.run(main())
```

异步客户端在首次使用的 event loop 上运行。同步入口拥有独立后台 event loop，不调用 `asyncio.run`，可在已有 event loop 的程序中构造；若在协程内调用同步方法，使用 `asyncio.to_thread`，避免阻塞应用线程。同步和异步回调都支持：同步回调在工作线程执行，异步回调在 SDK event loop 执行。回调与网络接收独立，异步回调应使用异步 I/O，同步回调应及时返回。关闭会取消回调任务；已经执行中的同步函数无法被 Python 强制终止。

Python 不存在抛出 `NotFound`（带 `snapshot`），无服务器且无缓存抛出 `Unavailable`，关闭后抛出 `Closed`。异步缓存/回调异常可从 `AsyncClient.errors` 获取。

## 共同语义

- 原文保持不变，返回实例 ID、配置键、流序号、编辑修订、版本、格式、命中规则和删除状态。来源 `online` / `memory` / `disk` 表示当前读取来自服务端或离线缓存。
- 自动采集 `sys.ip` 和 `sys.hostname`，可在 `Tags` / `tags` 显式覆盖。多网卡、容器和 NAT 场景建议指定希望参与灰度路由的地址。
- 同一客户端标签固定。切换标签请建立新客户端；缓存按规范化后的服务地址集合、标签及完整配置键隔离。地址应指向共享同一数据库的集群。
- 每个客户端最多订阅十份配置，每个配置只有一个回调。首次订阅推送当前状态；重连后重新注册全部订阅。同一客户端最多一条有效 WebSocket 连接；新增或取消订阅会重建连接。
- 正常服务故障先切换地址；全部失败后约 500ms 起指数退避，加随机抖动，最大 30s。使用服务端 Ping/Pong 检测断线。每个地址的 HTTP/握手时限默认为 2s。
- 回调只在有效版本、配置身份或存在状态变化时触发。快速变更或慢回调可能合并中间状态；保证收敛到最新状态，不提供每个历史版本的事件队列。
- 按流序号及实例身份抵御晚到 HTTP/旧连接结果，允许灰度版本号下降。明确删除立即清除正文及磁盘文件，保留内存中的删除水位；重建后的版本 1 使用新身份。
- 获取总是先访问服务端，连接失败才回退缓存；明确的删除不会回退旧正文。首次启动断网且无缓存明确报错。返回的磁盘快照也建立排序水位，晚到的较旧在线查询不能覆盖它。
- 磁盘缓存可选，文件权限 0600，目录权限 0700，使用临时文件加原子替换。SDK 不解析、合并或注入应用配置，也不加密缓存；缓存目录应由应用自身管理。
- 同一缓存目录和地址/标签组合由一个活跃客户端管理；多个进程或客户端应各自使用独立缓存目录，避免彼此覆盖缓存文件。

## 测试

开发依赖安装后，可从仓库根目录运行 `make sdk-test sdk-check` 和 `make sdk-integration`。

```sh
cd sdk/go && go test -race ./... && go vet ./...
cd ../python
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/mypy src/confhub
.venv/bin/ruff check src tests
```

单独运行时，真实服务测试会明确 skip。以下命令创建临时 PostgreSQL 与两个 ConfHub 进程，不读真实 `.env`、不写 `data/`，并在退出时清理：

```sh
python3 sdk/test-integration.py
# SDK 与服务端完整测试、race、vet：
python3 sdk/test-integration.py --full
```

性能目标尚未压测；SDK 尚未发布到包索引或远程版本标签。
