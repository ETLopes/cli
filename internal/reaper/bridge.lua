-- cli-studio bridge
--
-- Installed and run by `cli studio`. REAPER's web interface can read and write
-- extended state but cannot create tracks or assign hardware I/O; ReaScript can
-- do all of that but is unreachable from outside REAPER. This script closes the
-- gap: it runs resident inside REAPER, watches extended state for a request,
-- performs it through the ReaScript API, and writes the result back.
--
-- Protocol (deliberately line-based; REAPER's Lua has no JSON parser):
--   request   seq \31 op \31 arg \31 arg ...
--   response  seq \31 ok  \31 payload
--             seq \31 err \31 message
--
-- Managed objects are tagged with P_EXT:clistudio.role so they can be found
-- again after the user renames or reorders tracks, and so unmanaged tracks are
-- never touched.

local SECTION = "clistudio"
local SEP = "\31" -- unit separator: cannot occur in a track name
local TAG_ROLE = "P_EXT:clistudio.role"
local TAG_MANAGED = "P_EXT:clistudio.managed"

local POLL_SECONDS = 0.05

-- Send modes. Pre-fader-post-FX is the one that matters here: cue mixes must
-- carry the processed tone (the guitar's amp sim) while staying independent of
-- the main fader, so moving the control-room mix never alters what a musician
-- hears in their headphones.
local SENDMODE_POST_FADER = 0
local SENDMODE_PRE_FX = 1
local SENDMODE_PRE_FADER = 3

local function db_to_scalar(db)
  if db <= -60 then return 0 end
  return 10 ^ (db / 20)
end

local function scalar_to_db(v)
  if v <= 0 then return -60 end
  return 20 * math.log(v, 10)
end

-- split breaks a string on a single-character separator, preserving empty
-- fields: a blank argument is meaningful, and losing it would silently shift
-- every later field by one. The separator goes in a character class rather
-- than straight into the pattern so it is never read as pattern syntax.
local function split(s, sep)
  local out = {}
  for part in string.gmatch(s .. sep, "([^" .. sep .. "]*)" .. sep) do
    out[#out + 1] = part
  end
  return out
end

-- track_role reads the managed role tag, or nil for unmanaged tracks.
local function track_role(tr)
  local ok, val = reaper.GetSetMediaTrackInfo_String(tr, TAG_ROLE, "", false)
  if ok and val ~= "" then return val end
  return nil
end

-- find_managed returns the track carrying the given role, or nil.
local function find_managed(role)
  for i = 0, reaper.CountTracks(0) - 1 do
    local tr = reaper.GetTrack(0, i)
    if track_role(tr) == role then return tr end
  end
  return nil
end

-- ensure_track finds a managed track by role, creating it if absent. It
-- returns the track and whether it had to be created, so setup can report
-- honestly instead of claiming work it did not do.
local function ensure_track(role, name)
  local tr = find_managed(role)
  if tr then return tr, false end

  local idx = reaper.CountTracks(0)
  reaper.InsertTrackAtIndex(idx, true)
  tr = reaper.GetTrack(0, idx)
  reaper.GetSetMediaTrackInfo_String(tr, "P_NAME", name, true)
  reaper.GetSetMediaTrackInfo_String(tr, TAG_ROLE, role, true)
  reaper.GetSetMediaTrackInfo_String(tr, TAG_MANAGED, "true", true)
  return tr, true
end

-- find_send returns the index of the send from src to dest, or -1.
local function find_send(src, dest)
  for i = 0, reaper.GetTrackNumSends(src, 0) - 1 do
    local target = reaper.GetTrackSendInfo_Value(src, 0, i, "P_DESTTRACK")
    if target == dest then return i end
  end
  return -1
end

-- ensure_send creates a send from src to dest if missing and forces its mode.
local function ensure_send(src, dest, mode)
  local idx = find_send(src, dest)
  local created = false
  if idx < 0 then
    idx = reaper.CreateTrackSend(src, dest)
    created = true
  end
  local repaired = false
  if reaper.GetTrackSendInfo_Value(src, 0, idx, "I_SENDMODE") ~= mode then
    reaper.SetTrackSendInfo_Value(src, 0, idx, "I_SENDMODE", mode)
    repaired = not created
  end
  return idx, created, repaired
end

-- find_hw_send returns the index of the hardware output send on a track.
local function find_hw_send(tr)
  if reaper.GetTrackNumSends(tr, 1) > 0 then return 0 end
  return -1
end

-- ensure_hw_out routes a track to a stereo hardware output pair. chan is the
-- zero-based index of the left channel.
local function ensure_hw_out(tr, chan)
  local idx = find_hw_send(tr)
  local created = false
  if idx < 0 then
    idx = reaper.CreateTrackSend(tr, nil)
    created = true
  end
  local repaired = false
  if reaper.GetTrackSendInfo_Value(tr, 1, idx, "I_DSTCHAN") ~= chan then
    reaper.SetTrackSendInfo_Value(tr, 1, idx, "I_DSTCHAN", chan)
    repaired = not created
  end
  -- Buses feed the interface directly. Leaving the master send on as well
  -- would sum every bus into outputs 1/2 a second time.
  if reaper.GetMediaTrackInfo_Value(tr, "B_MAINSEND") ~= 0 then
    reaper.SetMediaTrackInfo_Value(tr, "B_MAINSEND", 0)
    repaired = not created
  end
  return created, repaired
end

-- ensure_input assigns a mono hardware input to a track and arms it.
local function ensure_input(tr, channel)
  -- I_RECINPUT: a mono hardware input is simply its zero-based channel.
  local want = channel - 1
  local changed = false
  if reaper.GetMediaTrackInfo_Value(tr, "I_RECINPUT") ~= want then
    reaper.SetMediaTrackInfo_Value(tr, "I_RECINPUT", want)
    changed = true
  end
  if reaper.GetMediaTrackInfo_Value(tr, "I_RECMON") ~= 1 then
    reaper.SetMediaTrackInfo_Value(tr, "I_RECMON", 1)
    changed = true
  end
  return changed
end

-- fx_index finds a named effect on a track, or -1.
local function fx_index(tr, name)
  for i = 0, reaper.TrackFX_GetCount(tr) - 1 do
    local _, fxname = reaper.TrackFX_GetFXName(tr, i, "")
    if fxname:find(name, 1, true) then return i end
  end
  return -1
end

-- ===== operations =====

local ops = {}

function ops.ping()
  return "REAPER " .. tostring(reaper.GetAppVersion())
end

-- setup builds the studio topology. Every step checks before it acts, so
-- running it twice creates nothing and repairs only what drifted.
function ops.setup(args)
  local actions = {}
  local function record(kind, object, detail)
    actions[#actions + 1] = kind .. "|" .. object .. "|" .. (detail or "")
  end

  -- args: instrument specs then bus specs, each as role,name,param
  local instruments = split(args[1] or "", ",")
  local buses = split(args[2] or "", ",")

  reaper.PreventUIRefresh(1)
  reaper.Undo_BeginBlock()

  -- Buses first: instrument sends need somewhere to land.
  local bus_tracks = {}
  for _, spec in ipairs(buses) do
    local f = split(spec, ":")
    local role, name, chan = f[1], f[2], tonumber(f[3])
    local tr, created = ensure_track(role, name)
    bus_tracks[role] = tr
    local hw_created, hw_repaired = ensure_hw_out(tr, chan)
    if created then
      record("created", "bus " .. name, "outputs " .. (chan + 1) .. "/" .. (chan + 2))
    elseif hw_created or hw_repaired then
      record("repaired", "bus " .. name, "outputs " .. (chan + 1) .. "/" .. (chan + 2))
    else
      record("unchanged", "bus " .. name, "")
    end
  end

  for _, spec in ipairs(instruments) do
    local f = split(spec, ":")
    local role, name, input = f[1], f[2], tonumber(f[3])
    local tr, created = ensure_track(role, name)
    local input_changed = ensure_input(tr, input)

    -- The instrument reaches the monitors through MAIN, not the master bus,
    -- so the control-room mix stays one explicit path.
    if reaper.GetMediaTrackInfo_Value(tr, "B_MAINSEND") ~= 0 then
      reaper.SetMediaTrackInfo_Value(tr, "B_MAINSEND", 0)
      input_changed = true
    end

    local touched = created or input_changed
    if bus_tracks["main"] then
      local _, c, r = ensure_send(tr, bus_tracks["main"], SENDMODE_POST_FADER)
      touched = touched or c or r
    end
    for _, spec2 in ipairs(buses) do
      local bf = split(spec2, ":")
      if bf[1] ~= "main" and bus_tracks[bf[1]] then
        local _, c, r = ensure_send(tr, bus_tracks[bf[1]], SENDMODE_PRE_FADER)
        touched = touched or c or r
      end
    end

    if created then
      record("created", "track " .. name, "input " .. input .. ", mono")
    elseif touched then
      record("repaired", "track " .. name, "input " .. input .. ", mono")
    else
      record("unchanged", "track " .. name, "")
    end
  end

  reaper.Undo_EndBlock("cli studio setup", -1)
  reaper.PreventUIRefresh(-1)
  reaper.TrackList_AdjustWindows(false)
  reaper.UpdateArrange()

  return table.concat(actions, SEP)
end

function ops.setsend(args)
  local cue_role, inst_role, db = args[1], args[2], tonumber(args[3])
  local src = find_managed(inst_role)
  local dest = find_managed(cue_role)
  if not src then error("no managed track for " .. inst_role) end
  if not dest then error("no managed bus for " .. cue_role) end

  local idx = find_send(src, dest)
  if idx < 0 then error("no send from " .. inst_role .. " to " .. cue_role) end
  reaper.SetTrackSendInfo_Value(src, 0, idx, "D_VOL", db_to_scalar(db))
  return "ok"
end

function ops.setfx(args)
  local inst_role, plugin, enabled = args[1], args[2], args[3] == "1"
  local tr = find_managed(inst_role)
  if not tr then error("no managed track for " .. inst_role) end

  local idx = fx_index(tr, plugin)
  if idx < 0 then
    if not enabled then return "absent" end
    idx = reaper.TrackFX_AddByName(tr, plugin, false, 1)
    if idx < 0 then
      error("plugin not found: " .. plugin)
    end
  end
  reaper.TrackFX_SetEnabled(tr, idx, enabled)
  return "ok"
end

function ops.setmonvol(args)
  local tr = find_managed("main")
  if not tr then error("no MAIN bus") end
  reaper.SetMediaTrackInfo_Value(tr, "D_VOL", db_to_scalar(tonumber(args[1])))
  return "ok"
end

function ops.setmonmute(args)
  local tr = find_managed("main")
  if not tr then error("no MAIN bus") end
  reaper.SetMediaTrackInfo_Value(tr, "B_MUTE", args[1] == "1" and 1 or 0)
  return "ok"
end

-- snapshot reports what REAPER currently has, so the caller can compare it
-- against the session rather than assume the two agree.
function ops.snapshot(args)
  local out = {}
  local cues = split(args[1] or "", ",")
  local insts = split(args[2] or "", ",")

  for _, cue_role in ipairs(cues) do
    local dest = find_managed(cue_role)
    if dest then
      for _, inst_role in ipairs(insts) do
        local src = find_managed(inst_role)
        if src then
          local idx = find_send(src, dest)
          if idx >= 0 then
            local v = reaper.GetTrackSendInfo_Value(src, 0, idx, "D_VOL")
            out[#out + 1] = "send|" .. cue_role .. "|" .. inst_role ..
              "|" .. string.format("%.2f", scalar_to_db(v))
          end
        end
      end
    end
  end

  local main = find_managed("main")
  if main then
    out[#out + 1] = "monvol|" ..
      string.format("%.2f", scalar_to_db(reaper.GetMediaTrackInfo_Value(main, "D_VOL")))
    out[#out + 1] = "monmute|" ..
      (reaper.GetMediaTrackInfo_Value(main, "B_MUTE") > 0 and "1" or "0")
  end

  for _, inst_role in ipairs(insts) do
    local tr = find_managed(inst_role)
    if tr then
      for i = 0, reaper.TrackFX_GetCount(tr) - 1 do
        local _, fxname = reaper.TrackFX_GetFXName(tr, i, "")
        local on = reaper.TrackFX_GetEnabled(tr, i) and "1" or "0"
        out[#out + 1] = "fx|" .. inst_role .. "|" .. fxname .. "|" .. on
      end
    end
  end
  return table.concat(out, SEP)
end

function ops.save()
  reaper.Main_OnCommand(40026, 0) -- File: Save project
  return "ok"
end

-- ===== poll loop =====

local last_seq = ""

local function handle(raw)
  local fields = split(raw, SEP)
  local seq, op = fields[1], fields[2]
  local args = {}
  for i = 3, #fields do args[#args + 1] = fields[i] end

  local fn = ops[op]
  if not fn then
    return seq .. SEP .. "err" .. SEP .. "unknown operation: " .. tostring(op)
  end
  local ok, result = pcall(fn, args)
  if not ok then
    return seq .. SEP .. "err" .. SEP .. tostring(result)
  end
  return seq .. SEP .. "ok" .. SEP .. tostring(result)
end

local function poll()
  local raw = reaper.GetExtState(SECTION, "req")
  if raw ~= "" and raw ~= last_seq then
    last_seq = raw
    local response = handle(raw)
    reaper.SetExtState(SECTION, "resp", response, false)
  end
  reaper.defer(poll)
end

reaper.SetExtState(SECTION, "bridge", "ready", false)
poll()
