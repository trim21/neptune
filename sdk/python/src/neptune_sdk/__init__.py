"""Neptune SDK — async Python client for the Neptune BitTorrent JSON-RPC API."""

from .client import NeptuneClient
from .exceptions import NeptuneConnectionError, NeptuneError, NeptuneRPCError
from .models import (
    AddTorrentRequest,
    AddTorrentResponse,
    InfoHashRequest,
    MainDataTorrent,
    Peer,
    RemoveTorrentRequest,
    SetFilePriorityRequest,
    SetGlobalSpeedLimitRequest,
    SetSpeedLimitRequest,
    TagsRequest,
    TorrentFile,
    TorrentFilesResponse,
    TorrentInfo,
    TorrentListResponse,
    TorrentPeersResponse,
    TorrentTrackersResponse,
    Tracker,
    TransferSummary,
)

__all__ = [
    # client
    "NeptuneClient",
    # exceptions
    "NeptuneError",
    "NeptuneRPCError",
    "NeptuneConnectionError",
    # models
    "AddTorrentRequest",
    "AddTorrentResponse",
    "InfoHashRequest",
    "MainDataTorrent",
    "Peer",
    "RemoveTorrentRequest",
    "SetFilePriorityRequest",
    "SetGlobalSpeedLimitRequest",
    "SetSpeedLimitRequest",
    "TagsRequest",
    "TorrentFile",
    "TorrentFilesResponse",
    "TorrentInfo",
    "TorrentListResponse",
    "TorrentPeersResponse",
    "TorrentTrackersResponse",
    "Tracker",
    "TransferSummary",
]
