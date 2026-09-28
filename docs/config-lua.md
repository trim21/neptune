# Config Script (config.lua)

Neptune 支持可选的 Lua 配置脚本，与 TOML 配置二选一。

## 加载机制

`config.toml` 和 `config.lua` **二选一**，通过 `--config` 指定（或自动检测）：

```bash
# 使用 TOML
neptune --config config.toml

# 使用 Lua
neptune --config config.lua
```

不指定 `--config` 时的自动检测逻辑：

- 如果 `{session}/config.lua` 存在 → 使用 Lua
- 否则如果 `{session}/config.toml` 存在 → 使用 TOML
- 两个都不存在 → 全部使用内置默认值

显式传入 `--config` 时文件必须存在，路径不存在或扩展名不是 `.toml` / `.lua` 都会导致启动失败。

配置文件里**没有**的键一律使用内置默认值；`--p2p-port` / `NEPTUNE_P2P_PORT` 这类命令行与环境的取值**只在显式提供时**覆盖配置文件。

## 编辑器支持

项目仓库的 [`library/neptune.lua`](../library/neptune.lua) 提供了 [LuaCATS](https://luals.github.io/wiki/annotations/) 类型声明。

在项目根目录有 `.luarc.json` 配置，用 VS Code + [lua-language-server](https://github.com/LuaLS/lua-language-server) 打开仓库即可获得自动补全和类型检查。

对于自己的 `~/.neptune/config.lua`，在该目录创建 `.luarc.json`：

```json
{
  "runtime.version": "Lua 5.1",
  "workspace.library": ["/path/to/neptune/library"]
}
```

## 全局 API

### neptune.set(key, value)

设置配置项。重复调用后面的值覆盖前面的值。`key` 必须在白名单内，否则报错。

### neptune.get(key)

读取当前配置值（默认值或被 `neptune.set()` 覆盖后的值）。

### os.getenv(name)

读取环境变量，等价于 `os.Getenv`。变量不存在时返回空字符串 `""`。

### os.hostname()

返回主机名。

### os.cpus()

返回逻辑 CPU 核心数。

### console.log(...) / console.warn(...) / console.error(...)

输出日志到 stderr，格式: `[config] <message>`。

### 标准 Lua 库

`math`、`string`、`table`、`os.date()`、`os.time()` 等标准库可用。

## 配置键列表

| key | 类型 | 说明 | 默认值 |
|---|---|---|---|
| `application.download-dir` | string | 下载目录 | `~/downloads` |
| `application.p2p-port` | number | P2P 监听端口 | `50047` |
| `application.max-http-parallel` | number | 最大 HTTP 并发连接数（tracker 请求） | `100` |
| `application.global-connections-limit` | number | 全局连接数上限 | `200` |
| `application.torrent-connection-limit` | number | 单个 torrent 的连接数上限 | `50` |
| `application.connection-speed` | number | 出站连接建立速率上限（个/秒），`0` 不限制 | `30` |
| `application.num-want` | number | 每次向 peer 请求的 piece 数，`0` 为自动 | `0` (auto) |
| `application.download-slots` | number | 同时下载的 torrent 数上限，`0` 不限制 | `0` |
| `application.global-upload-slots` | number | 全局上传 slot 上限 | `0` (auto: `global-connections-limit*4`，至少 `64`) |
| `application.global-download-speed-limit` | number | 全局下载限速 (bytes/sec)，`0` 不限制 | `0` |
| `application.global-upload-speed-limit` | number | 全局上传限速 (bytes/sec)，`0` 不限制 | `0` |
| `application.slow-download-speed-threshold` | number | 慢速下载判定阈值 (bytes/sec)，低于该值的下载会被让位 | `0` |
| `application.max-rpc-request-body-size` | number | JSON-RPC 请求体大小上限 (bytes) | `52428800` |
| `application.fallocate` | boolean | 是否预分配磁盘空间 | `false` |
| `application.recheck-on-complete` | boolean | 下载完成后自动重新校验 | `false` |
| `application.piece-pick-strategy` | string | piece 选择策略：`rarest-first` / `sequential` | `rarest-first` |
| `application.crypto` | string | MSE 加密策略：`prefer` / `force` / `prefer-no-encryption` / `none` | `prefer` |
| `application.hook.on-download-started` | string | 下载开始时的 hook 命令 | `""` |
| `application.hook.on-download-completed` | string | 下载完成时的 hook 命令 | `""` |
| `application.hook.timeout` | string | hook 超时，Go duration 格式（如 `"30s"`） | `"0s"` |

Key 使用 kebab-case，与 TOML 完全一致 —— Lua 能设置的键就是 TOML 能设置的键，两者不会有一方多出或少掉某个键。

取值规则：

- **boolean 字段按 Lua 真值判断**：只有 `false` 和 `nil` 是假，其余一切值（包括 `0` 和 `""`）都会被当成 `true`。想关掉一个 boolean 要写 `false`。
- **number 字段**接受 number，也接受数字字符串；`uint16` 类型的键超出 `0..65535` 会报错。
- 可选字符串键（`application.crypto`、`application.piece-pick-strategy`）留空表示使用默认值。


## 示例

### 基础：根据主机名切换配置

```lua
local node = os.getenv("NODE_NAME") or ""

if node == "seedbox" then
    neptune.set("application.download-dir", "/mnt/big/downloads")
    neptune.set("application.global-connections-limit", 500)
    neptune.set("application.global-upload-slots", 200)
    neptune.set("application.global-upload-speed-limit", 0)  -- 不限速做种
end
```

### 时间段限速

```lua
local hour = tonumber(os.date("%H"))

if hour >= 1 and hour < 8 then
    -- 夜间不限速
    neptune.set("application.global-upload-speed-limit", 0)
    neptune.set("application.global-download-speed-limit", 0)
else
    -- 白天限速
    neptune.set("application.global-upload-speed-limit", 30 * 1024 * 1024)   -- 30 MB/s
    neptune.set("application.global-download-speed-limit", 100 * 1024 * 1024) -- 100 MB/s
end
```

### 根据 CPU 核心数调整并发

```lua
local conns = neptune.get("application.global-connections-limit")
neptune.set("application.global-connections-limit", math.max(conns, os.cpus() * 20))

local http = neptune.get("application.max-http-parallel")
neptune.set("application.max-http-parallel", math.max(http, os.cpus() * 50))
```

### 多条件组合

```lua
local node = os.getenv("NODE_NAME") or ""
local hour = tonumber(os.date("%H"))

-- 默认上传限速 200 MB/s
neptune.set("application.global-upload-speed-limit", 200 * 1024 * 1024)

if node == "n5" then
    neptune.set("application.global-upload-speed-limit", 10 * 1024 * 1024)
    neptune.set("application.global-download-speed-limit", 15 * 1024 * 1024)
    neptune.set("application.fallocate", false)
elseif node == "n5-slow" then
    neptune.set("application.global-download-speed-limit", 5 * 1024 * 1024)
    neptune.set("application.global-upload-speed-limit", 0)
end

-- 无论什么节点，深夜都不限速
if hour >= 2 and hour < 6 then
    neptune.set("application.global-upload-speed-limit", 0)
    neptune.set("application.global-download-speed-limit", 0)
end

console.log("node=" .. node .. " host=" .. os.hostname() .. " cpus=" .. os.cpus())
```

## 错误处理

- **语法错误**：启动失败，打印 Lua 错误信息
- **未知 key**：`neptune.set("typoKey", 123)` → 启动失败，按字典序列出所有合法 key
- **类型错误**：`neptune.set("application.p2p-port", "abc")` → 启动失败（值装不进字段类型，报错带脚本行号）
- **取值非法**：`crypto = "bogus"`、`piece-pick-strategy = "typo"` → 启动失败。这类校验在所有来源（配置文件、命令行、环境变量）合并之后统一执行一次，因此 TOML 和 Lua 接受的值集合完全相同

## 注意事项

- `config.toml` 和 `config.lua` 二选一；自动检测时 `config.lua` 优先，两个都不存在则全部使用默认值
- 脚本启动时执行一次，不支持热重载
- 不要写死循环（Lua 默认不带超时中断）
- `os.getenv()` 返回空字符串表示环境变量不存在
- `application.download-dir` 未在脚本中设置时默认为 `~/downloads`
- 配置可信，没有沙箱限制
