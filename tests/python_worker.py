"""JSON-lines bridge exercising only the public Python SDK interface."""

import asyncio
import json
import sys

from confhub import AsyncClient, Key, NotFound


async def main():
    client = None
    latest = {}
    counts = {}
    callback_errors = []
    try:
        while True:
            line = await asyncio.to_thread(sys.stdin.readline)
            if not line:
                break
            request = json.loads(line)
            op = request["op"]
            try:
                if op == "init":
                    client = AsyncClient(request["addresses"], tags=request["tags"], timeout=1)
                    result = {}
                elif op == "close":
                    await client.close()
                    result = {}
                else:
                    key = Key(**request["key"])
                    identity = json.dumps(key.to_dict(), sort_keys=True)
                    if op == "subscribe":

                        async def receive(value, identity=identity):
                            previous = latest.get(identity)
                            if previous and previous.sequence > value.sequence:
                                callback_errors.append("older snapshot replaced newer state")
                            latest[identity] = value
                            counts[identity] = counts.get(identity, 0) + 1

                        await client.subscribe(key, receive)
                        result = {}
                    elif op == "latest":
                        if callback_errors:
                            raise AssertionError(callback_errors[0])
                        value = latest.get(identity)
                        result = {
                            "value": value.to_dict() if value else None,
                            "count": counts.get(identity, 0),
                        }
                    elif op == "get":
                        value = await client.get(key)
                        result = {"value": value.to_dict(), "source": value.source}
                    elif op == "unsubscribe":
                        await client.unsubscribe(key)
                        result = {}
                    else:
                        raise ValueError("unknown operation")
                print(json.dumps({"result": result}), flush=True)
            except NotFound as error:
                print(
                    json.dumps({"result": {"value": error.snapshot.to_dict(), "not_found": True}}),
                    flush=True,
                )
            except Exception as error:
                print(json.dumps({"error": type(error).__name__}), flush=True)
            if op == "close":
                break
    finally:
        if client:
            await client.close()


if __name__ == "__main__":
    asyncio.run(main())
