"""Neptune ↔ Neptune interop — parametrized matrix.

Matrix (6 combinations):
    encryption ∈ {require, prefer, disable}
    scenario   ∈ {seed_leech, dual_download}
"""

import shutil
from pathlib import Path

import pytest

ENCRYPTION_LEVELS = ["require", "prefer", "disable"]


@pytest.mark.e2e
@pytest.mark.parametrize("encryption", ENCRYPTION_LEVELS)
def test_neptune_seed_leech(neptune, fixture, tmp_path: Path, encryption: str) -> None:
    """Seeder has complete data, leecher starts empty and downloads."""
    seeder = neptune(crypto=encryption)
    leecher = neptune(crypto=encryption)

    seeder_data = tmp_path / "seeder"
    seeder_data.mkdir()
    shutil.copytree(fixture.data_dir, seeder_data / fixture.data_dir.name)

    ih = seeder.torrent_add(str(fixture.torrent_path), str(seeder_data))
    seeder.torrent_recheck(ih)
    seeder.torrent_start(ih)
    seeder.wait_state(ih, "seeding")

    leecher_data = tmp_path / "leecher"
    leecher_data.mkdir()
    leecher.torrent_add(str(fixture.torrent_path), str(leecher_data))
    leecher.torrent_start(ih)
    leecher.wait_state(ih, "seeding")

    verify_files(fixture, str(leecher_data))


@pytest.mark.e2e
@pytest.mark.parametrize("encryption", ENCRYPTION_LEVELS)
def test_neptune_dual_download(neptune, fixture, tmp_path: Path, encryption: str) -> None:
    """Two neptune instances, only one has data initially."""
    seeder = neptune(crypto=encryption)
    leecher = neptune(crypto=encryption)

    seeder_data = tmp_path / "seeder"
    seeder_data.mkdir()
    shutil.copytree(fixture.data_dir, seeder_data / fixture.data_dir.name)

    ih = seeder.torrent_add(str(fixture.torrent_path), str(seeder_data))
    seeder.torrent_recheck(ih)
    seeder.torrent_start(ih)
    seeder.wait_state(ih, "seeding")

    leecher_data = tmp_path / "leecher"
    leecher_data.mkdir()
    leecher.torrent_add(str(fixture.torrent_path), str(leecher_data))
    leecher.torrent_start(ih)
    leecher.wait_state(ih, "seeding")

    verify_files(fixture, str(leecher_data))
