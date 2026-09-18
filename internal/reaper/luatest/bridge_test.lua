-- Stub REAPER's API so the bridge's pure logic can be exercised outside REAPER.
--
-- The track half of the stub is a small project model rather than empty
-- returns, because setup reconciles a project it did not build: it creates,
-- repairs, and now removes tracks, and removing one is worth testing before it
-- runs against someone's session.
local deferred = nil
local project = {}

local TAG_ROLE = "P_EXT:clistudio.role"

local function mk_track()
  return {
    name = "", ext = {}, sends = {}, hw = {}, items = 0,
    vals = { B_MAINSEND = 1, I_RECINPUT = 0, I_RECMON = 0, I_RECARM = 0 },
  }
end

local function send_list(tr, cat)
  if cat == 0 then return tr.sends end
  return tr.hw
end

reaper = {
  defer = function(f) deferred = f end,           -- do not actually recurse
  SetExtState = function() end,
  GetExtState = function() return "" end,
  GetAppVersion = function() return "7.80/OSX64" end,

  PreventUIRefresh = function() end,
  Undo_BeginBlock = function() end,
  Undo_EndBlock = function() end,
  TrackList_AdjustWindows = function() end,
  UpdateArrange = function() end,

  CountTracks = function() return #project end,
  GetTrack = function(_, i) return project[i + 1] end,
  InsertTrackAtIndex = function(idx) table.insert(project, idx + 1, mk_track()) end,
  DeleteTrack = function(tr)
    for i, t in ipairs(project) do
      if t == tr then table.remove(project, i); return end
    end
  end,
  CountTrackMediaItems = function(tr) return tr.items end,

  GetSetMediaTrackInfo_String = function(tr, key, val, set)
    if set then
      if key == "P_NAME" then tr.name = val else tr.ext[key] = val end
      return true, val
    end
    if key == "P_NAME" then return true, tr.name end
    local v = tr.ext[key]
    if v == nil or v == "" then return false, "" end
    return true, v
  end,
  GetMediaTrackInfo_Value = function(tr, key) return tr.vals[key] or 0 end,
  SetMediaTrackInfo_Value = function(tr, key, v) tr.vals[key] = v end,

  GetTrackNumSends = function(tr, cat) return #send_list(tr, cat) end,
  CreateTrackSend = function(src, dest)
    if dest == nil then
      table.insert(src.hw, { I_DSTCHAN = 0, I_SRCCHAN = 0 })
      return #src.hw - 1
    end
    table.insert(src.sends, { dest = dest, I_SENDMODE = 0 })
    return #src.sends - 1
  end,
  RemoveTrackSend = function(tr, cat, idx) table.remove(send_list(tr, cat), idx + 1) end,
  GetTrackSendInfo_Value = function(tr, cat, idx, key)
    local s = send_list(tr, cat)[idx + 1]
    if s == nil then return 0 end
    if key == "P_DESTTRACK" then return s.dest end
    return s[key] or 0
  end,
  SetTrackSendInfo_Value = function(tr, cat, idx, key, v)
    local s = send_list(tr, cat)[idx + 1]
    if s ~= nil then s[key] = v end
  end,
}

local chunk = assert(loadfile("../bridge.lua"))
chunk()

-- Reach the locals through a fresh load so we can test them directly.
local src = io.open("../bridge.lua"):read("a")
local probe = load(src .. [[
return { split = split, db_to_scalar = db_to_scalar,
         scalar_to_db = scalar_to_db, handle = handle, SEP = SEP, ops = ops }
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

-- ===== setup reconciles a project =====

local BUSES = "main:MAIN:0:0,cue1:CUE 1:2:1"

local function setup(instruments)
  return M.split(M.ops.setup({ instruments, BUSES }), M.SEP)
end

local function role_of(tr) return tr.ext[TAG_ROLE] end

local function find_role(role)
  for _, tr in ipairs(project) do
    if role_of(tr) == role then return tr end
  end
  return nil
end

local function count_kind(actions, kind)
  local n = 0
  for _, a in ipairs(actions) do
    if a:sub(1, #kind + 1) == kind .. "|" then n = n + 1 end
  end
  return n
end

project = {}
local acts = setup("guitar:Guitar:1,bass:Bass:2")
check("setup builds every track", #project == 4, #project)
check("setup reports what it created", count_kind(acts, "created") == 4, table.concat(acts, " "))
check("the guitar is armed", find_role("guitar").vals.I_RECARM == 1, nil)
check("the guitar reaches both buses", #find_role("guitar").sends == 2, #find_role("guitar").sends)

-- Running it again must be a no-op, or every launch would churn the project.
acts = setup("guitar:Guitar:1,bass:Bass:2")
check("setup is idempotent", #project == 4, #project)
check("nothing changes on a second run", count_kind(acts, "unchanged") == 4, table.concat(acts, " "))

-- The bass player left and a second guitarist took input 2. The bass track has
-- to go: left behind it is still armed on input 2 and still feeding every cue,
-- so that input would arrive twice.
acts = setup("guitar:Guitar:1,guitar2:Guitar 2:2")
check("the replaced track is gone", find_role("bass") == nil, nil)
check("the new instrument is there", find_role("guitar2") ~= nil, nil)
check("the project did not grow", #project == 4, #project)
check("the removal is reported", count_kind(acts, "removed") == 1, table.concat(acts, " "))

-- A track holding a take must never be deleted for a rig change. It is unwired
-- instead, which is the part that actually matters.
project = {}
setup("guitar:Guitar:1,bass:Bass:2")
local bass = find_role("bass")
bass.items = 1
acts = setup("guitar:Guitar:1")
check("a track with a take is kept", #project == 4, #project)
check("the retirement is reported", count_kind(acts, "retired") == 1, table.concat(acts, " "))
check("it is disarmed", bass.vals.I_RECARM == 0, bass.vals.I_RECARM)
check("it has no input", bass.vals.I_RECINPUT == -1, bass.vals.I_RECINPUT)
check("it feeds nothing", #bass.sends == 0 and #bass.hw == 0, #bass.sends)
check("it is no longer managed", role_of(bass) == nil or role_of(bass) == "", role_of(bass))

-- Buses are managed too, and deleting the cues on every run would be the
-- worst possible reading of "no longer in the topology".
check("the buses survive", find_role("main") ~= nil and find_role("cue1") ~= nil, nil)

-- A monitor bus knocked off centre silences one speaker, and no amount of
-- staring at the routing shows it. Setup puts a bus back in the middle.
project = {}
setup("guitar:Guitar:1")
find_role("main").vals.D_PAN = 1        -- hard right, as a stray drag leaves it
find_role("cue1").vals.D_PAN = -1
acts = setup("guitar:Guitar:1")
check("the monitor bus is recentred", find_role("main").vals.D_PAN == 0, find_role("main").vals.D_PAN)
check("the cue bus is recentred", find_role("cue1").vals.D_PAN == 0, find_role("cue1").vals.D_PAN)
check("recentring is reported", count_kind(acts, "repaired") == 2, table.concat(acts, " "))

-- An instrument's pan is a choice, not damage: leave it exactly alone.
find_role("guitar").vals.D_PAN = -0.5
setup("guitar:Guitar:1")
check("an instrument keeps its pan", find_role("guitar").vals.D_PAN == -0.5, find_role("guitar").vals.D_PAN)

print(fails == 0 and "\nALL LUA CHECKS PASSED" or ("\n" .. fails .. " LUA CHECKS FAILED"))
os.exit(fails == 0 and 0 or 1)
