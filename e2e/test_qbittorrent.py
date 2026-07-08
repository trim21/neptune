"""Neptune ↔ qBittorrent interop — parametrized matrix.

Matrix (12 combinations):
    direction  ∈ {upload, download}
    encryption ∈ {require, prefer, disable}
"""

import shutil
from pathlib import Path

import httpx
import pytest

DIRECTIONS = ["upload", "download"]
ENCRYPTION_LEVELS = ["require", "prefer", "disable"]


@pytest.mark.e2e
@pytest.mark.parametrize("encryption", ENCRYPTION_LEVELS)
@pytest.mark.parametrize("direction", DIRECTIONS)
def test_neptune_qb_interop(
    qb_client: httpx.Client,
    neptune,
    fixture,
    tmp_path: Path,
    direction: str,
    encryption: str,
) -> None:
    """Neptune ↔ qBittorrent transfer with encryption level."""

    if direction == "upload":
        # Neptune seeds → QB downloads.
        seed_dir = tmp_path / "seeder"
        seed_dir.mkdir()
        shutil.copytree(fixture.data_dir, seed_dir / fixture.data_dir.name)

        inst = neptune(crypto=encryption)
        ih = inst.torrent_add(str(fixture.torrent_path), str(seed_dir))
        inst.torrent_recheck(ih)
        inst.torrent_start(ih)
        inst.wait_state(ih, "seeding")

        _qb_add_torrent(qb_client, fixture.torrent_path)
        _qb_wait_state(qb_client, fixture.info_hash, "uploading", timeout=60)
    else:
        # QB seeds → Neptune downloads.
        download_dir = tmp_path / "downloads"
        download_dir.mkdir()

        _qb_add_torrent(qb_client, fixture.torrent_path)
        _qb_wait_state(qb_client, fixture.info_hash, "uploading", timeout=30)

        inst = neptune(crypto=encryption)
        ih = inst.torrent_add(str(fixture.torrent_path), str(download_dir))
        inst.torrent_start(ih)
        inst.wait_state(ih, "seeding")

        verify_files(fixture, str(download_dir))


# ── QB helpers ─────────────────────────────────────────────────────────

def _qb_add_torrent(client: httpx.Client, torrent_path: Path) -> None:
    data = torrent_path.read_bytes()
    resp = client.post("/api/v2/torrents/add", content=data)
    assert resp.status_code == 200, f"QB add torrent failed: {resp.text[:200]}"


def _qb_wait_state(
    client: httpx.Client, info_hash: str, state: str, timeout: float = 60
) -> dict:
    import time
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        resp = client.get(
            "/api/v2/torrents/info",
            params={"hashes": info_hash, "category": ""},
        )
        if resp.status_code != 200:
            time.sleep(0.5)
            continue
        data = resp.json()
        if not data:
            time.sleep(0.5)
            continue
        current = data[0].get("state", "").lower()
        if current == state.lower():
            return data[0]
        time.sleep(1)
    raise RuntimeError(
        f"QB torrent {info_hash[:8]} did not reach state={state} after {timeout}s"
    )
