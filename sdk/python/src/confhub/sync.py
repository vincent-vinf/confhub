"""Synchronous facade using one owned, joined background event loop."""

import asyncio
import os
import threading
from collections.abc import Coroutine, Mapping, Sequence
from concurrent.futures import CancelledError
from types import TracebackType
from typing import Any, TypeVar

from .client import AsyncClient, Callback
from .types import Closed, Key, Snapshot

T = TypeVar("T")


class Client:
    def __init__(
        self,
        addresses: Sequence[str],
        *,
        tags: Mapping[str, str] | None = None,
        timeout: float = 2.0,
        cache_dir: str | os.PathLike[str] | None = None,
    ) -> None:
        self._client = AsyncClient(addresses, tags=tags, timeout=timeout, cache_dir=cache_dir)
        self._loop = asyncio.new_event_loop()
        self._closed = False
        self._guard = threading.RLock()
        self._thread = threading.Thread(target=self._run, name="confhub-client", daemon=True)
        self._thread.start()

    def _run(self) -> None:
        asyncio.set_event_loop(self._loop)
        try:
            self._loop.run_forever()
        finally:
            tasks = asyncio.all_tasks(self._loop)
            for task in tasks:
                task.cancel()
            if tasks:
                self._loop.run_until_complete(asyncio.gather(*tasks, return_exceptions=True))
            self._loop.run_until_complete(self._loop.shutdown_asyncgens())
            self._loop.close()

    def _call(self, coroutine: Coroutine[Any, Any, T]) -> T:
        with self._guard:
            if self._closed or threading.current_thread() is self._thread:
                coroutine.close()
                if self._closed:
                    raise Closed("client closed")
                raise RuntimeError("use AsyncClient inside an asynchronous callback")
            future = asyncio.run_coroutine_threadsafe(coroutine, self._loop)
        try:
            return future.result()
        except CancelledError as error:
            raise Closed("client closed") from error

    def get(self, key: Key) -> Snapshot:
        return self._call(self._client.get(key))

    def subscribe(self, key: Key, callback: Callback) -> None:
        self._call(self._client.subscribe(key, callback))

    def unsubscribe(self, key: Key) -> None:
        self._call(self._client.unsubscribe(key))

    def close(self) -> None:
        with self._guard:
            if self._closed:
                return
            if threading.current_thread() is self._thread:
                raise RuntimeError("cannot synchronously close from the client event loop")
            self._closed = True
        try:
            asyncio.run_coroutine_threadsafe(self._client.close(), self._loop).result()
        finally:
            self._loop.call_soon_threadsafe(self._loop.stop)
            self._thread.join()

    def __enter__(self) -> "Client":
        if self._closed:
            raise Closed("client closed")
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        self.close()
