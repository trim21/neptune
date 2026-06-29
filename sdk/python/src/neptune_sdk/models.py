from __future__ import annotations

from dataclasses import dataclass

# ── Shared domain types ──────────────────────────────────────────────


@dataclass(frozen=True, slots=True, kw_only=True)
class MainDataTorrent:
    """A torrent entry returned by torrent.list / torrent.remove."""

    hash: str
    name: str
    state: str
    comment: str
    directory_base: str
    message: str
    tracker_errors: dict[str, str]
    tags: list[str]
    custom: dict[str, str]
    download_rate: int
    download_total: int
    upload_rate: int
    upload_total: int
    connection_count: int
    completed: int
    total_length: int
    selected_size: int
    add_at: int
    private: bool
    total_seeding: int
    total_downloading: int
    connected_seeding: int
    connected_downloading: int


@dataclass(frozen=True, slots=True, kw_only=True)
class TransferSummary:
    """Global transfer rates and totals."""

    download_rate: int
    download_total: int
    upload_rate: int
    upload_total: int


@dataclass(frozen=True, slots=True, kw_only=True)
class TorrentFile:
    """A single file inside a torrent."""

    path: list[str]
    index: int
    progress: float
    size: int


@dataclass(frozen=True, slots=True, kw_only=True)
class Peer:
    """A connected peer."""

    address: str
    client: str
    progress: float
    download_rate: int
    upload_rate: int
    is_incoming: bool


@dataclass(frozen=True, slots=True, kw_only=True)
class Tracker:
    """A tracker entry."""

    url: str
    tier: int
    message: str


@dataclass(frozen=True, slots=True, kw_only=True)
class TorrentInfo:
    """Basic torrent metadata from torrent.get."""

    name: str
    tags: list[str]
    custom: dict[str, str]


# ── Request types ─────────────────────────────────────────────────────


@dataclass(frozen=True, slots=True, kw_only=True)
class AddTorrentRequest:
    """Parameters for torrent.add."""

    torrent_file: bytes
    download_dir: str | None = None
    tags: list[str] | None = None
    custom: dict[str, str] | None = None
    selected_files: list[int] | None = None
    is_base_dir: bool = False
    skip_hash_check: bool = False


@dataclass(frozen=True, slots=True, kw_only=True)
class InfoHashRequest:
    """Common request that only needs an info_hash."""

    info_hash: str


@dataclass(frozen=True, slots=True, kw_only=True)
class MoveTorrentRequest:
    """Parameters for torrent.move."""

    info_hash: str
    target_base_path: str


@dataclass(frozen=True, slots=True, kw_only=True)
class RemoveTorrentRequest:
    """Parameters for torrent.remove."""

    info_hash: str
    delete_data: bool = False


@dataclass(frozen=True, slots=True, kw_only=True)
class TagsRequest:
    """Parameters for torrent.add_tags / torrent.remove_tags."""

    info_hash: str
    tags: list[str]


@dataclass(frozen=True, slots=True, kw_only=True)
class SetFilePriorityRequest:
    """Parameters for torrent.set_file_priority."""

    info_hash: str
    file_ids: list[int]
    priority: int = 0


@dataclass(frozen=True, slots=True, kw_only=True)
class SetSpeedLimitRequest:
    """Parameters for torrent.set_download_limit / torrent.set_upload_limit."""

    info_hash: str
    limit: int = 0


@dataclass(frozen=True, slots=True, kw_only=True)
class SetGlobalSpeedLimitRequest:
    """Parameters for client.set_download_limit / client.set_upload_limit."""

    limit: int = 0


@dataclass(frozen=True, slots=True, kw_only=True)
class ListTorrentRequest:
    """Parameters for torrent.list."""

    keys: list[str] | None = None


@dataclass(frozen=True, slots=True, kw_only=True)
class SetCustomRequest:
    """Parameters for torrent.custom.set."""

    info_hash: str
    key: str
    value: str


@dataclass(frozen=True, slots=True, kw_only=True)
class UpdateCustomRequest:
    """Parameters for torrent.custom.update."""

    info_hash: str
    custom: dict[str, str]


@dataclass(frozen=True, slots=True, kw_only=True)
class DelCustomRequest:
    """Parameters for torrent.custom.del."""

    info_hash: str
    key: str


@dataclass(frozen=True, slots=True, kw_only=True)
class AddTrackerRequest:
    """Parameters for torrent.add_tracker."""

    info_hash: str
    url: str
    tier: int = 0


@dataclass(frozen=True, slots=True, kw_only=True)
class RemoveTrackerRequest:
    """Parameters for torrent.remove_tracker."""

    info_hash: str
    url: str


@dataclass(frozen=True, slots=True, kw_only=True)
class ReplaceTrackersRequest:
    """Parameters for torrent.replace_trackers."""

    info_hash: str
    replacements: dict[str, str]


# ── Response types ────────────────────────────────────────────────────


@dataclass(frozen=True, slots=True, kw_only=True)
class TorrentListResponse:
    """Response for torrent.list and torrent.remove."""

    torrents: list[MainDataTorrent]


@dataclass(frozen=True, slots=True, kw_only=True)
class AddTorrentResponse:
    """Response for torrent.add."""

    info_hash: str


@dataclass(frozen=True, slots=True, kw_only=True)
class TorrentFilesResponse:
    """Response for torrent.files."""

    files: list[TorrentFile]


@dataclass(frozen=True, slots=True, kw_only=True)
class TorrentPeersResponse:
    """Response for torrent.peers."""

    peers: list[Peer]


@dataclass(frozen=True, slots=True, kw_only=True)
class TorrentTrackersResponse:
    """Response for torrent.trackers."""

    trackers: list[Tracker]
