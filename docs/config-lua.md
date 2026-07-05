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
- 否则 → 使用 `{session}/config.toml`

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
| `application.max-http-parallel` | number | 最大 HTTP 并发连接数 | `100` |
| `application.num-want` | number | 每次向 peer 请求的 piece 数 | `0` (auto) |
| `application.global-connections-limit` | number | 全局连接数上限 | `50` |
| `application.global-upload-slots` | number | 全局上传 slot 上限 | `0` (auto: `connections-limit*4`) |
| `application.global-download-speed-limit` | number | 全局下载限速 (bytes/sec)，`0` 不限制 | `0` |
| `application.global-upload-speed-limit` | number | 全局上传限速 (bytes/sec)，`0` 不限制 | `0` |
| `application.fallocate` | boolean | 是否预分配磁盘空间 | `false` |

Key 使用 kebab-case，与 TOML 完全一致。

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
- **未知 key**：`neptune.set("typoKey", 123)` → 启动失败，列出所有合法 key
- **类型错误**：`neptune.set("application.p2p-port", "abc")` → 启动失败
- 未通过 validator 校验的配置值也会导致启动失败

## 注意事项

- `config.toml` 和 `config.lua` 二选一，Lua 脚本存在时 TOML 被忽略
- 脚本启动时执行一次，不支持热重载
- 不要写死循环（Lua 默认不带超时中断）
- `os.getenv()` 返回空字符串表示环境变量不存在
- `application.download-dir` 未在脚本中设置时默认为 `~/downloads`
- 配置可信，没有沙箱限制
