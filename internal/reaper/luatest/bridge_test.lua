-- Stub REAPER's API so the bridge's pure logic can be exercised outside REAPER.
local deferred = nil
reaper = {
  defer = function(f) deferred = f end,           -- do not actually recurse
  SetExtState = function() end,
  GetExtState = function() return "" end,
  GetAppVersion = function() return "7.80/OSX64" end,
  CountTracks = function() return 0 end,
}

local chunk = assert(loadfile("../bridge.lua"))
chunk()

-- Reach the locals through a fresh load so we can test them directly.
local src = io.open("../bridge.lua"):read("a")
local probe = load(src .. [[
return { split = split, db_to_scalar = db_to_scalar,
         scalar_to_db = scalar_to_db, handle = handle, SEP = SEP }
]])
local M = probe()

local fails = 0
local function check(name, cond, got)
  if cond then print("  ok   " .. name)
  else print("  FAIL " .. name .. "  got=" .. tostring(got)); fails = fails + 1 end
end

-- split must survive empty fields, which appear whenever an argument is blank.
local p = M.split("7" .. M.SEP .. "setsend" .. M.SEP .. "cue1" .. M.SEP .. "guitar" .. M.SEP .. "3", M.SEP)
check("split field count", #p == 5, #p)
check("split values", p[1]=="7" and p[2]=="setsend" and p[5]=="3", table.concat(p, ","))

local e = M.split("a::b", ":")
check("split keeps empty middle field", #e == 3 and e[2] == "", #e)

-- Unity must be exactly 1.0 or every level is silently wrong.
check("0 dB is unity", math.abs(M.db_to_scalar(0) - 1.0) < 1e-9, M.db_to_scalar(0))
check("-6 dB ~= 0.501", math.abs(M.db_to_scalar(-6) - 0.5012) < 1e-3, M.db_to_scalar(-6))
check("+6 dB ~= 1.995", math.abs(M.db_to_scalar(6) - 1.9953) < 1e-3, M.db_to_scalar(6))
check("-60 dB is silence", M.db_to_scalar(-60) == 0, M.db_to_scalar(-60))

for _, db in ipairs({0, 3, -2, 6, -12, -6.5}) do
  local rt = M.scalar_to_db(M.db_to_scalar(db))
  check("round trip " .. db .. " dB", math.abs(rt - db) < 1e-6, rt)
end
check("silence round trip", M.scalar_to_db(0) == -60, M.scalar_to_db(0))

-- handle() must answer with the sequence id and never throw.
local resp = M.handle("42" .. M.SEP .. "ping")
local rp = M.split(resp, M.SEP)
check("ping seq echoed", rp[1] == "42", rp[1])
check("ping ok", rp[2] == "ok", rp[2])

local bad = M.split(M.handle("9" .. M.SEP .. "nosuchop"), M.SEP)
check("unknown op -> err", bad[2] == "err", bad[2])
check("unknown op names it", bad[3]:find("nosuchop") ~= nil, bad[3])

-- An operation that throws must become an error response, not crash the loop.
local boom = M.split(M.handle("3" .. M.SEP .. "setmonvol" .. M.SEP .. "0"), M.SEP)
check("failing op -> err", boom[2] == "err", boom[2])

print(fails == 0 and "\nALL LUA CHECKS PASSED" or ("\n" .. fails .. " LUA CHECKS FAILED"))
os.exit(fails == 0 and 0 or 1)
