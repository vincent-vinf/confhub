import asyncio
import tempfile
import unittest

from aiohttp import web

from confhub import AsyncClient, Client, Closed, Key, NotFound, Unavailable


class ClientTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.mode = "online"
        self.hold_next = False
        self.held = asyncio.Event()
        self.release = asyncio.Event()
        self.key = Key("服务.yaml")
        self.value = dict(
            sequence=42,
            id="first",
            key=dict(namespace="public", group="DEFAULT_GROUP", name="服务.yaml"),
            revision=7,
            version=3,
            content="port: 8080\n",
            format="yaml",
            deleted=False,
        )
        self.app = web.Application()
        self.app.router.add_get("/api/client/config", self.get_config)
        self.app.router.add_get("/api/client/watch", self.watch)
        self.messages = asyncio.Queue()
        self.subscribed = asyncio.Event()
        self.watch_count = 0
        self.reject_watch = False
        self.sockets = []
        self.runner = web.AppRunner(self.app)
        await self.runner.setup()
        self.site = web.TCPSite(self.runner, "127.0.0.1", 0)
        await self.site.start()
        self.address = "http://127.0.0.1:" + str(self.runner.addresses[0][1])

    async def asyncTearDown(self):
        for socket in self.sockets:
            await socket.close()
        await self.runner.cleanup()

    async def watch(self, request):
        socket = web.WebSocketResponse()
        await socket.prepare(request)
        self.sockets.append(socket)
        self.watch_count += 1
        await socket.receive_json()
        self.subscribed.set()
        if self.reject_watch:
            await socket.close()
            return socket
        await socket.send_json(self.value)
        receiver = asyncio.create_task(socket.receive())
        try:
            while not socket.closed:
                message = asyncio.create_task(self.messages.get())
                done, _ = await asyncio.wait(
                    [receiver, message], return_when=asyncio.FIRST_COMPLETED
                )
                if receiver in done:
                    message.cancel()
                    await asyncio.gather(message, return_exceptions=True)
                    break
                value = message.result()
                if value is None:
                    await socket.close()
                    break
                await socket.send_json(value)
        finally:
            receiver.cancel()
            await asyncio.gather(receiver, return_exceptions=True)
        return socket

    async def get_config(self, request):
        if self.hold_next:
            self.hold_next = False
            self.held.set()
            await self.release.wait()
            return web.json_response(
                dict(self.value, sequence=40, revision=1, version=1, content="old")
            )
        self.assertEqual(request.query["namespace"], "public")
        self.assertEqual(request.query["group"], "DEFAULT_GROUP")
        self.assertEqual(request.query["name"], "服务.yaml")
        import json

        tags = json.loads(request.query["tags"])
        self.assertTrue(tags["sys.hostname"])
        self.assertTrue(tags["sys.ip"])
        if self.mode == "offline":
            return web.Response(status=503)
        return web.json_response(self.value, status=404 if self.value["deleted"] else 200)

    async def test_get_preserves_text_and_fails_without_cache(self):
        async with AsyncClient([self.address]) as client:
            value = await client.get(self.key)
            self.assertEqual(
                (value.content, value.version, value.source), ("port: 8080\n", 3, "online")
            )
        self.mode = "offline"
        async with AsyncClient([self.address], timeout=0.1) as client:
            with self.assertRaises(Unavailable):
                await client.get(self.key)

    async def test_disk_cache_is_scoped_and_deletion_prevents_resurrection(self):
        with tempfile.TemporaryDirectory() as directory:
            async with AsyncClient(
                [self.address], cache_dir=directory, tags={"env": "blue"}
            ) as client:
                await client.get(self.key)
                self.mode = "offline"
                value = await client.get(self.key)
                self.assertEqual((value.content, value.source), ("port: 8080\n", "memory"))
            async with AsyncClient(
                [self.address], cache_dir=directory, tags={"env": "blue"}
            ) as client:
                self.assertEqual((await client.get(self.key)).source, "disk")
                async with AsyncClient(
                    [self.address], cache_dir=directory, tags={"env": "green"}
                ) as isolated:
                    with self.assertRaises(Unavailable):
                        await isolated.get(self.key)
                self.mode = "online"
                self.value = dict(self.value, sequence=43, deleted=True, content="", version=0)
                with self.assertRaises(NotFound):
                    await client.get(self.key)
                self.mode = "offline"
                with self.assertRaises(NotFound):
                    await client.get(self.key)
            async with AsyncClient(
                [self.address], cache_dir=directory, tags={"env": "blue"}
            ) as restarted:
                with self.assertRaises(Unavailable):
                    await restarted.get(self.key)

    async def test_watch_reconnect_lower_version_delete_and_rebuild(self):
        received = asyncio.Queue()
        async with AsyncClient([self.address]) as client:
            await client.subscribe(self.key, received.put)
            first = await asyncio.wait_for(received.get(), 2)
            self.assertEqual(first.version, 3)
            await asyncio.wait_for(self.subscribed.wait(), 2)
            self.value = dict(
                self.value, sequence=43, revision=8, version=1, content="gray one", rule_id="gray"
            )
            await self.messages.put(self.value)
            lower = await asyncio.wait_for(received.get(), 2)
            self.assertEqual((lower.version, lower.content), (1, "gray one"))
            await self.messages.put(dict(self.value, sequence=42, revision=7, version=3))
            await self.messages.put(None)
            deadline = asyncio.get_running_loop().time() + 3
            while self.watch_count < 2:
                if asyncio.get_running_loop().time() > deadline:
                    self.fail("did not recreate subscription")
                await asyncio.sleep(0.01)
            self.assertTrue(received.empty(), "duplicate or obsolete callback")
            self.value = dict(
                self.value, sequence=44, revision=9, content="beta replaced", rule_id="gray"
            )
            await self.messages.put(self.value)
            beta = await asyncio.wait_for(received.get(), 2)
            self.assertEqual((beta.version, beta.content), (1, "beta replaced"))
            deleted = dict(
                self.value, sequence=45, revision=10, deleted=True, content="", version=0
            )
            await self.messages.put(deleted)
            self.assertTrue((await asyncio.wait_for(received.get(), 2)).deleted)
            with self.assertRaises(NotFound):
                await client.get(self.key)  # stale live HTTP result cannot resurrect deletion
            rebuilt = dict(self.value, sequence=46, revision=1, id="rebuilt", version=1)
            await self.messages.put(rebuilt)
            self.assertEqual((await asyncio.wait_for(received.get(), 2)).id, "rebuilt")
            await client.unsubscribe(self.key)

    async def test_sync_callback_does_not_block_asyncio_or_network_receive(self):
        import threading

        started = threading.Event()
        release = threading.Event()

        def slow(value):
            if value.version == 3:
                started.set()
                release.wait(3)

        try:
            async with AsyncClient([self.address]) as client:
                await client.subscribe(self.key, slow)
                await asyncio.wait_for(self.subscribed.wait(), 2)
                while not started.is_set():
                    await asyncio.sleep(0.01)
                await self.messages.put(
                    dict(self.value, sequence=43, revision=8, version=2, content="two")
                )
                self.mode = "offline"
                for _ in range(100):
                    value = await client.get(self.key)
                    if value.version == 2:
                        break
                    await asyncio.sleep(0.01)
                self.assertEqual(value.content, "two")
                release.set()
        finally:
            release.set()

    async def test_sync_facade_works_while_application_event_loop_is_running(self):
        import threading

        # Construction is legal inside an already-running application loop.
        client = Client([self.address])
        try:
            value = await asyncio.to_thread(client.get, self.key)
            self.assertEqual(value.content, "port: 8080\n")
            seen = threading.Event()
            await asyncio.to_thread(client.subscribe, self.key, lambda value: seen.set())
            self.assertTrue(await asyncio.to_thread(seen.wait, 2))
            await asyncio.to_thread(client.unsubscribe, self.key)
        finally:
            await asyncio.to_thread(client.close)
        with self.assertRaises(Closed):
            await asyncio.to_thread(client.get, self.key)

    async def test_close_waits_for_inflight_atomic_disk_write(self):
        import os
        import threading
        from unittest.mock import patch

        started = threading.Event()
        release = threading.Event()
        replace = os.replace

        def slow_replace(source, target):
            started.set()
            release.wait(3)
            replace(source, target)

        with tempfile.TemporaryDirectory() as directory:
            client = AsyncClient([self.address], cache_dir=directory)
            try:
                with patch("os.replace", slow_replace):
                    operation = asyncio.create_task(client.get(self.key))
                    while not started.is_set():
                        await asyncio.sleep(0.01)
                    operation.cancel()
                    await asyncio.sleep(0.02)
                    self.assertFalse(
                        operation.done(),
                        "cancelled cache writer escaped before atomic commit completed",
                    )
                    closing = asyncio.create_task(client.close())
                    await asyncio.sleep(0.02)
                    self.assertFalse(
                        closing.done(),
                        "close returned before atomic cache commit completed",
                    )
                    release.set()
                    await asyncio.gather(operation, return_exceptions=True)
                    await asyncio.wait_for(closing, 2)
            finally:
                release.set()
                await client.close()

    async def test_rapid_disconnect_uses_backoff(self):
        self.reject_watch = True
        async with AsyncClient([self.address]) as client:
            await client.subscribe(self.key, lambda value: None)
            await asyncio.sleep(0.15)
            self.assertLessEqual(self.watch_count, 2, "reconnect storm")

    async def test_maximum_escaped_content_survives_disk_restart(self):
        self.value = dict(self.value, content="\0" * (1 << 20), format="text")
        with tempfile.TemporaryDirectory() as directory:
            async with AsyncClient([self.address], cache_dir=directory) as client:
                self.assertEqual(len((await client.get(self.key)).content), 1 << 20)
            self.mode = "offline"
            async with AsyncClient([self.address], cache_dir=directory) as client:
                value = await client.get(self.key)
                self.assertEqual((len(value.content), value.source), (1 << 20, "disk"))

    async def test_disk_snapshot_fences_an_already_running_older_query(self):
        with tempfile.TemporaryDirectory() as directory:
            async with AsyncClient([self.address], cache_dir=directory) as client:
                await client.get(self.key)
            async with AsyncClient([self.address], cache_dir=directory) as client:
                self.mode = "offline"
                self.hold_next = True
                pending = asyncio.create_task(client.get(self.key))
                try:
                    await asyncio.wait_for(self.held.wait(), 2)
                    self.assertEqual((await client.get(self.key)).sequence, 42)
                    self.release.set()
                    late = await asyncio.wait_for(pending, 2)
                    self.assertEqual((late.sequence, late.content), (42, "port: 8080\n"))
                finally:
                    self.release.set()
                    await asyncio.gather(pending, return_exceptions=True)


if __name__ == "__main__":
    unittest.main()
