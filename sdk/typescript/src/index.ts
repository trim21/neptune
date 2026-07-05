export {NeptuneClient} from './client.js';
export type {NeptuneClientOptions, NeptuneMethod, NeptuneMethodMap} from './client.js';

export {NeptuneHTTPError, NeptuneRPCError} from './errors.js';

export type {
  AddTorrentParams,
  AddTorrentResult,
  AddTrackerParams,
  DelCustomParams,
  GlobalSpeedLimitParams,
  InfoHashParams,
  ListTorrentParams,
  MoveTorrentParams,
  Peer,
  RemoveTorrentParams,
  RemoveTrackerParams,
  ReplaceTrackersParams,
  SetCustomParams,
  SetFilePriorityParams,
  SpeedLimitParams,
  TagsParams,
  Torrent,
  TorrentFile,
  TorrentFiles,
  TorrentInfo,
  TorrentList,
  TorrentPeers,
  TorrentState,
  TorrentTrackers,
  Tracker,
  TransferSummary,
  UpdateCustomParams,
} from './types.js';
