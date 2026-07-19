"""Tests for NeptuneClient using respx to mock HTTP transport."""

from __future__ import annotations

import base64
import json

import httpx
import pytest
import respx

from neptune_sdk import (
    AddTorrentRequest,
    NeptuneClient,
    NeptuneRPCError,
    TorrentState,
)

BASE_URL = "http://127.0.0.1:8002"
RPC_URL = f"{BASE_URL}/json_rpc"
TOKEN = "test-token"

# ── helpers ───────────────────────────────────────────────────────────


def _ok(result, *, id=1):
    return httpx.Response(200, json={"jsonrpc": "2.0", "result": result, "id": id})


def _rpc_error(code=-32600, message="bad request", *, id=1):
    return httpx.Response(
        200,
        json={"jsonrpc": "2.0", "error": {"code": code, "message": message}, "id": id},
    )


TORRENT_JSON = {
    "hash": "aabb",
    "name": "test",
    "state": TorrentState.Downloading,
    "comment": "",
    "directory_base": "/downloads/test",
    "message": "",
    "tracker_errors": {},
    "tags": [],
    "custom": {},
    "download_rate": 0,
    "download_total": 0,
    "upload_rate": 0,
    "upload_total": 0,
    "connection_count": 0,
    "completed": 0,
    "corrupted": 0,
    "total_length": 100,
    "selected_size": 100,
    "add_at": 0,
    "completed_at": 0,
    "total_seeding": 0,
    "total_downloading": 1,
    "connected_seeding": 0,
    "connected_downloading": 2,
    "private": False,
}


# ── tests ─────────────────────────────────────────────────────────────


@pytest.fixture()
def mock_api():
    with respx.mock(base_url=BASE_URL) as rspx:
        yield rspx


@pytest.fixture()
def client():
    with NeptuneClient(RPC_URL, token=TOKEN) as c:
        yield c


def test_ping(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.ping()
    req = mock_api.calls.last.request
    payload = json.loads(req.content)
    assert payload["method"] == "system.ping"
    assert req.headers["authorization"] == TOKEN


def test_transfer_summary(mock_api, client):
    mock_api.post("/json_rpc").mock(
        return_value=_ok(
            {
                "download_rate": 100,
                "download_total": 200,
                "upload_rate": 50,
                "upload_total": 80,
            }
        )
    )
    result = client.transfer_summary()
    assert result.download_rate == 100
    assert result.upload_total == 80


def test_torrent_list(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok({"torrents": [TORRENT_JSON]}))
    result = client.torrent_list()
    assert len(result.torrents) == 1
    assert result.torrents[0].hash == "aabb"
    assert result.torrents[0].name == "test"


def test_torrent_get(mock_api, client):
    mock_api.post("/json_rpc").mock(
        return_value=_ok(
            {"name": "my_torrent", "tags": ["a", "b"], "custom": {"key1": "val1"}}
        )
    )
    result = client.torrent_get("aabb")
    assert result.name == "my_torrent"
    assert result.tags == ["a", "b"]
    assert result.custom == {"key1": "val1"}


def test_torrent_files(mock_api, client):
    mock_api.post("/json_rpc").mock(
        return_value=_ok(
            {
                "files": [
                    {
                        "path": ["dir", "file.txt"],
                        "index": 0,
                        "progress": 0.5,
                        "size": 1024,
                    }
                ]
            }
        )
    )
    result = client.torrent_files("aabb")
    assert len(result.files) == 1
    assert result.files[0].path == ["dir", "file.txt"]
    assert result.files[0].progress == 0.5


def test_torrent_peers(mock_api, client):
    mock_api.post("/json_rpc").mock(
        return_value=_ok(
            {
                "peers": [
                    {
                        "address": "1.2.3.4:5678",
                        "client": "qBittorrent",
                        "progress": 0.9,
                        "download_rate": 1000,
                        "upload_rate": 500,
                        "is_incoming": False,
                        "encrypted": False,
                    }
                ]
            }
        )
    )
    result = client.torrent_peers("aabb")
    assert result.peers[0].address == "1.2.3.4:5678"


def test_torrent_trackers(mock_api, client):
    mock_api.post("/json_rpc").mock(
        return_value=_ok(
            {
                "trackers": [
                    {"url": "http://t.example.com/announce", "tier": 0, "message": ""}
                ]
            }
        )
    )
    result = client.torrent_trackers("aabb")
    assert result.trackers[0].url == "http://t.example.com/announce"


def test_torrent_add_encodes_base64(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok({"info_hash": "aa" * 20}))

    torrent_bytes = b"d8:announce3:url..."
    req = AddTorrentRequest(torrent_file=torrent_bytes, tags=["linux"])
    result = client.torrent_add(req)

    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["params"]["torrent_file"] == base64.b64encode(torrent_bytes).decode()
    assert payload["params"]["tags"] == ["linux"]
    assert result.info_hash == "aa" * 20


def test_torrent_remove(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok({}))
    client.torrent_remove("aabb", delete_data=True)

    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["params"]["delete_data"] is True


def test_torrent_move(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_move("aabb", "/new/path")
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["method"] == "torrent.move"
    assert payload["params"]["info_hash"] == "aabb"
    assert payload["params"]["target_base_path"] == "/new/path"


def test_torrent_start(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_start("aabb")


def test_torrent_stop(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_stop("aabb")


def test_torrent_add_tags(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_add_tags("aabb", ["tag1", "tag2"])
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["params"]["tags"] == ["tag1", "tag2"]


def test_torrent_remove_tags(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_remove_tags("aabb", ["old"])


def test_torrent_set_download_limit(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_set_download_limit("aabb", 1024 * 1024)
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["params"]["limit"] == 1048576


def test_torrent_set_upload_limit(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_set_upload_limit("aabb", 512000)


def test_client_set_download_limit(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.client_set_download_limit(0)
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["method"] == "client.set_download_limit"
    assert payload["params"]["limit"] == 0


def test_client_set_upload_limit(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.client_set_upload_limit(-1)


def test_torrent_set_file_priority(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_set_file_priority("aabb", [0, 2], 1)
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["params"]["file_ids"] == [0, 2]
    assert payload["params"]["priority"] == 1


def test_rpc_error_raises(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_rpc_error(-32601, "method not found"))
    with pytest.raises(NeptuneRPCError) as exc_info:
        client.ping()
    assert exc_info.value.code == -32601
    assert "method not found" in exc_info.value.message


def test_empty_torrent_list(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok({"torrents": []}))
    result = client.torrent_list()
    assert result.torrents == []


def test_torrent_custom_set(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_custom_set("aabb", "label", "my-label")
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["method"] == "torrent.custom.set"
    assert payload["params"]["key"] == "label"
    assert payload["params"]["value"] == "my-label"


def test_torrent_custom_update(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_custom_update("aabb", {"k1": "v1", "k2": "v2"})
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["method"] == "torrent.custom.update"
    assert payload["params"]["custom"] == {"k1": "v1", "k2": "v2"}


def test_torrent_custom_del(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok(None))
    client.torrent_custom_del("aabb", "label")
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["method"] == "torrent.custom.del"
    assert payload["params"]["key"] == "label"


def test_torrent_list_with_keys(mock_api, client):
    mock_api.post("/json_rpc").mock(return_value=_ok({"torrents": [TORRENT_JSON]}))
    result = client.torrent_list(keys=["label"])
    assert len(result.torrents) == 1
    payload = json.loads(mock_api.calls.last.request.content)
    assert payload["params"]["keys"] == ["label"]
