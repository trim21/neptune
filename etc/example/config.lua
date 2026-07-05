-- Neptune configuration script.
-- If this file exists at {session}/config.lua, it replaces config.toml entirely.
--
-- For editor autocompletion, place the library/neptune.lua stub in your
-- lua-language-server workspace.library path (see .luarc.json).
--
--@type neptune Neptune
--@type console NeptuneConsole

local node = os.getenv("NODE_NAME") or ""

-- Default: 200 MB/s upload, no download limit
neptune.set("application.global-upload-speed-limit", 200 * 1024 * 1024)

if node == "n5" then
    neptune.set("application.global-upload-speed-limit", 10 * 1024 * 1024)
    neptune.set("application.global-download-speed-limit", 15 * 1024 * 1024)
    neptune.set("application.fallocate", false)

elseif node == "n5-slow" then
    neptune.set("application.global-download-speed-limit", 5 * 1024 * 1024)
    neptune.set("application.global-upload-speed-limit", 0)

elseif node == "seedbox" then
    neptune.set("application.global-upload-speed-limit", 0)
    neptune.set("application.global-connections-limit", 500)
    neptune.set("application.global-upload-slots", 200)
    neptune.set("application.download-dir", "/mnt/big/downloads")

else
    -- Adjust based on CPU cores (reads default value first, then bumps if needed)
    local conns = neptune.get("application.global-connections-limit")
    neptune.set("application.global-connections-limit", math.max(conns, os.cpus() * 20))
    neptune.set("application.max-http-parallel", math.max(neptune.get("application.max-http-parallel"), os.cpus() * 50))
end

print("node=" .. node .. " host=" .. os.hostname())
