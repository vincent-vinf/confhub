"""Private disk snapshots; all calls are offloaded from the application loop."""

import hashlib
import json
import os
import tempfile
from collections.abc import Sequence
from pathlib import Path

from .types import Key, Snapshot


class DiskCache:
    def __init__(
        self, directory: str | os.PathLike[str] | None, addresses: Sequence[str], tags: str
    ) -> None:
        scope = json.dumps([sorted(addresses), tags], ensure_ascii=False).encode()
        self._root = (
            Path(directory) / hashlib.sha256(scope).hexdigest() if directory is not None else None
        )

    def _file(self, key: Key) -> Path:
        assert self._root is not None
        identity = json.dumps(key.to_dict(), sort_keys=True, ensure_ascii=False).encode()
        return self._root / (hashlib.sha256(identity).hexdigest() + ".json")

    def read(self, key: Key) -> Snapshot | None:
        if self._root is None:
            return None
        try:
            with self._file(key).open("rb") as file:
                raw = file.read((2 << 20) + 1)
            if len(raw) > 2 << 20:
                return None
            value = Snapshot.from_dict(json.loads(raw))
            if value.key == key and not value.deleted:
                return value.with_source("disk")
        except (OSError, ValueError):
            pass
        return None

    def write(self, value: Snapshot) -> None:
        if self._root is None:
            return
        path = self._file(value.key)
        if value.deleted:
            path.unlink(missing_ok=True)
            return
        self._root.mkdir(mode=0o700, parents=True, exist_ok=True)
        raw = json.dumps(value.to_dict(), ensure_ascii=False).encode()
        descriptor, name = tempfile.mkstemp(prefix=".snapshot-", dir=self._root)
        try:
            with os.fdopen(descriptor, "wb") as file:
                file.write(raw)
                file.flush()
                os.fsync(file.fileno())
            os.replace(name, path)
        finally:
            Path(name).unlink(missing_ok=True)
