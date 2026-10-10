"""Public immutable configuration identities and snapshots."""

from dataclasses import dataclass, replace
from typing import Literal

Source = Literal["online", "memory", "disk"]


@dataclass(frozen=True)
class Key:
    name: str
    namespace: str = "public"
    group: str = "DEFAULT_GROUP"

    def __post_init__(self) -> None:
        for value in (self.namespace, self.group, self.name):
            if (
                not isinstance(value, str)
                or not 1 <= len(value.encode()) <= 128
                or value.strip() != value
                or any(char in value for char in "/\\\0\r\n")
            ):
                raise ValueError(
                    "configuration names require 1–128 bytes without slashes or surrounding whitespace"
                )

    def to_dict(self) -> dict[str, str]:
        return {"namespace": self.namespace, "group": self.group, "name": self.name}


@dataclass(frozen=True)
class Snapshot:
    key: Key
    sequence: int
    id: str
    revision: int
    version: int
    content: str
    format: str
    deleted: bool = False
    rule_id: str = ""
    source: Source = "online"

    @classmethod
    def from_dict(cls, raw: object) -> "Snapshot":
        if not isinstance(raw, dict) or not isinstance(raw.get("key"), dict):
            raise ValueError("invalid configuration snapshot")
        key_raw = raw["key"]
        key = Key(
            name=key_raw.get("name", ""),
            namespace=key_raw.get("namespace", ""),
            group=key_raw.get("group", ""),
        )
        numbers: dict[str, int] = {}
        for field in ("sequence", "revision", "version"):
            number = raw.get(field, 0)
            if type(number) is not int or number < 0:
                raise ValueError("invalid configuration watermark")
            numbers[field] = number
        strings: dict[str, str] = {}
        for field in ("id", "content", "format", "rule_id"):
            value = raw.get(field, "")
            if not isinstance(value, str):
                raise ValueError("invalid configuration text")
            strings[field] = value
        deleted = raw.get("deleted", False)
        if type(deleted) is not bool or len(strings["content"].encode()) > 1 << 20:
            raise ValueError("invalid configuration snapshot")
        if not deleted and (not strings["id"] or numbers["version"] < 1 or not strings["format"]):
            raise ValueError("invalid live configuration snapshot")
        return cls(
            key=key,
            sequence=numbers["sequence"],
            revision=numbers["revision"],
            version=numbers["version"],
            id=strings["id"],
            content=strings["content"],
            format=strings["format"],
            rule_id=strings["rule_id"],
            deleted=deleted,
        )

    def to_dict(self) -> dict[str, object]:
        return {
            "key": self.key.to_dict(),
            "sequence": self.sequence,
            "id": self.id,
            "revision": self.revision,
            "version": self.version,
            "content": self.content,
            "format": self.format,
            "rule_id": self.rule_id,
            "deleted": self.deleted,
        }

    def with_source(self, source: Source) -> "Snapshot":
        return replace(self, source=source)


class Unavailable(Exception):
    """No server or last-known configuration is available."""


class NotFound(Exception):
    """The server explicitly deleted or has no such configuration."""

    def __init__(self, snapshot: Snapshot) -> None:
        super().__init__("configuration not found")
        self.snapshot = snapshot


class Closed(Exception):
    """The client has been closed."""
