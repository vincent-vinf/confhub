# ConfHub Python SDK

Python 3.10+。提供 `Client` 和 `AsyncClient`，支持配置获取、订阅、重连、多地址故障切换和可选磁盘缓存。

```sh
python -m pip install ./sdk/python
```

```python
from confhub import Client, Key

with Client(["http://localhost:8080"]) as client:
    value = client.get(Key("service.yaml"))
    print(value.content)
```

完整示例与回调、缓存语义见仓库 [docs/sdk.md](../../docs/sdk.md)。
