"""Sync Python client for the Neptune BitTorrent JSON-RPC API."""

from __future__ import annotations

import base64
import json
import random
from dataclasses import asdict
from functools import cache
from typing import Any, TypeVar

import httpx
from pydantic import TypeAdapter

from .exceptions import NeptuneConnectionError, NeptuneRPCError
from .models import (
    AddTorrentRequest,
    AddTorrentResponse,
    AddTrackerRequest,
    DelCustomRequest,
    InfoHashRequest,
    ListTorrentRequest,
    MoveTorrentRequest,
    RemoveTorrentRequest,
    RemoveTrackerRequest,
    ReplaceTrackersRequest,
    SetCustomRequest,
    SetFilePriorityRequest,
    SetGlobalSpeedLimitRequest,
    SetSpeedLimitRequest,
    TagsRequest,
    TorrentFilesResponse,
    TorrentInfo,
    TorrentListResponse,
    TorrentPeersResponse,
    TorrentTrackersResponse,
    TransferConfig,
    TransferSummary,
    UpdateCustomRequest,
)

T = TypeVar("T")


def _json_default(obj: Any) -> Any:
    if isinstance(obj, (bytes, bytearray)):
        return base64.b64encode(obj).decode()
    raise TypeError(f"Object of type {type(obj).__name__} is not JSON serializable")


@cache
def _adapter(cls: type[T]) -> TypeAdapter[T]:
    return TypeAdapter(cls)


def _validate(cls: type[T], data: Any) -> T:
    return _adapter(cls).validate_python(data)


class NeptuneClient:
    """Sync JSON-RPC client for Neptune.

    Example::

        with NeptuneClient("http://127.0.0.1:8002", token="secret") as c:
            torrents = c.torrent_list()
    """

    def __init__(
        self,
        base_url: str,
        *,
        token: str,
        timeout: float = 30.0,
    ) -> None:
        self._client = httpx.Client(
            base_url=base_url.rstrip("/"),
            headers={"Authorization": token},
            timeout=timeout,
        )

    # ── lifecycle ──────────────────────────────────────────────────────

    def __enter__(self) -> NeptuneClient:
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()

    # ── transport ──────────────────────────────────────────────────────

    def _call(self, method: str, params: Any = None) -> Any:
        """Send a JSON-RPC request and return the result field."""
        payload: dict[str, Any] = {
            "jsonrpc": "2.0",
            "method": method,
            "id": random.randint(1, 2**31),
        }
        if params is not None:
            payload["params"] = asdict(params)

        body_bytes = json.dumps(payload, default=_json_default).encode()

        try:
            resp = self._client.post(
                "/json_rpc",
                content=body_bytes,
                headers={"Content-Type": "application/json"},
            )
            resp.raise_for_status()
        except httpx.HTTPError as exc:
            raise NeptuneConnectionError(str(exc)) from exc

        body = resp.json()

        if "error" in body and body["error"] is not None:
            err = body["error"]
            raise NeptuneRPCError(
                code=err.get("code", -1),
                message=err.get("message", ""),
                data=err.get("data"),
            )

        return body.get("result")

    # ── system ─────────────────────────────────────────────────────────

    def ping(self) -> None:
        """system.ping — health check."""
        self._call("system.ping")

    # ── transfer ───────────────────────────────────────────────────────

    def transfer_summary(self) -> TransferSummary:
        """Global download/upload rates and totals."""
        return _validate(TransferSummary, self._call("transfer_summary"))

    # ── torrent — queries ──────────────────────────────────────────────

    def torrent_list(self, keys: list[str] | None = None) -> TorrentListResponse:
        """List all torrents. Optionally filter custom keys returned."""
        return _validate(
            TorrentListResponse,
            self._call("torrent.list", ListTorrentRequest(keys=keys or None)),
        )

    def torrent_get(self, info_hash: str) -> TorrentInfo:
        """Get basic info for a single torrent."""
        return _validate(
            TorrentInfo,
            self._call("torrent.get", InfoHashRequest(info_hash=info_hash)),
        )

    def torrent_files(self, info_hash: str) -> TorrentFilesResponse:
        """List files in a torrent."""
        return _validate(
            TorrentFilesResponse,
            self._call("torrent.files", InfoHashRequest(info_hash=info_hash)),
        )

    def torrent_peers(self, info_hash: str) -> TorrentPeersResponse:
        """List connected peers for a torrent."""
        return _validate(
            TorrentPeersResponse,
            self._call("torrent.peers", InfoHashRequest(info_hash=info_hash)),
        )

    def torrent_trackers(self, info_hash: str) -> TorrentTrackersResponse:
        """List trackers for a torrent."""
        return _validate(
            TorrentTrackersResponse,
            self._call("torrent.trackers", InfoHashRequest(info_hash=info_hash)),
        )

    def torrent_add_tracker(self, info_hash: str, url: str, *, tier: int = 0) -> None:
        """Add a tracker to a torrent."""
        self._call(
            "torrent.add_tracker",
            AddTrackerRequest(info_hash=info_hash, url=url, tier=tier),
        )

    def torrent_remove_tracker(self, info_hash: str, url: str) -> None:
        """Remove a tracker from a torrent."""
        self._call(
            "torrent.remove_tracker",
            RemoveTrackerRequest(info_hash=info_hash, url=url),
        )

    def torrent_replace_trackers(
        self, info_hash: str, replacements: dict[str, str]
    ) -> None:
        """Replace tracker URLs. Keys are old URLs, values are new URLs."""
        self._call(
            "torrent.replace_trackers",
            ReplaceTrackersRequest(info_hash=info_hash, replacements=replacements),
        )

    # ── torrent — mutations ────────────────────────────────────────────

    def torrent_add(self, req: AddTorrentRequest) -> AddTorrentResponse:
        """Add a torrent from raw .torrent bytes."""
        return _validate(AddTorrentResponse, self._call("torrent.add", req))

    def torrent_move(self, info_hash: str, target_base_path: str) -> None:
        """Move torrent data to a new directory."""
        self._call(
            "torrent.move",
            MoveTorrentRequest(info_hash=info_hash, target_base_path=target_base_path),
        )

    def torrent_remove(
        self, info_hash: str, *, delete_data: bool = False
    ) -> TorrentListResponse:
        """Remove a torrent and return updated list."""
        return _validate(
            TorrentListResponse,
            self._call(
                "torrent.remove",
                RemoveTorrentRequest(info_hash=info_hash, delete_data=delete_data),
            ),
        )

    def torrent_start(self, info_hash: str) -> None:
        """Start a torrent."""
        self._call("torrent.start", InfoHashRequest(info_hash=info_hash))

    def torrent_stop(self, info_hash: str) -> None:
        """Stop a torrent."""
        self._call("torrent.stop", InfoHashRequest(info_hash=info_hash))

    def torrent_recheck(self, info_hash: str) -> None:
        """Recheck torrent data integrity."""
        self._call("torrent.recheck", InfoHashRequest(info_hash=info_hash))

    # ── torrent — custom ───────────────────────────────────────────────

    def torrent_custom_set(self, info_hash: str, key: str, value: str) -> None:
        """Set a custom key-value pair on a torrent."""
        self._call(
            "torrent.custom.set",
            SetCustomRequest(info_hash=info_hash, key=key, value=value),
        )

    def torrent_custom_update(self, info_hash: str, custom: dict[str, str]) -> None:
        """Update multiple custom key-value pairs on a torrent."""
        self._call(
            "torrent.custom.update",
            UpdateCustomRequest(info_hash=info_hash, custom=custom),
        )

    def torrent_custom_del(self, info_hash: str, key: str) -> None:
        """Delete a custom key from a torrent."""
        self._call(
            "torrent.custom.del",
            DelCustomRequest(info_hash=info_hash, key=key),
        )

    def torrent_add_tags(self, info_hash: str, tags: list[str]) -> None:
        """Add tags to a torrent."""
        self._call("torrent.add_tags", TagsRequest(info_hash=info_hash, tags=tags))

    def torrent_remove_tags(self, info_hash: str, tags: list[str]) -> None:
        """Remove tags from a torrent."""
        self._call("torrent.remove_tags", TagsRequest(info_hash=info_hash, tags=tags))

    # ── torrent — limits ───────────────────────────────────────────────

    def torrent_set_download_limit(self, info_hash: str, limit: int) -> None:
        """Set per-torrent download speed limit (bytes/s, <=0 = unlimited)."""
        self._call(
            "torrent.set_download_limit",
            SetSpeedLimitRequest(info_hash=info_hash, limit=limit),
        )

    def torrent_set_upload_limit(self, info_hash: str, limit: int) -> None:
        """Set per-torrent upload speed limit (bytes/s, <=0 = unlimited)."""
        self._call(
            "torrent.set_upload_limit",
            SetSpeedLimitRequest(info_hash=info_hash, limit=limit),
        )

    def client_set_download_limit(self, limit: int) -> None:
        """Set global download speed limit (bytes/s, <=0 = unlimited)."""
        self._call(
            "client.set_download_limit",
            SetGlobalSpeedLimitRequest(limit=limit),
        )

    def client_set_upload_limit(self, limit: int) -> None:
        """Set global upload speed limit (bytes/s, <=0 = unlimited)."""
        self._call(
            "client.set_upload_limit",
            SetGlobalSpeedLimitRequest(limit=limit),
        )

    def client_get_transfer_config(self) -> TransferConfig:
        """Get global download/upload speed limits."""
        return _validate(TransferConfig, self._call("client.get_transfer_config"))

    # ── torrent — file priority ────────────────────────────────────────

    def torrent_set_file_priority(
        self, info_hash: str, file_ids: list[int], priority: int
    ) -> None:
        """Set file priority (0 = skip, 1 = download)."""
        self._call(
            "torrent.set_file_priority",
            SetFilePriorityRequest(
                info_hash=info_hash,
                file_ids=file_ids,
                priority=priority,
            ),
        )
