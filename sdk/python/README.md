# neptune-sdk

Python SDK for the [Neptune](https://github.com/jesec/neptune) BitTorrent client JSON-RPC API.

Built with [httpx](https://github.com/encode/httpx).

## Install

```bash
pip install neptune-sdk
```

## Quick start

```python
from neptune_sdk import NeptuneClient, AddTorrentRequest

with NeptuneClient("http://127.0.0.1:8002", token="your-token") as client:
    # health check
    client.ping()

    # global transfer stats
    summary = client.transfer_summary()
    print(f"↓ {summary.download_rate} B/s  ↑ {summary.upload_rate} B/s")

    # list torrents
    result = client.torrent_list()
    for t in result.torrents:
        print(f"{t.name}  [{t.state}]  {t.completed}/{t.total_length}")

    # add a torrent
    torrent_bytes = open("example.torrent", "rb").read()
    resp = client.torrent_add(AddTorrentRequest(torrent_file=torrent_bytes))
    print(f"added: {resp.info_hash}")
```

## API reference

Every JSON-RPC method maps 1:1 to an async client method:

| RPC method | Client method |
|---|---|
| `system.ping` | `ping()` |
| `transfer_summary` | `transfer_summary()` |
| `torrent.list` | `torrent_list()` |
| `torrent.get` | `torrent_get(info_hash)` |
| `torrent.files` | `torrent_files(info_hash)` |
| `torrent.peers` | `torrent_peers(info_hash)` |
| `torrent.trackers` | `torrent_trackers(info_hash)` |
| `torrent.add` | `torrent_add(AddTorrentRequest)` |
| `torrent.move` | `torrent_move(info_hash, target_base_path)` |
| `torrent.remove` | `torrent_remove(info_hash, delete_data=False)` |
| `torrent.resume` | `torrent_resume(info_hash)` |
| `torrent.start` | `torrent_start(info_hash)` |
| `torrent.stop` | `torrent_stop(info_hash)` |
| `torrent.add_tags` | `torrent_add_tags(info_hash, tags)` |
| `torrent.remove_tags` | `torrent_remove_tags(info_hash, tags)` |
| `torrent.set_download_limit` | `torrent_set_download_limit(info_hash, limit)` |
| `torrent.set_upload_limit` | `torrent_set_upload_limit(info_hash, limit)` |
| `torrent.set_file_priority` | `torrent_set_file_priority(info_hash, file_ids, priority)` |
| `client.set_download_limit` | `client_set_download_limit(limit)` |
| `client.set_upload_limit` | `client_set_upload_limit(limit)` |

## Development

```bash
cd sdk/python
uv venv
uv pip install -e ".[dev]"
uv run ty check src/
uv run pytest tests/ -v
```
