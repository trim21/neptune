"""Pytest fixtures for Neptune e2e tests.

Provides:
    tracker_url  – HTTP tracker URL (default: http://127.0.0.1:6969/announce)
    fixture      – the loaded torrent + data fixture
    neptune      – factory to start/stop neptune instances
    qb_client    – authenticated httpx client for qBittorrent Web API

Usage:
    docker compose -f e2e/docker-compose.yml up -d
    uv run pytest e2e/ -v
"""

from __future__ import annotations

import hashlib
import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from dataclasses import dataclass
from typing import Callable, Iterator

import httpx
import pytest

# ── Paths ──────────────────────────────────────────────────────────────

E2E_DIR = Path(__file__).parent
FIXTURES_DIR = E2E_DIR / "testdata" / "fixtures"
NEPTUNE_BINARY = E2E_DIR / "testdata" / "neptune"
FIXTURE_NAME = "test-fixture"

# ── Defaults ───────────────────────────────────────────────────────────

TRACKER_URL = os.getenv("E2E_TRACKER_URL", "http://127.0.0.1:6969/announce")
QB_WEBUI = os.getenv("E2E_QB_URL", "http://127.0.0.1:8090")
QB_USERNAME = os.getenv("E2E_QB_USER", "admin")
QB_PASSWORD = os.getenv("E2E_QB_PASS", "adminadmin")
E2E_TIMEOUT = int(os.getenv("E2E_TIMEOUT", "60"))


# ── Fixture data ───────────────────────────────────────────────────────

@dataclass
class TorrentFixture:
    """A generated torrent fixture with its data directory."""

    torrent_path: Path
    data_dir: Path
    info_hash: str
    total_size: int


@pytest.fixture(scope="session")
def fixture() -> TorrentFixture:
    """Load the test torrent fixture."""
    torrent_path = FIXTURES_DIR / f"{FIXTURE_NAME}.torrent"
    data_dir = FIXTURES_DIR / FIXTURE_NAME

    if not torrent_path.exists():
        pytest.fail(
            f"Fixture not found: {torrent_path}\n"
            "Run: uv run python e2e/gen_fixtures.py"
        )

    torrent_bytes = torrent_path.read_bytes()
    info = _extract_info(torrent_bytes)
    info_hash = hashlib.sha1(info).hexdigest()

    return TorrentFixture(
        torrent_path=torrent_path,
        data_dir=data_dir,
        info_hash=info_hash,
        total_size=torrent_path.stat().st_size,
    )


def _extract_info(torrent: bytes) -> bytes:
    """Extract the info dict from a bencoded .torrent file."""
    marker = b"4:info"
    idx = torrent.index(marker)
    return torrent[idx + 6 : -1]


# ── Neptune instance ───────────────────────────────────────────────────

class NeptuneInstance:
    """A running neptune process with JSON-RPC API access."""

    def __init__(
        self,
        port: int,
        p2p_port: int,
        session_dir: str,
        crypto: str = "disable",
    ) -> None:
        self.port = port
        self.p2p_port = p2p_port
        self.url = f"http://127.0.0.1:{port}"
        self.session_dir = session_dir
        self.crypto = crypto
        self._process: subprocess.Popen[bytes] | None = None
        self.request_id = 0

    def start(self) -> None:
        if not NEPTUNE_BINARY.exists():
            pytest.fail(
                f"Neptune binary not found: {NEPTUNE_BINARY}\n"
                "Run: go build -tags release -o e2e/testdata/neptune ."
            )

        # Write a minimal config file so we can set crypto level.
        config_path = Path(self.session_dir) / "config.toml"
        config_path.write_text(f'[app]\ncrypto = "{self.crypto}"\n')

        args = [
            str(NEPTUNE_BINARY),
            "--web", f"127.0.0.1:{self.port}",
            "--session-path", self.session_dir,
            "--config-file", str(config_path),
            "--p2p-port", str(self.p2p_port),
            "--log-level", "debug",
            "--log-json",
        ]
        env = os.environ | {"NEPTUNE_LOG_SAVE_TO_FILE": "false"}

        self._process = subprocess.Popen(
            args,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            env=env,
        )
        self._wait_ready()

    def stop(self) -> None:
        if self._process is None:
            return
        self._process.send_signal(signal.SIGTERM)
        try:
            self._process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self._process.kill()
            self._process.wait()

    def _wait_ready(self, timeout: float = 30) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                resp = httpx.get(f"{self.url}/healthz", timeout=1)
                if resp.status_code == 200:
                    return
            except Exception:
                pass
            time.sleep(0.2)
        raise RuntimeError(f"Neptune on port {self.port} not ready after {timeout}s")

    # ── JSON-RPC helpers ───────────────────────────────────────────────

    def _rpc(self, method: str, params: dict | None = None) -> object:
        self.request_id += 1
        payload = {
            "jsonrpc": "2.0",
            "method": method,
            "id": self.request_id,
        }
        if params is not None:
            payload["params"] = params

        resp = httpx.post(
            f"{self.url}/json_rpc",
            json=payload,
            headers={"Authorization": "test-token"},
            timeout=10,
        )
        resp.raise_for_status()
        body = resp.json()
        if "error" in body and body["error"] is not None:
            raise RuntimeError(f"RPC error: {body['error']}")
        return body.get("result")

    def torrent_add(self, torrent_path: str, save_path: str) -> str:
        """Add a torrent from file and return info_hash."""
        torrent_b64 = Path(torrent_path).read_bytes()
        import base64
        result = self._rpc("torrent.add", {
            "torrent": base64.b64encode(torrent_b64).decode(),
            "save_path": save_path,
        })
        assert isinstance(result, dict)
        ih = result.get("info_hash")
        assert isinstance(ih, str)
        # Add tracker
        self._rpc("torrent.add_tracker", {
            "info_hash": ih,
            "url": TRACKER_URL,
            "tier": 0,
        })
        return ih

    def torrent_start(self, info_hash: str) -> None:
        self._rpc("torrent.start", {"info_hash": info_hash})

    def torrent_stop(self, info_hash: str) -> None:
        self._rpc("torrent.stop", {"info_hash": info_hash})

    def torrent_get(self, info_hash: str) -> dict:
        result = self._rpc("torrent.get", {"info_hash": info_hash})
        assert isinstance(result, dict)
        return result

    def torrent_recheck(self, info_hash: str) -> None:
        self._rpc("torrent.recheck", {"info_hash": info_hash})

    def wait_state(
        self, info_hash: str, state: str, timeout: float | None = None
    ) -> dict:
        """Poll until torrent reaches a given state (e.g. 'seeding')."""
        if timeout is None:
            timeout = E2E_TIMEOUT
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            info = self.torrent_get(info_hash)
            current = info.get("state", "").lower()
            if current == state.lower():
                return info
            time.sleep(0.5)
        raise RuntimeError(
            f"Torrent {info_hash[:8]} state={state} not reached "
            f"(current={info.get('state')}) after {timeout}s"
        )


@pytest.fixture
def neptune(tmp_path_factory: pytest.TempPathFactory) -> Iterator[Callable[[], NeptuneInstance]]:
    """Start a neptune instance, return it, stop after test."""
    instances: list[NeptuneInstance] = []

    def _start(p2p_port: int | None = None, web_port: int | None = None,
               crypto: str = "disable") -> NeptuneInstance:
        p = web_port or (18002 + len(instances))
        pp = p2p_port or (50048 + len(instances))
        session = str(tmp_path_factory.mktemp(f"neptune-{len(instances)}"))
        inst = NeptuneInstance(p, pp, session, crypto=crypto)
        inst.start()
        instances.append(inst)
        return inst

    yield _start

    for inst in instances:
        inst.stop()


# ── qBittorrent ────────────────────────────────────────────────────────

@pytest.fixture(scope="session")
def qb_client() -> Iterator[httpx.Client]:
    """Authenticated qBittorrent Web API client."""
    import subprocess

    client = httpx.Client(base_url=QB_WEBUI, timeout=30)

    # Wait for QB to be ready.
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            resp = client.get("/api/v2/app/version")
            if resp.status_code == 200:
                break
        except Exception:
            pass
        time.sleep(1)
    else:
        pytest.fail("qBittorrent not ready")

    # Detect password from Docker logs (linuxserver image generates random pw).
    result = subprocess.run(
        ["docker", "logs", "neptune-e2e-qb"],
        capture_output=True, text=True,
    )
    password = QB_PASSWORD
    for line in reversed(result.stderr.split("\n") + result.stdout.split("\n")):
        if "temporary password" in line.lower():
            password = line.strip().split()[-1]
            break

    resp = client.post(
        "/api/v2/auth/login",
        data={"username": QB_USERNAME, "password": password},
    )
    if resp.status_code != 200:
        pytest.fail(f"QB login failed: {resp.status_code}")

    yield client

    client.close()


# ── Helpers ────────────────────────────────────────────────────────────

def verify_files(fixture: TorrentFixture, download_dir: str) -> None:
    """Verify downloaded files match the fixture."""
    for f in fixture.data_dir.rglob("*"):
        if f.is_dir():
            continue
        rel = f.relative_to(fixture.data_dir)
        got = Path(download_dir) / FIXTURE_NAME / rel

        expected_data = f.read_bytes()
        got_data = got.read_bytes()

        assert len(expected_data) == len(got_data), (
            f"Size mismatch: {rel} (want {len(expected_data)}, got {len(got_data)})"
        )
        assert expected_data == got_data, (
            f"Content mismatch: {rel}"
        )
