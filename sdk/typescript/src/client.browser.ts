import {BaseClient} from './base-client.ts';

/**
 * Standard `fetch`-based Neptune JSON-RPC client.
 *
 * This is the `browser` / `default` export resolved by bundlers.
 * In Node.js the `"node"` condition automatically picks the Unix-socket-aware
 * build instead.
 *
 * @example
 * ```ts
 * import { NeptuneClient } from '@neptune/sdk';
 *
 * const client = new NeptuneClient({
 *   url: 'http://127.0.0.1:8002/json_rpc',
 *   token: 'your-secret-token',
 * });
 *
 * await client.call('system.ping');
 * const { torrents } = await client.call('torrent.list');
 * ```
 */
export class NeptuneClient extends BaseClient {
  protected async request(body: string): Promise<Response> {
    return this.fetch(this.url, {
      method: 'POST',
      headers: {
        Authorization: this.token,
        'Content-Type': 'application/json',
      },
      body,
    });
  }
}
