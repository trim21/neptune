// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

/**
 * This file was auto-generated by openapi-typescript.
 * Do not make direct changes to the file.
 */

export type paths = {
    "torrent.add": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** Add Torrent */
        post: operations["torrent.add"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
};
export type webhooks = Record<string, never>;
export type components = {
    schemas: {
        WebAddTorrentRequest: {
            /** @description download dir */
            download_dir?: string;
            /** @description if true, will not append torrent name to download_dir */
            is_base_dir?: boolean;
            tags?: string[] | null;
            /**
             * Format: base64
             * @description base64 encoded torrent file content
             */
            torrent_file: string;
        };
        WebAddTorrentResponse: {
            /** @description torrent file hash */
            info_hash: string;
        };
    };
    responses: never;
    parameters: never;
    requestBodies: never;
    headers: never;
    pathItems: never;
};
export type $defs = Record<string, never>;
export interface operations {
    "torrent.add": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: {
            content: {
                "application/json": components["schemas"]["WebAddTorrentRequest"];
            };
        };
        responses: {
            /** @description OK */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["WebAddTorrentResponse"];
                };
            };
        };
    };
}
