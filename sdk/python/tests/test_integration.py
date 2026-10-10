"""Integration through public interfaces against the isolated runner."""

import asyncio
import os
import time
import unittest

import aiohttp

from confhub import AsyncClient, Client, Key, NotFound


@unittest.skipUnless(os.getenv("CONFHUB_SDK_TEST_URL"), "run sdk/test-integration.py")
class IntegrationTests(unittest.IsolatedAsyncioTestCase):
    async def test_real_server_get_subscribe_gray_delete_recreate(self):
        address = os.environ["CONFHUB_SDK_TEST_URL"]
        other = os.environ["CONFHUB_SDK_TEST_URL2"]
        key = Key(f"python-sdk-{time.time_ns()}")
        path = "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/" + key.name
        received = asyncio.Queue()
        async with aiohttp.ClientSession(
            cookie_jar=aiohttp.CookieJar(unsafe=True), headers={"Origin": address}
        ) as admin:

            async def request(method, endpoint, body):
                async with admin.request(method, address + endpoint, json=body) as response:
                    self.assertEqual(response.status, 200)
                    return await response.json()

            await request(
                "POST",
                "/api/admin/login",
                {"username": "admin", "password": os.environ["CONFHUB_SDK_TEST_PASSWORD"]},
            )
            state = (
                await request(
                    "PUT", path, {"content": "first\n", "format": "text", "confirmed": True}
                )
            )["state"]
            async with AsyncClient(["http://127.0.0.1:1", other], tags={"env": "sdk"}) as client:
                await client.subscribe(key, received.put)
                first = await asyncio.wait_for(received.get(), 5)
                self.assertEqual((first.version, first.content), (1, "first\n"))
                state = (
                    await request(
                        "PUT",
                        path,
                        {
                            "expected_id": state["id"],
                            "expected_revision": state["revision"],
                            "content": "second\n",
                            "format": "text",
                            "confirmed": True,
                        },
                    )
                )["state"]
                self.assertEqual((await asyncio.wait_for(received.get(), 5)).version, 2)
                state = (
                    await request(
                        "PUT",
                        path + "/rules",
                        {
                            "expected_id": state["id"],
                            "expected_revision": state["revision"],
                            "confirmed": True,
                            "source_version": 2,
                            "rules": [
                                {
                                    "id": "gray",
                                    "name": "SDK",
                                    "enabled": True,
                                    "conditions": [
                                        {"tag": "env", "operator": "eq", "values": ["sdk"]}
                                    ],
                                }
                            ],
                        },
                    )
                )["state"]
                gray = await asyncio.wait_for(received.get(), 5)
                self.assertEqual((gray.version, gray.rule_id), (0, "gray"))
                self.assertTrue(gray.beta)
                for content in ("beta one", "beta two"):
                    state = (
                        await request(
                            "PUT",
                            path,
                            {
                                "expected_id": state["id"],
                                "expected_revision": state["revision"],
                                "target": "beta",
                                "content": content,
                                "format": "text",
                                "confirmed": True,
                            },
                        )
                    )["state"]
                    value = await asyncio.wait_for(received.get(), 5)
                    self.assertEqual(
                        (value.version, value.rule_id, value.content), (0, "gray", content)
                    )
                await request(
                    "DELETE",
                    path,
                    {
                        "expected_id": state["id"],
                        "expected_revision": state["revision"],
                        "confirmed": True,
                    },
                )
                self.assertTrue((await asyncio.wait_for(received.get(), 5)).deleted)
                with self.assertRaises(NotFound):
                    await client.get(key)
                await request(
                    "PUT", path, {"content": "rebuilt\n", "format": "text", "confirmed": True}
                )
                rebuilt = await asyncio.wait_for(received.get(), 5)
                self.assertNotEqual(first.id, rebuilt.id)
                self.assertEqual((rebuilt.version, rebuilt.content), (1, "rebuilt\n"))
                await client.unsubscribe(key)

            def use_sync():
                with Client([other]) as client:
                    return client.get(key)

            self.assertEqual((await asyncio.to_thread(use_sync)).content, "rebuilt\n")
