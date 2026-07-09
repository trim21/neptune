import { NeptuneConnectionError, NeptuneHTTPError, NeptuneRPCError } from './errors.ts';
import type {
  AddTorrentParams,
  AddTorrentResult,
  AddTrackerParams,
  DelCustomParams,
  GetRecheckOnCompleteResult,
  GlobalSpeedLimitParams,
  InfoHashParams,
  ListTorrentParams,
  MoveTorrentParams,
  RemoveTorrentParams,
  RemoveTrackerParams,
  ReplaceTrackersParams,
  SetCustomParams,
  SetFilePriorityParams,
  SetRecheckOnCompleteParams,
  SpeedLimitParams,
  TagsParams,
  TorrentFiles,
  TorrentInfo,
  TorrentList,
  TorrentPeers,
  TorrentTrackers,
  TransferConfig,
  TransferSummary,
  UpdateCustomParams,
} from './types.ts';

// ── JSON-RPC wire types ─────────────────────────────────────────────

interface JsonRpcResponse<T = unknown> {
  jsonrpc: '2.0';
  result?: T;
  error?: { code: number; message: string; data?: string; };
  id: number;
}

// ── Method map ───────────────────────────────────────────────────────

/**
 * Maps every Neptune JSON-RPC method name to its parameter and result types.
 * Used to provide fully type-safe `.call()` invocations.
 */
export interface NeptuneMethodMap {
  'system.ping': { params: Record<string, never>; result: void; };
  transfer_summary: { params: Record<string, never>; result: TransferSummary; };
  'torrent.list': { params: ListTorrentParams; result: TorrentList; };
  'torrent.get': { params: InfoHashParams; result: TorrentInfo; };
  'torrent.files': { params: InfoHashParams; result: TorrentFiles; };
  'torrent.peers': { params: InfoHashParams; result: TorrentPeers; };
  'torrent.trackers': { params: InfoHashParams; result: TorrentTrackers; };
  'torrent.add': { params: AddTorrentParams; result: AddTorrentResult; };
  'torrent.remove': { params: RemoveTorrentParams; result: void; };
  'torrent.start': { params: InfoHashParams; result: void; };
  'torrent.stop': { params: InfoHashParams; result: void; };
  'torrent.recheck': { params: InfoHashParams; result: void; };
  'torrent.move': { params: MoveTorrentParams; result: void; };
  'torrent.add_tags': { params: TagsParams; result: void; };
  'torrent.remove_tags': { params: TagsParams; result: void; };
  'torrent.add_tracker': { params: AddTrackerParams; result: void; };
  'torrent.remove_tracker': { params: RemoveTrackerParams; result: void; };
  'torrent.replace_trackers': { params: ReplaceTrackersParams; result: void; };
  'torrent.reannounce': { params: InfoHashParams; result: void; };
  'torrent.set_file_priority': { params: SetFilePriorityParams; result: void; };
  'torrent.set_download_limit': { params: SpeedLimitParams; result: void; };
  'torrent.set_upload_limit': { params: SpeedLimitParams; result: void; };
  'torrent.custom.set': { params: SetCustomParams; result: void; };
  'torrent.custom.update': { params: UpdateCustomParams; result: void; };
  'torrent.custom.del': { params: DelCustomParams; result: void; };
  'client.set_download_limit': { params: GlobalSpeedLimitParams; result: void; };
  'client.set_upload_limit': { params: GlobalSpeedLimitParams; result: void; };
  'client.get_transfer_config': { params: Record<string, never>; result: TransferConfig; };
  'client.set_recheck_on_complete': { params: SetRecheckOnCompleteParams; result: void; };
  'client.get_recheck_on_complete': { params: Record<string, never>; result: GetRecheckOnCompleteResult; };
}

/** Union of all method name strings. */
export type NeptuneMethod = keyof NeptuneMethodMap;

// ── Client options ───────────────────────────────────────────────────

export interface NeptuneClientOptions {
  /** Full URL of the Neptune JSON-RPC endpoint (e.g. `http://127.0.0.1:8002/json_rpc`). */
  url: string;
  /** Secret token for the `Authorization` header. */
  token: string;
  /** Custom `fetch` implementation. Defaults to the global `fetch`. */
  fetch?: typeof fetch;
}

// ── Base client ──────────────────────────────────────────────────────

/**
 * Abstract base for Neptune JSON-RPC clients.
 *
 * Subclasses implement {@link request} to provide the transport layer
 * (e.g. standard `fetch`, or `node:http` for Unix domain sockets).
 */
export abstract class BaseClient {
  protected readonly url: string;
  protected readonly token: string;
  protected readonly fetch: typeof fetch;
  #id = 0;

  constructor(options: NeptuneClientOptions) {
    this.url = options.url;
    this.token = options.token;
    this.fetch = options.fetch ?? globalThis.fetch;
  }

  /** Implemented by subclasses — sends the JSON-RPC body and returns the HTTP response. */
  protected abstract request(body: string): Promise<Response>;

  /**
   * Invoke a typed Neptune JSON-RPC method.
   */
  async call<M extends NeptuneMethod>(
    method: M,
    ...args: {} extends NeptuneMethodMap[M]['params'] ? [params?: NeptuneMethodMap[M]['params']]
      : [params: NeptuneMethodMap[M]['params']]
  ): Promise<NeptuneMethodMap[M]['result']> {
    const params = args[0] ?? {};

    const id = ++this.#id;

    const body = JSON.stringify({
      jsonrpc: '2.0',
      method,
      params,
      id,
    });

    let response: Response;
    try {
      response = await this.request(body);
    } catch (err) {
      throw new NeptuneConnectionError(`Failed to reach Neptune at ${this.url}: ${String(err)}`, err);
    }

    if (!response.ok) {
      throw new NeptuneHTTPError(`Neptune returned HTTP ${response.status} ${response.statusText}`, response.status);
    }

    const json = (await response.json()) as JsonRpcResponse<NeptuneMethodMap[M]['result']>;

    if (json.error) {
      throw new NeptuneRPCError(json.error.code, json.error.message, json.error.data);
    }

    return json.result as NeptuneMethodMap[M]['result'];
  }
}
