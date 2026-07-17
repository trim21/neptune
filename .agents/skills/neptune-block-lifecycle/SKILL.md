---
name: neptune-block-lifecycle
description: >
  Complete lifecycle of a named block claim in Neptune's BitTorrent pipeline:
  free, claimed, response accepted, disk write, and hash verification.
---

# Block Lifecycle

> Key source files:
> - `internal/piece_picker/piece_picker.go` - block state and claim indexes
> - `internal/piece_picker/request_ablock.go` - atomic selection and claiming
> - `internal/download/p.go` - peer queue and wire-request tracking
> - `internal/download/download.go` - response acceptance, write, and verification

## State And Ownership

Blocks retain the compact 2-bit state representation:

```go
blockStateNone      // free
blockStateRequested // at least one live named claim
blockStateResponded // one response accepted; awaiting write/hash verification
```

Ownership is sparse and exists only for live requests:

```text
token -> {owner peer ID, piece/block}
global block index -> up to three tokens (original + two endgame duplicates)
peer ID -> owned token set
```

Tokens increase monotonically and are not reset. A late timeout, reject, or
response therefore cannot terminate a newer claim for the same peer/block.

## Request Flow

```text
peer scheduler
  -> PiecePicker.PickAndClaim(peer, capacity)
       a read-only gate checks the owning Download's atomic state
       selection and claim registration occur under one picker lock
  -> requestQueue stores BlockClaim
  -> myRequests stores {BlockClaim, sentAt} before sending Request
  -> Piece response removes and returns that exact BlockClaim
  -> chunkSubmit carries the claim to backgroundResHandler
  -> PiecePicker.AcceptResponse(claim)
  -> pending heap -> disk write -> piece hash verification
```

Production code must never reconstruct ownership from piece/block coordinates.
Queue entries, wire tracking, timeout, reject, and response paths all carry the
original `BlockClaim`.

## Terminal Operations

- `AcceptResponse(claim)` accepts one live response, moves the block to
  Responded, and invalidates every sibling endgame claim atomically.
- `ReleaseClaim(claim)` releases only that token. The block returns to None
  only after its final live claim is released.
- `ReleasePeerClaims(peerID)` releases queued, in-flight, and response-handoff
  claims owned by a peer.
- `RequestGate` holds a read-only reference to the owning Download's atomic
  state. It is the single source used for best-effort admission of new claims;
  the picker does not mirror lifecycle state in a mutable boolean.
- `ReleaseAllClaims()` promptly releases claims visible while it holds the
  picker lock. A racing `PickAndClaim` may return claims afterward, and its
  caller remains responsible for consuming those exact tokens.

False from `AcceptResponse` or `ReleaseClaim` means the token is stale. This is
normal for late endgame responses and racing termination paths; it must not
modify block state.

## Termination Paths

- Timeout/reject/choke/queue failure: remove the exact peer-local entry, then
  call `ReleaseClaim` with its stored token.
- Peer close or snub: under `requestMu`, call `ReleasePeerClaims`, clear
  `myRequests`, and clear `requestQueue`.
- Download becomes PendingDownloading: publishing the atomic state closes the
  request gate. Racing picks may still return claims; peer enqueue consumes or
  releases them, while existing in-flight responses remain valid.
- Download leaves Downloading/PendingDownloading: publish the terminal state,
  call `ReleaseAllClaims`, and clear each peer's download-side tracking while
  keeping the connections. This cleanup accelerates convergence and is not a
  global barrier.
- Accepted response: sibling tokens become stale immediately; their peer-local
  entries disappear later through response, timeout, or bulk clear.
- Hash succeeds: `WeHave` marks the piece Responded and invalidates claims.
- Hash fails: `ResetPiece` invalidates claims and returns blocks to None.
- Recheck: `ResetAll` clears block state/indexes without resetting token IDs.

## Invariants

1. Requested implies at least one entry in the block claim index.
2. Every active token appears in token, block, and peer indexes exactly once.
3. Multiple claims produce one Requested -> Responded transition, or return to
   None only after the last release.
4. Peer-local request deletion and claim extraction occur under `requestMu`, so
   timeout/reject/response races have one winner.
5. PendingDownloading does not intentionally schedule new claims, but racing
   picks are allowed. Once scheduling quiesces, every such token must converge
   through response, release, timeout, peer close, or terminal cleanup.
