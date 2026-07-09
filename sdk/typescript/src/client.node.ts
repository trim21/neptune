import type {NeptuneClientOptions} from './base-client.ts';
import {BaseClient} from './base-client.ts';

export * from './errors.ts';
export type * from './base-client.ts';
export type * from './types.ts';

/**
 * Node.js Neptune JSON-RPC client with Unix domain socket support.
 *
 * This is the `"node"` export resolved by bundlers / Node.js runtime.
 * A default `fetch` adapter that handles `unix://` URLs via `node:http`
 * is provided automatically.
 *
 * @example
 * ```ts
 * import { NeptuneClient } from '@neptune/sdk';
 *
 * const client = new NeptuneClient({
 *   url: 'unix:///run/neptune.sock',
 *   token: 'your-secret-token',
 * });
 *
 * await client.call('system.ping');
 * ```
 */
export class NeptuneClient extends BaseClient {
  constructor(options: NeptuneClientOptions) {
    super({...options, fetch: options.fetch ?? createNodeFetch()});
  }

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

// ── Internal fetch adapter ───────────────────────────────────────────

function createNodeFetch(): typeof fetch {
  return async (input, init?) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith('unix://')) {
      const http = await import('node:http');
      const socketPath = url.slice(7);
      return new Promise<Response>((resolve, reject) => {
        const req = http.request(
          {
            socketPath,
            path: '/json_rpc',
            method: init?.method ?? 'GET',
            headers: {
              ...(init?.headers as Record<string, string> | undefined),
              'Content-Length': Buffer.byteLength((init?.body as string) ?? '').toString(),
            },
          },
          (res) => {
            const chunks: Buffer[] = [];
            res.on('data', (chunk: Buffer) => chunks.push(chunk));
            res.on('end', () => {
              const text = Buffer.concat(chunks).toString('utf-8');
              resolve(new Response(text, {status: res.statusCode ?? 502, statusText: res.statusMessage ?? ''}));
            });
          },
        );
        req.on('error', reject);
        if (init?.body) req.write(init.body as string);
        req.end();
      });
    }
    return globalThis.fetch(input, init);
  };
}
