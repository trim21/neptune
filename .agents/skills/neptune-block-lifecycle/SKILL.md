---
name: neptune-block-lifecycle
description: >
  Complete lifecycle of a block in Neptune's BitTorrent download pipeline: None
  → Requested → Responded → disk write → hash verification (completed or
  rolled back). Covers piecePicker, peer, backgroundResHandler, and checkPiece
  module interactions. Useful for understanding or debugging the download flow.
---

# Block Lifecycle

> Key source files:
> - `internal/download/piece_picker.go` — block state machine + picker logic
> - `internal/download/download.go` — response handling, disk write, hash verification
> - `internal/download/p.go` — peer lifecycle, request/response send & receive
> - `internal/download/peer_methods.go` — peer interface method implementations

## State Values

```go
blockStateNone      blockState = 0  // free, not yet requested from any peer
blockStateRequested              = 1  // Request sent, waiting for Piece response
blockStateResponded              = 2  // Piece data received, waiting for disk write
```

Stored in `blockStates` (2 bits per block, 4 blocks per byte). There is no independent "completed" state for blocks — completion is tracked at the piece level by `completedBm` after hash verification passes.

## Global Data Flow

```
requestABlock()           sendBlockRequests()      peer event loop               backgroundResHandler()
    │                          │                      │                           │
    ├─ pickPieces()            │                      │                           │
    │  returns freeBlocks      │                      │                           │
    ├─ EnqueueBlock()          │                      │                           │
    │  → requestQueue          │                      │                           │
    ├─ markAsRequesting()      │                      │                           │
    │  None → Requested        │                      │                           │
    │                          ├─ Request() wire msg   │                           │
    │                          │  → myRequests         ├─ Piece msg received ─────┤
    │                          │                      │   resIsValid()             │
    │                          │                      │   → resChan                │
    │                          │                      │                            ├─ markAsResponded()
    │                          │                      │                            │   Requested → Responded
    │                          │                      │                            ├─ chunk.heap.Push()
    │                          │                      │                            ├─ flushContiguousFromHeap()
    │                          │                      │                            │   store.WriteChunk()
    │                          │                      │                            ├─ checkPiece()
    │                          │                      │                            │   VerifyPiece(hash)
    │                          │                      │                            ├─ ✅: completedBm.Set()
    │                          │                      │                            │      weHave() → remove piece
    │                          │                      │                            ├─ ❌: resetPiece()
    │                          │                      │                            │      back to None, retry
```

## Phase 1: None → Requested

**Entry point**: `download.requestABlock(p)`

1. **Calculate demand**: `desiredQueueSize - myRequests - requestQueue` determines how many more blocks to request.
2. **Pick blocks**: `piecePicker.pickPieces()` selects blocks by strategy:
   - **Startup mode** (0 completed pieces, no downloading pieces): randomly pick block 0 of a medium-rarity piece.
   - **Phase 1 — partial pieces**: prioritize continuing partially-downloaded pieces (highest completion ratio first).
   - **Phase 2 — suggested pieces**: pieces suggested by the peer.
   - **Phase 3 — rarest-first / sequential**: scan the global priority array `pp.pieces` by strategy.
   - Returns two categories: `freeBlocks` (idle) and `busyBlocks` (already requested by other peers, used for endgame replication).
3. **Enqueue**: `p.EnqueueBlock()` → appends to `peerImpl.requestQueue` (peer-local queue).
4. **Global mark**: `picker.markAsRequesting()`:
   - `blockInfos[idx] = blockStateRequested`
   - `downloadQueueSize++`
   - `downloadingPiece.requested++`
   - If the piece is not yet in `downloadingPieces`, calls `addDownloadingPiece()`
5. **Send**: `p.SendBlockRequests()` → `sendBlockRequests()`:
   - Dequeues from `requestQueue` one by one, calls `p.Request(chunk)`
   - Records in `myRequests` map (key = ChunkRequest, value = time.Time, for timeout detection)
   - Sends wire message via `sendEventX(Event{Event: proto.Request, Req: req})`

## Phase 2: Requested → Responded

**Entry point**: peer `start()` event loop receives `proto.Piece` event

6. **Validation**: `resIsValid()` does `LoadAndDelete` from `myRequests`, verifying this is a block we actually requested, and records RTT.
7. **Delivery**: `p.d.resChan <- event.Res` places the response into the global channel.
8. **Mark Responded**: `backgroundResHandler()` → `handleRes()`:
   - **Rate limiting**: global limiter first, then per-torrent limiter.
   - `picker.markAsResponded(pieceIndex, blockIndex)`:
     - `blockInfos[idx] = blockStateResponded`
     - `downloadQueueSize--`
     - `downloadingPiece.requested--`
     - `downloadingPiece.responded++`
   - Pushes into sorted heap: `d.chunk.heap.Push(responseChunk{...})`, sorted by global chunk ID.

## Phase 3: Responded → Disk Write

**Trigger conditions** (bottom of `handleRes`):
- All blocks of a piece have arrived (in heap or already done) → `handlePieceFromHeap()`
- Heap ≥ 1000 or oldest chunk older than 5 seconds → `flushContiguousFromHeap()`

9. **Coalesced write**: `flushContiguousFromHeap()`:
   - Pops the head from heap, merges up to 10 contiguous chunks (reducing syscalls)
   - Calls `d.store.WriteChunk(pieceIdx, begin, data)` to write to disk
   - Updates chunk bitmap: `d.chunk.done.Set(pi)`, `d.chunk.pending.Remove(pi)`

## Phase 4: Hash Verification → Final State

**Entry point**: `checkPieceBitmapDone()` detects all chunks of a piece have been written → `checkPiece()`

10. **Verify**: `d.store.VerifyPiece(pieceIndex, hash)` computes SHA1.

### ✅ Passes → Completed

- `d.completedBm.Set(pieceIndex)` — piece-level completion marker
- `picker.weHave(pieceIndex)`:
  - All block states for the piece → `blockStateResponded`
  - Removed from `downloadingPieces`
  - `numCompletedPieces++`
- Sends `Have` message to all peers
- Triggers `scheduleRequestSignal` so other peers can request new blocks
- Checks if fully complete → `checkDone()` → transitions to `Seeding`, releases picker

### ❌ Fails → Back to None

- `picker.resetPiece(pieceIndex)`:
  - All block states for the piece → `blockStateNone`
  - `downloadQueueSize` adjusted accordingly
  - `downloadingPiece.responded = 0; requested = 0`
  - **Keeps the downloadingPiece record** — prevents pickPieces from entering startup mode and randomly picking a different piece, ensuring this piece is re-prioritized
- Chunk bitmap bits for this piece all cleared (`d.chunk.done.Remove(i)`)
- Records corrupt count
- Triggers `scheduleRequestSignal` to re-schedule

## Other Termination Paths

> **Invariant**: every picker claim (Requested mark, or a `retriedBlocks`
> increment for an endgame duplicate) must end exactly once — via
> `markAsResponded()` or `abortDownload()`. Once the peer read loop consumes a
> request from `myRequests` (`resIsValid`), timeout/close/reject will never
> see it again, so any path that drops the response afterwards must abort the
> claim explicitly, or the block stays Requested forever and the piece can
> never complete.

| Path | Mechanism |
|---|---|
| **Timeout** (30s) | `checkRequestTimeouts()` iterates `myRequests`; any request stale >30s → `abortDownload()` → `blockStateNone` |
| **Reject** | Peer sends `proto.Reject` → `abortDownload()` + remove from `requestQueue` |
| **Peer disconnect** | `peerImpl.Close()` → `abortAllRequestsLocked()` aborts `myRequests` + `requestQueue` |
| **Snub** | 5 consecutive timeouts → marks peer as snubbed, `abortAllRequestsLocked()` aborts all in-flight **and queued** requests, desiredQueueSize drops to 1 |
| **State leaves Downloading** (Stop / queue demote / Moving) | `backgroundResHandler()` drops the response but first calls `abortDownload()` for its block |
| **Peer ctx canceled mid-delivery** | `resChan` send loses to `<-p.ctx.Done()` → `abortDownload()` before dropping the response |

## Key Data Structure Relationships

```
piecePicker
├── blockInfos     [][]2bits      global block state (None/Requested/Responded)
├── downloadingPieces []          in-flight piece progress tracking
├── pieces         []uint32       candidate piece priority array
├── completedBm    *Bitmap        piece-level completion marker (shared with Download)
└── availability   []uint16       per-piece replica count across all peers

peerImpl
├── requestQueue   []pieceBlock                blocks to send (marked Requested, not yet on wire)
├── myRequests     map[ChunkRequest]time.Time  requests already sent on wire
└── Bitmap         *Bitmap                     pieces this peer has

Download
├── chunk.heap     Heap[responseChunk]         responses waiting for disk write
├── chunk.done     Bitmap                      chunks already written to disk
├── chunk.pending  Bitmap                      chunks currently in heap
└── completedBm    *Bitmap                     pieces that passed hash verification
```
