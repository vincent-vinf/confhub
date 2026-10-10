"""Native asyncio transport for the public ConfHub protocol."""

import asyncio
import inspect
import json
import os
import random
import socket
from collections.abc import Awaitable, Callable, Mapping, Sequence
from dataclasses import dataclass
from types import TracebackType
from typing import Protocol
from urllib.parse import urlsplit

import aiohttp

from .cache import DiskCache
from .types import MAX_WIRE_BYTES, Closed, Key, NotFound, Snapshot, Unavailable

Callback = Callable[[Snapshot], Awaitable[None] | None]


class _WatchConnection(Protocol):
    async def close(self) -> bool: ...


@dataclass
class _Subscription:
    callback: Callback
    pending: asyncio.Queue[Snapshot]
    last: Snapshot | None = None
    worker: asyncio.Task[None] | None = None


def _local_ip() -> str:
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as connection:
            connection.connect(("192.0.2.1", 9))
            return str(connection.getsockname()[0])
    except OSError:
        return "127.0.0.1"


class AsyncClient:
    def __init__(
        self,
        addresses: Sequence[str],
        *,
        tags: Mapping[str, str] | None = None,
        timeout: float = 2.0,
        cache_dir: str | os.PathLike[str] | None = None,
    ) -> None:
        if isinstance(addresses, str) or not addresses or timeout <= 0:
            raise ValueError("server addresses and a positive timeout are required")
        normalized = []
        for address in addresses:
            parsed = urlsplit(address)
            if (
                parsed.scheme not in ("http", "https")
                or not parsed.netloc
                or parsed.username is not None
                or parsed.query
                or parsed.fragment
            ):
                raise ValueError("invalid server address")
            normalized.append(address.rstrip("/"))
        context = {"sys.hostname": socket.gethostname(), "sys.ip": _local_ip()}
        context.update(tags or {})
        if len(context) > 64 or any(
            not isinstance(key, str)
            or not isinstance(value, str)
            or not key
            or len(key.encode()) > 128
            or len(value.encode()) > 512
            for key, value in context.items()
        ):
            raise ValueError("invalid routing tags")
        self._addresses = tuple(normalized)
        self._tags = json.dumps(context, sort_keys=True, ensure_ascii=False)
        self._timeout = timeout
        self._session: aiohttp.ClientSession | None = None
        self._closed = False
        self._loop: asyncio.AbstractEventLoop | None = None
        self._records: dict[Key, Snapshot] = {}
        self._disk = DiskCache(cache_dir, self._addresses, self._tags)
        self._lock = asyncio.Lock()
        self.errors: asyncio.Queue[Exception] = asyncio.Queue(maxsize=1)
        self._subscriptions: dict[Key, _Subscription] = {}
        self._ws: _WatchConnection | None = None
        self._watcher: asyncio.Task[None] | None = None
        self._generation = 0
        self._wake = asyncio.Event()

    async def _transport(self) -> aiohttp.ClientSession:
        if self._closed:
            raise Closed("client closed")
        loop = asyncio.get_running_loop()
        if self._loop is not None and self._loop is not loop:
            raise RuntimeError("AsyncClient must be used on its original event loop")
        self._loop = loop
        if self._session is None:
            self._session = aiohttp.ClientSession(
                timeout=aiohttp.ClientTimeout(total=self._timeout)
            )
        return self._session

    async def get(self, key: Key) -> Snapshot:
        session = await self._transport()
        params = {**key.to_dict(), "tags": self._tags}
        for address in self._addresses:
            try:
                async with session.get(address + "/api/client/config", params=params) as response:
                    if response.status not in (200, 404):
                        continue
                    raw = bytearray()
                    async for chunk in response.content.iter_chunked(65536):
                        raw.extend(chunk)
                        if len(raw) > MAX_WIRE_BYTES:
                            raise ValueError("configuration response too large")
                    value = Snapshot.from_dict(json.loads(raw))
                    if value.key != key or value.deleted != (response.status == 404):
                        raise ValueError("configuration response mismatch")
            except (aiohttp.ClientError, asyncio.TimeoutError, ValueError, OSError):
                continue
            value = await self._accept(value)
            if value.deleted:
                raise NotFound(value)
            return value
        if self._closed:
            raise Closed("client closed")
        return await self._cached(key)

    def _report(self, error: Exception) -> None:
        if self.errors.full():
            self.errors.get_nowait()
        self.errors.put_nowait(error)

    async def _accept_locked(self, value: Snapshot) -> Snapshot:
        if self._closed:
            raise Closed("client closed")
        previous = self._records.get(value.key)
        if previous is not None and (
            value.sequence < previous.sequence
            or (
                value.sequence == previous.sequence
                and (value.revision < previous.revision or value.id != previous.id)
            )
        ):
            return previous
        if value.deleted:
            from dataclasses import replace

            value = replace(value, content="", format="")
        self._records[value.key] = value
        transaction = asyncio.create_task(asyncio.to_thread(self._disk.write, value))
        cancelled = False
        while True:
            try:
                await asyncio.shield(transaction)
                break
            except asyncio.CancelledError:
                # An OS write can't be cancelled. Keep the state lock across
                # repeated cancellation until its atomic transaction finishes.
                cancelled = True
            except OSError as error:
                self._report(error)
                break
        if cancelled:
            raise asyncio.CancelledError
        return value

    async def _accept(self, value: Snapshot) -> Snapshot:
        async with self._lock:
            return await self._accept_locked(value)

    async def _cached(self, key: Key) -> Snapshot:
        async with self._lock:
            if self._closed:
                raise Closed("client closed")
            value = self._records.get(key)
            if value is not None:
                if value.deleted:
                    raise NotFound(value)
                return value.with_source("memory")
            disk = await asyncio.to_thread(self._disk.read, key)
            if self._closed:
                raise Closed("client closed")
            if disk is not None:
                self._records[key] = disk
                return disk
        raise Unavailable("no available server or cached configuration")

    async def close(self) -> None:
        self._closed = True
        self._generation += 1
        tasks = [sub.worker for sub in self._subscriptions.values() if sub.worker is not None]
        if self._watcher is not None:
            tasks.append(self._watcher)
        self._subscriptions.clear()
        pending = [task for task in tasks if task is not asyncio.current_task()]
        for task in pending:
            task.cancel()
        if pending:
            await asyncio.gather(*pending, return_exceptions=True)
        if self._ws is not None:
            await self._ws.close()
        if self._session is not None:
            await self._session.close()
        # External get() callers may still be committing a cache transaction.
        # Wait for its ordering lock rather than letting writes escape shutdown.
        async with self._lock:
            pass

    async def subscribe(self, key: Key, callback: Callback) -> None:
        if not callable(callback):
            raise ValueError("callback is required")
        try:
            value = await self.get(key)
        except NotFound as error:
            value = error.snapshot
        async with self._lock:
            if self._closed:
                raise Closed("client closed")
            if key in self._subscriptions:
                raise ValueError("key already subscribed")
            if len(self._subscriptions) >= 10:
                raise ValueError("at most ten subscriptions per client")
            sub = _Subscription(callback, asyncio.Queue(maxsize=1))
            self._subscriptions[key] = sub
            self._enqueue(sub, value)
            sub.worker = asyncio.create_task(self._dispatch(key, sub))
            self._generation += 1
            self._wake.set()
            ws = self._ws
            if self._watcher is None:
                self._watcher = asyncio.create_task(self._watch())
        if ws is not None:
            await ws.close()

    async def unsubscribe(self, key: Key) -> None:
        await self._transport()
        async with self._lock:
            sub = self._subscriptions.pop(key, None)
            if sub is None:
                return
            self._generation += 1
            self._wake.set()
            ws = self._ws
        if sub.worker is not None and sub.worker is not asyncio.current_task():
            sub.worker.cancel()
            await asyncio.gather(sub.worker, return_exceptions=True)
        if ws is not None:
            await ws.close()

    def _enqueue(self, sub: _Subscription, value: Snapshot) -> None:
        if sub.last is not None and (
            sub.last.id,
            sub.last.version,
            sub.last.deleted,
            sub.last.rule_id,
            sub.last.beta,
            sub.last.content,
            sub.last.format,
        ) == (
            value.id,
            value.version,
            value.deleted,
            value.rule_id,
            value.beta,
            value.content,
            value.format,
        ):
            return
        sub.last = value
        if sub.pending.full():
            sub.pending.get_nowait()
        sub.pending.put_nowait(value)

    async def _dispatch(self, key: Key, sub: _Subscription) -> None:
        while not self._closed and self._subscriptions.get(key) is sub:
            value = await sub.pending.get()
            try:
                result: object
                if inspect.iscoroutinefunction(sub.callback):
                    result = sub.callback(value)
                else:
                    result = await asyncio.to_thread(sub.callback, value)
                if inspect.isawaitable(result):
                    await result
            except Exception as error:
                self._report(error)

    async def _watch(self) -> None:
        backoff = 0.5
        index = 0
        session = await self._transport()
        while not self._closed:
            async with self._lock:
                keys = tuple(self._subscriptions)
                generation = self._generation
                self._wake.clear()
            if not keys:
                await self._wake.wait()
                continue
            for _ in self._addresses:
                address = self._addresses[index % len(self._addresses)]
                index += 1
                try:
                    connected_at = asyncio.get_running_loop().time()
                    async with session.ws_connect(
                        address + "/api/client/watch",
                        params={"tags": self._tags},
                        autoping=True,
                        max_msg_size=MAX_WIRE_BYTES,
                        timeout=aiohttp.ClientWSTimeout(ws_receive=35, ws_close=self._timeout),
                    ) as ws:
                        async with self._lock:
                            if self._closed or self._generation != generation:
                                break
                            self._ws = ws
                            for key in keys:
                                await ws.send_json({"op": "subscribe", "key": key.to_dict()})
                        async for message in ws:
                            if message.type != aiohttp.WSMsgType.TEXT:
                                break
                            value = Snapshot.from_dict(json.loads(message.data))
                            async with self._lock:
                                if (
                                    self._closed
                                    or self._generation != generation
                                    or self._ws is not ws
                                ):
                                    break
                                sub = self._subscriptions.get(value.key)
                                if sub is not None:
                                    accepted = await self._accept_locked(value)
                                    self._enqueue(sub, accepted)
                    if asyncio.get_running_loop().time() - connected_at >= 1:
                        backoff = 0.5
                    if self._generation != generation:
                        break
                except (aiohttp.ClientError, asyncio.TimeoutError, ValueError, OSError):
                    continue
                finally:
                    self._ws = None
            if self._closed:
                return
            if self._generation != generation:
                backoff = 0.5
                continue
            try:
                await asyncio.wait_for(
                    self._wake.wait(), min(30, backoff * random.uniform(0.8, 1.2))
                )
            except asyncio.TimeoutError:
                pass
            backoff = min(30, backoff * 2)

    async def __aenter__(self) -> "AsyncClient":
        await self._transport()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        await self.close()
