"""Neptune SDK — async Python client for the Neptune BitTorrent JSON-RPC API."""

from .client import NeptuneClient
from .exceptions import NeptuneConnectionError, NeptuneError, NeptuneRPCError
from .models import (
    AddTorrentRequest,
    AddTorrentResponse,
    DelCustomRequest,
    InfoHashRequest,
    ListTorrentRequest,
    MainDataTorrent,
    MoveTorrentRequest,
    Peer,
    RemoveTorrentRequest,
    SetCustomRequest,
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
    UpdateCustomRequest,
)

__all__ = [
    # client
    "NeptuneClient",
    # exceptions
    "NeptuneError",
    "NeptuneRPCError",
    "NeptuneConnectionError",
    # request models
    "AddTorrentRequest",
    "DelCustomRequest",
    "InfoHashRequest",
    "ListTorrentRequest",
    "MoveTorrentRequest",
    "RemoveTorrentRequest",
    "SetCustomRequest",
    "SetFilePriorityRequest",
    "SetGlobalSpeedLimitRequest",
    "SetSpeedLimitRequest",
    "TagsRequest",
    "UpdateCustomRequest",
    # response / domain models
    "AddTorrentResponse",
    "MainDataTorrent",
    "Peer",
    "TorrentFile",
    "TorrentFilesResponse",
    "TorrentInfo",
    "TorrentListResponse",
    "TorrentPeersResponse",
    "TorrentTrackersResponse",
    "Tracker",
    "TransferSummary",
]
