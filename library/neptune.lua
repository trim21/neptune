---@meta
--- Neptune config script API type stubs.
--- For editor autocompletion, add this file's directory to lua-language-server's
--- workspace.library path (see .luarc.json).

---@class Neptune
--- Each key takes its own Lua type or a string parsed as the target type.
---@field set fun(key: string, value: string|number|boolean)
---@field get fun(key: string): string|number|boolean
neptune = {}

---@class NeptuneConsole
---@field log fun(...: any)
---@field warn fun(...: any)
---@field error fun(...: any)
console = {}

--- Extensions to the standard os library.

--- Returns the value of the environment variable `name`, or empty string if not set.
---@param name string
---@return string
function os.getenv(name) end

--- Returns the hostname of the current machine.
---@return string
function os.hostname() end

--- Returns the number of logical CPU cores.
---@return integer
function os.cpus() end
