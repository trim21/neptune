---
name: add-http-api
description: Guide for adding new JSON-RPC HTTP API methods in Neptune. Use when asked to add a new API endpoint, RPC method, or HTTP handler.
---

# Adding JSON-RPC HTTP APIs to Neptune

Neptune uses a custom JSON-RPC 2.0 framework with auto-generated OpenAPI specs. All API methods are registered via `usecase.Interactor` + `jsonrpc.Handler.Add()`.

## Anatomy of a New API Method

Each API method is a single Go file in `internal/web/` (package `web`) with three parts:

1. **Request struct** — fields tagged with `json:""`, `description:""`, `required:"true"`, and optionally `validate:"required"` for the validator.
2. **Response struct** — fields tagged with `json:""` and optionally `required:"true"`.
3. **Registration function** — creates a `usecase.Interactor`, calls `u.SetName("method.name")` and `h.Add(u)`.

## Step-by-Step

### 1. Create a new file: `internal/web/<feature>.go`

```go
package web

import (
    "context"

    "github.com/swaggest/usecase"

    "neptune/internal/core"
    "neptune/internal/web/jsonrpc"
)

type myMethodRequest struct {
    ParamA string `description:"description of param_a" json:"param_a" required:"true"`
    ParamB int64  `description:"description of param_b" json:"param_b"`
}

type myMethodResponse struct {
    Result string `json:"result"`
}

func myMethod(h *jsonrpc.Handler, c *core.Client) {
    u := usecase.NewInteractor(
        func(ctx context.Context, req *myMethodRequest, res *myMethodResponse) error {
            // implementation
            return nil
        },
    )
    u.SetName("my.method")
    h.Add(u)
}
```

### 2. Register the method in `internal/web/web.go`

In the `New()` function, add a call to your registration function alongside the existing ones:

```go
myMethod(h, c)
```

That's it — no separate route definition, no middleware wiring. All JSON-RPC methods go through the single `POST /json_rpc` handler.

## Request/Response Struct Conventions

- **Use pointer types in `usecase.NewInteractor`**: For request structs, always use `*MyRequest` (pointer). Response structs can be value or pointer — existing code uses both `*X` and `X`.
- **Public methods use exported names**: e.g., `AddTorrentRequest`; internal methods use unexported: `addTagsRequest`.
- **`description` + `json` + `required` tags** are needed for OpenAPI generation. The `description` tag populates the OpenAPI spec.
- **Validator**: `go-playground/validator` auto-validates request structs. Use `validate:"required"` for additional validation beyond `required:"true"`.
- **Empty response struct**: `type myResponse struct{}` — the JSON-RPC framework handles empty objects correctly.

## Info Hash Validation

Info hash fields must be 40 hex characters (sha1.Size*2 == 40). The canonical pattern:

```go
if len(req.InfoHash) != sha1.Size*2 {
    return errInvalidInfoHash
}
raw, err := hex.DecodeString(req.InfoHash)
if err != nil {
    return errInvalidInfoHash
}
hash := metainfo.Hash(raw)
```

A shared helper `checkInfoHash()` exists in `internal/web/torrent_speed_limit.go:19` — prefer using it for new methods.

## Error Handling

### Application error codes

Use `CodeError(code int, err error)` from `internal/web/error.go` to return errors with custom numeric codes:

```go
if err != nil {
    return CodeError(1, errgo.Wrap(err, "description of what failed"))
}
```

The JSON-RPC handler (`internal/web/jsonrpc/handler.go:290`) checks for the `ErrWithAppCode` interface. When present, the error code replaces the default `-32603` (InternalError) and the error message is sent verbatim.

**Error code conventions by existing methods:**

| Code | Meaning |
|------|---------|
| 1 | Invalid parameter (bad info_hash, invalid limit value) |
| 2 | Operation failed (torrent parse error, get download error, schedule move error) |
| 4 | Validation rejected (piece length too big, etc.) |
| 5 | Specific operation failure (add torrent failure) |

### Error wrapping

Always wrap errors with `trim21/errgo`:
```go
return errgo.Wrap(err, "failed to do something")
```

## Method Naming

- Method name format: `"scope.action"` or `"scope.sub_scope.action"`
- Examples: `torrent.add`, `torrent.start`, `torrent.set_file_priority`, `client.set_upload_limit`, `transfer_summary`, `system.ping`
- **IMPORTANT**: `SetName()` must be called before `h.Add(u)`. The name string becomes both the JSON-RPC `method` field and the OpenAPI `operationId`.

## How `h.Add(u)` Works

`Handler.Add()` in `internal/web/jsonrpc/handler.go:95`:
1. Extracts the method name via `usecase.HasName` interface (panics if missing).
2. Wraps the usecase through registered middlewares.
3. Stores the method in the handler's method map.
4. Auto-registers the method with the OpenAPI reflector, adding `api-key` security.

The `ServeHTTP` handler then:
- Decodes the incoming JSON-RPC `method` field
- Looks it up in the methods map
- Reflectively creates input/output buffers, unmarshals params into them
- Calls `u.Interact(ctx, input, output)`
- Marshals the output into the JSON-RPC result

## Auth

All JSON-RPC methods are behind a single auth middleware in `web.go:138`:

```go
r.With(middleware.NoCache, auth).Handle("POST /json_rpc", h)
```

The auth check compares the `Authorization` header against the configured token. Failed auth returns a JSON-RPC error with `CodeInvalidRequest (-32600)`.

**No per-method auth handling is needed** — it's handled uniformly at the route level.

## Dependencies / Imports

Standard imports for a new API file:

```go
import (
    "context"
    "crypto/sha1"
    "encoding/hex"

    "github.com/swaggest/usecase"
    "github.com/trim21/errgo"

    "neptune/internal/core"
    "neptune/internal/metainfo"
    "neptune/internal/web/jsonrpc"
)
```

## Checklist

- [ ] New `.go` file in `internal/web/` with request + response structs + registration function
- [ ] Request struct has `json`, `description`, `required` tags
- [ ] Response struct has `json` tags
- [ ] Info hash fields validated with `sha1.Size*2` (40 chars) + `hex.DecodeString`
- [ ] Errors wrapped with `errgo.Wrap` + `CodeError` for application error codes
- [ ] `u.SetName("scope.method_name")` with a unique, descriptive name
- [ ] Registration function called in `web.go:New()`
- [ ] No manual route registration needed — `POST /json_rpc` handles it automatically
- [ ] OpenAPI spec is auto-generated from struct tags — no manual spec writing needed
