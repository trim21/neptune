"""Encryption (MSE) cross-compatibility matrix.

Tests different encryption level combinations between two peers:
    neptune_enc × peer_enc ∈ {require, prefer, disable}²

Also qBittorrent integration with encryption.
"""

from pathlib import Path

import pytest

ALL_LEVELS = ["require", "prefer", "disable"]


@pytest.mark.e2e
@pytest.mark.parametrize("peer_enc", ALL_LEVELS)
@pytest.mark.parametrize("neptune_enc", ALL_LEVELS)
def test_encryption_cross(
    neptune, fixture, tmp_path: Path,
    neptune_enc: str, peer_enc: str,
) -> None:
    """Two neptune instances with different encryption levels.

    Expected behavior (libtorrent convention):
        require × require  → encrypted, transfer OK
        require × prefer   → encrypted, transfer OK
        require × disable  → neptune refuses plain, no connection
        prefer  × require  → encrypted, transfer OK
        prefer  × prefer   → encrypted, transfer OK
        prefer  × disable  → plain fallback, transfer OK
        disable × require  → neptune connects plain, peer refuses, no connection
        disable × prefer   → plain fallback, transfer OK
        disable × disable  → plain, transfer OK
    """
    import shutil

    seeder = neptune(crypto=peer_enc)
    leecher = neptune(crypto=neptune_enc)

    seeder_data = tmp_path / "seeder"
    seeder_data.mkdir()
    shutil.copytree(fixture.data_dir, seeder_data / fixture.data_dir.name)

    ih = seeder.torrent_add(str(fixture.torrent_path), str(seeder_data))
    seeder.torrent_recheck(ih)
    seeder.torrent_start(ih)
    seeder.wait_state(ih, "seeding")

    # For incompatible combinations (require × disable), connection should fail.
    # The leecher won't be able to download.
    incompatible = (
        (neptune_enc == "require" and peer_enc == "disable")
        or (neptune_enc == "disable" and peer_enc == "require")
    )

    leecher_data = tmp_path / "leecher"
    leecher_data.mkdir()
    leecher.torrent_add(str(fixture.torrent_path), str(leecher_data))
    leecher.torrent_start(ih)

    if incompatible:
        # Should timeout — no connection possible.
        import time
        time.sleep(10)
        info = leecher.torrent_get(ih)
        # Must remain in "downloading" (stalled) — not "seeding".
        assert info.get("state", "").lower() != "seeding", (
            f"Unexpected: {neptune_enc}×{peer_enc} succeeded (should be incompatible)"
        )
    else:
        leecher.wait_state(ih, "seeding")
        verify_files(fixture, str(leecher_data))


@pytest.mark.e2e
@pytest.mark.parametrize("encryption", ALL_LEVELS)
def test_encryption_qb_interop(
    qb_client, neptune, fixture, tmp_path: Path, encryption: str,
) -> None:
    """Neptune downloads from QB with given encryption level."""
    download_dir = tmp_path / "downloads"
    download_dir.mkdir()

    _qb_add_torrent(qb_client, fixture.torrent_path)
    _qb_wait_state(qb_client, fixture.info_hash, "uploading", timeout=30)

    inst = neptune(crypto=encryption)
    ih = inst.torrent_add(str(fixture.torrent_path), str(download_dir))
    inst.torrent_start(ih)

    # QB default is 'prefer' (allow encryption, allow plain).
    # Neptune 'require' should work (QB encrypts).
    # Neptune 'prefer' should work (both encrypt).
    # Neptune 'disable' should work (QB falls back to plain).
    inst.wait_state(ih, "seeding")
    verify_files(fixture, str(download_dir))


# ── Helpers ────────────────────────────────────────────────────────────

def _qb_add_torrent(client, torrent_path: Path) -> None:
    data = torrent_path.read_bytes()
    resp = client.post("/api/v2/torrents/add", content=data)
    assert resp.status_code == 200, f"QB add torrent failed: {resp.text[:200]}"


def _qb_wait_state(client, info_hash: str, state: str, timeout: float = 60) -> dict:
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
