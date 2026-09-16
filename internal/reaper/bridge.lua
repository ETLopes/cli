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

-- ensure_input assigns a mono hardware input to a track, enables input
-- monitoring, and arms it for record.
--
-- Arming is not optional here. REAPER only passes input through a track that
-- is armed, so without it nothing reaches the cue mixes, the monitors, or a
-- tuner: the whole studio is silent while appearing correctly wired.
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
  if reaper.GetMediaTrackInfo_Value(tr, "I_RECARM") ~= 1 then
    reaper.SetMediaTrackInfo_Value(tr, "I_RECARM", 1)
    changed = true
  end
  return changed
end

-- Each managed plugin instance is renamed to a stable tag, because a chain
-- can hold several instances of one plugin -- a vocal has a tuner, a pitch
-- corrector and a hard-tuned corrector, all of which are ReaTune -- and
-- matching on the plugin name would find whichever came first every time.
local function fx_tag(effect_id) return "cs:" .. effect_id end

-- fx_index finds a managed effect by its tag, falling back to the plugin name
-- so instances created before tagging are still found and can be adopted.
local function fx_index(tr, tag, plugin)
  for i = 0, reaper.TrackFX_GetCount(tr) - 1 do
    local _, fxname = reaper.TrackFX_GetFXName(tr, i, "")
    if tag ~= nil and tag ~= "" and fxname:find(tag, 1, true) then return i end
  end
  if plugin and plugin ~= "" then
    for i = 0, reaper.TrackFX_GetCount(tr) - 1 do
      local _, fxname = reaper.TrackFX_GetFXName(tr, i, "")
      if fxname:find(plugin, 1, true) then return i end
    end
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

-- TrackFX_Show modes.
local FX_HIDE_FLOATING = 2
local FX_SHOW_FLOATING = 3

-- adopt tags plugins that were added before tagging existed.
--
-- Instances created earlier carry only their plugin name, so a lookup by tag
-- misses them and a second copy gets added alongside. Matching each untagged
-- instance to the first chain entry that wants that plugin repairs them in
-- place, and each entry claims at most one so three ReaTune instances cannot
-- all become the tuner.
function ops.adopt(args)
  local role = args[1]
  local tr = find_managed(role)
  if not tr then error("no managed track for " .. tostring(role)) end

  local claimed = {}
  local adopted = {}

  -- Anything already tagged keeps its identity.
  for i = 0, reaper.TrackFX_GetCount(tr) - 1 do
    local _, name = reaper.TrackFX_GetFXName(tr, i, "")
    if name:find("cs:", 1, true) then
      claimed[name] = true
    end
  end

  for _, spec in ipairs(split(args[2] or "", ",")) do
    local sep = spec:find(":")
    if sep then
      local effect_id = spec:sub(1, sep - 1)
      local plugin = spec:sub(sep + 1)
      local tag = fx_tag(effect_id)
      if not claimed[tag] then
        for i = 0, reaper.TrackFX_GetCount(tr) - 1 do
          local _, name = reaper.TrackFX_GetFXName(tr, i, "")
          if not name:find("cs:", 1, true) and name:find(plugin, 1, true) then
            reaper.TrackFX_SetNamedConfigParm(tr, i, "renamed_name", tag)
            claimed[tag] = true
            adopted[#adopted + 1] = effect_id
            break
          end
        end
      end
    end
  end
  return table.concat(adopted, ",")
end

function ops.setfx(args)
  local inst_role, plugin, enabled = args[1], args[2], args[3] == "1"
  -- shows_ui marks an effect that exists to be looked at, such as a tuner.
  local shows_ui = args[4] == "1"
  local effect_id = args[6] or ""
  local tag = effect_id ~= "" and fx_tag(effect_id) or ""

  local tr = find_managed(inst_role)
  if not tr then error("no managed track for " .. inst_role) end

  local idx = fx_index(tr, tag, plugin)
  local created = false
  if idx < 0 then
    if not enabled then return "absent" end
    idx = reaper.TrackFX_AddByName(tr, plugin, false, 1)
    if idx < 0 then
      error("plugin not found: " .. plugin)
    end
    created = true
  end
  -- Tag the instance so it can be found again regardless of how many copies
  -- of the same plugin the chain holds.
  if tag ~= "" then
    reaper.TrackFX_SetNamedConfigParm(tr, idx, "renamed_name", tag)
  end
  reaper.TrackFX_SetEnabled(tr, idx, enabled)

  -- Starting values are written only on creation. Several stock plugins load
  -- in a state that does nothing audible, but re-applying defaults on every
  -- toggle would discard whatever the player had dialled in since.
  if created and args[5] and args[5] ~= "" then
    for _, pair in ipairs(split(args[5], ",")) do
      local name, value = pair:match("^(.-)=(.*)$")
      if name and value then
        for i = 0, reaper.TrackFX_GetNumParams(tr, idx) - 1 do
          local _, pname = reaper.TrackFX_GetParamName(tr, idx, i, "")
          if pname == name then
            reaper.TrackFX_SetParam(tr, idx, i, tonumber(value) or 0)
            break
          end
        end
      end
    end
  end

  -- A tuner that is loaded but not on screen reads to the player as nothing
  -- having happened, so its window follows the switch.
  if shows_ui then
    reaper.TrackFX_Show(tr, idx, enabled and FX_SHOW_FLOATING or FX_HIDE_FLOATING)
  end
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

-- fxparams lists a plugin's parameters, read-only. Plugin parameter indexes
-- are not documented anywhere a program can consult, so they have to be
-- discovered from the plugin itself.
function ops.fxparams(args)
  local tr = find_managed(args[1])
  if not tr then error("no managed track for " .. tostring(args[1])) end
  local idx = fx_index(tr, args[2], args[2])
  if idx < 0 then error("plugin not on track: " .. tostring(args[2])) end

  local out = {}
  for i = 0, reaper.TrackFX_GetNumParams(tr, idx) - 1 do
    local _, pname = reaper.TrackFX_GetParamName(tr, idx, i, "")
    local val, minv, maxv = reaper.TrackFX_GetParam(tr, idx, i)
    local _, fmt = reaper.TrackFX_GetFormattedParamValue(tr, idx, i, "")
    out[#out + 1] = table.concat({
      tostring(i), pname, string.format("%.4f", val),
      string.format("%.2f", minv), string.format("%.2f", maxv), fmt,
    }, "|")
  end
  return table.concat(out, SEP)
end

-- fxconfig probes named configuration values on a plugin. Some settings are
-- not VST parameters and are only reachable this way.
function ops.fxconfig(args)
  local tr = find_managed(args[1])
  if not tr then error("no managed track for " .. tostring(args[1])) end
  local idx = fx_index(tr, args[2], args[2])
  if idx < 0 then error("plugin not on track: " .. tostring(args[2])) end

  local out = {}
  for _, key in ipairs(split(args[3] or "", ",")) do
    local ok, val = reaper.TrackFX_GetNamedConfigParm(tr, idx, key)
    out[#out + 1] = key .. "|" .. tostring(ok) .. "|" .. tostring(val)
  end
  -- Also report the track's channel count, since a detector pointed at a
  -- channel the track does not have is silent by definition.
  out[#out + 1] = "track_nchan|true|" .. tostring(math.floor(reaper.GetMediaTrackInfo_Value(tr, "I_NCHAN")))
  local _, inpins = reaper.TrackFX_GetIOSize(tr, idx)
  out[#out + 1] = "fx_inpins|true|" .. tostring(inpins)
  return table.concat(out, SEP)
end

-- setfxconfig writes a named configuration value on a plugin.
function ops.setfxconfig(args)
  local tr = find_managed(args[1])
  if not tr then error("no managed track for " .. tostring(args[1])) end
  local idx = fx_index(tr, args[2], args[2])
  if idx < 0 then error("plugin not on track: " .. tostring(args[2])) end
  local ok = reaper.TrackFX_SetNamedConfigParm(tr, idx, args[3], args[4])
  local _, now = reaper.TrackFX_GetNamedConfigParm(tr, idx, args[3])
  return tostring(ok) .. "|" .. tostring(now)
end

-- trackchunk returns a managed track's full state, which includes REAPER's
-- own serialisation of each plugin alongside the plugin's private state.
function ops.trackchunk(args)
  local tr = find_managed(args[1])
  if not tr then error("no managed track for " .. tostring(args[1])) end
  local ok, chunk = reaper.GetTrackStateChunk(tr, "", false)
  if not ok then error("could not read the track state") end
  return chunk
end

function ops.delfx(args)
  local tr = find_managed(args[1])
  if not tr then error("no managed track for " .. tostring(args[1])) end
  local idx = fx_index(tr, args[2], args[2])
  if idx < 0 then return "absent" end
  reaper.TrackFX_Delete(tr, idx)
  return "deleted"
end

-- enumfx lists the plugins REAPER actually has installed, optionally filtered
-- by a substring. Plugin names cannot be guessed: they vary by platform and
-- install, and a wrong one fails only at the moment a player reaches for it.
function ops.enumfx(args)
  local needle = (args[1] or ""):lower()
  local out, i = {}, 0
  while true do
    local ok, name, ident = reaper.EnumInstalledFX(i)
    if not ok then break end
    if needle == "" or name:lower():find(needle, 1, true) then
      out[#out + 1] = name
    end
    i = i + 1
    if i > 5000 then break end
  end
  return table.concat(out, SEP)
end

-- setfxparam sets a plugin parameter by its reported name, so callers need
-- not know indexes that differ between plugins and versions.
function ops.setfxparam(args)
  local tr = find_managed(args[1])
  if not tr then error("no managed track for " .. tostring(args[1])) end
  local idx = fx_index(tr, args[2], args[2])
  if idx < 0 then error("plugin not on track: " .. tostring(args[2])) end

  for i = 0, reaper.TrackFX_GetNumParams(tr, idx) - 1 do
    local _, pname = reaper.TrackFX_GetParamName(tr, idx, i, "")
    if pname == args[3] then
      reaper.TrackFX_SetParam(tr, idx, i, tonumber(args[4]))
      local _, fmt = reaper.TrackFX_GetFormattedParamValue(tr, idx, i, "")
      return pname .. "=" .. tostring(fmt)
    end
  end
  error("no parameter named " .. tostring(args[3]))
end

-- ReaEQ stores gain as a linear scalar where 0.25 is unity, so unity sits a
-- quarter of the way up its range rather than in the middle. Established by
-- sweeping the parameter and reading back what the plugin reported.
local EQ_UNITY = 0.25

local function eq_gain(db)
  if db <= -60 then return 0 end
  return EQ_UNITY * (10 ^ (db / 20))
end

-- ensure_fx finds a tagged plugin, adding it if it is not there.
--
-- A mixing desk has an EQ and a compressor on every channel; they are not
-- things you switch on. Without this, moving a tone control on a channel whose
-- EQ had never been added updated the display and changed nothing, which is
-- the worst way for a control to fail.
local function ensure_fx(tr, tag, plugin)
  local idx = fx_index(tr, tag, plugin)
  if idx >= 0 then return idx end
  idx = reaper.TrackFX_AddByName(tr, plugin, false, 1)
  if idx < 0 then return -1 end
  reaper.TrackFX_SetNamedConfigParm(tr, idx, "renamed_name", tag)
  reaper.TrackFX_SetEnabled(tr, idx, true)
  return idx
end

-- set_param writes a plugin parameter by name.
local function set_param(tr, tag, plugin, name, value, create)
  local idx
  if create then
    idx = ensure_fx(tr, tag, plugin)
  else
    idx = fx_index(tr, tag, plugin)
  end
  if idx < 0 then return false end
  for i = 0, reaper.TrackFX_GetNumParams(tr, idx) - 1 do
    local _, pname = reaper.TrackFX_GetParamName(tr, idx, i, "")
    if pname == name then
      reaper.TrackFX_SetParam(tr, idx, i, value)
      return true
    end
  end
  return false
end

-- setchannel applies a whole strip at once. A desk moves as a unit, and one
-- round trip per knob would make the console feel slow over HTTP.
function ops.setchannel(args)
  local role = args[1]
  local tr = find_managed(role)
  if not tr then error("no managed track for " .. tostring(role)) end

  local trim    = tonumber(args[2]) or 0
  local high    = tonumber(args[3]) or 0
  local mid     = tonumber(args[4]) or 0
  local midfreq = tonumber(args[5]) or 1000
  local low     = tonumber(args[6]) or 0
  local comp    = tonumber(args[7]) or 0
  local pan     = tonumber(args[8]) or 0
  local fader   = tonumber(args[9]) or 0
  local muted   = args[10] == "1"
  local soloed  = args[11] == "1"

  reaper.SetMediaTrackInfo_Value(tr, "D_VOL", db_to_scalar(fader))
  reaper.SetMediaTrackInfo_Value(tr, "D_PAN", pan)
  reaper.SetMediaTrackInfo_Value(tr, "B_MUTE", muted and 1 or 0)
  reaper.SetMediaTrackInfo_Value(tr, "I_SOLO", soloed and 1 or 0)

  -- The strip's tone controls are the channel's EQ, not a second one. It is
  -- created the moment a control leaves centre, and not before: a channel
  -- nobody has touched should not collect plugins.
  local want_eq = (high ~= 0) or (mid ~= 0) or (low ~= 0) or (trim ~= 0)
  local has_eq = fx_index(tr, "cs:eq", "ReaEQ") >= 0
  if want_eq or has_eq then
    set_param(tr, "cs:eq", "ReaEQ", "Gain-High Shelf 4", eq_gain(high), true)
    set_param(tr, "cs:eq", "ReaEQ", "Gain-Band 3", eq_gain(mid), true)
    set_param(tr, "cs:eq", "ReaEQ", "Gain-Low Shelf", eq_gain(low), true)
    -- Global Gain is unity at 1.0, unlike the band gains which are unity at
    -- 0.25. Using the band scaling here inverted the sign: asking for +6 dB
    -- produced -6. Two scales in one plugin, so each needs its own.
    set_param(tr, "cs:eq", "ReaEQ", "Global Gain", db_to_scalar(trim), true)
  end
  -- The frequency sweep is logarithmic about a 40 Hz offset, not a plain log
  -- between its endpoints. Measured by sweeping the parameter and reading the
  -- plugin back: a plain log mapping asked for 2 kHz and produced 2.9 kHz.
  if midfreq > 0 and (want_eq or has_eq) then
    local offset = 40
    local lo, hi = 20 + offset, 24000 + offset
    local norm = math.log((midfreq + offset) / lo) / math.log(hi / lo)
    if norm < 0 then norm = 0 elseif norm > 1 then norm = 1 end
    set_param(tr, "cs:eq", "ReaEQ", "Freq-Band 3", norm, true)
  end

  if comp < 0 or fx_index(tr, "cs:compressor", "ReaComp") >= 0 then
    set_param(tr, "cs:compressor", "ReaComp", "Threshold", db_to_scalar(comp), true)
  end
  return "ok"
end

function ops.save()
  -- An untitled project is refused rather than saved. REAPER answers a save
  -- on an untitled project with a modal file dialog, which blocks its main
  -- thread and with it this web interface, hanging the caller on something
  -- only a human at the machine can dismiss.
  local _, name = reaper.EnumProjects(-1, "")
  if name == nil or name == "" then
    error("this REAPER project has never been saved; save it once in REAPER first")
  end
  reaper.Main_OnCommand(40026, 0) -- File: Save project
  return "ok"
end

-- verify reports the routing that matters, read-only, so a configuration can
-- be checked without changing anything.
function ops.verify(args)
  local out = {}
  for _, role in ipairs(split(args[1] or "", ",")) do
    local tr = find_managed(role)
    if not tr then
      out[#out + 1] = role .. "|missing"
    else
      local _, name = reaper.GetSetMediaTrackInfo_String(tr, "P_NAME", "", false)
      local rec = reaper.GetMediaTrackInfo_Value(tr, "I_RECINPUT")
      local chans = reaper.GetMediaTrackInfo_Value(tr, "I_NCHAN")
      local mainsend = reaper.GetMediaTrackInfo_Value(tr, "B_MAINSEND")

      local hw = "-"
      if reaper.GetTrackNumSends(tr, 1) > 0 then
        hw = tostring(math.floor(reaper.GetTrackSendInfo_Value(tr, 1, 0, "I_DSTCHAN")))
      end

      local sends = {}
      for i = 0, reaper.GetTrackNumSends(tr, 0) - 1 do
        local dest = reaper.GetTrackSendInfo_Value(tr, 0, i, "P_DESTTRACK")
        local drole = "?"
        for j = 0, reaper.CountTracks(0) - 1 do
          local cand = reaper.GetTrack(0, j)
          if cand == dest then drole = track_role(cand) or "?" end
        end
        local mode = reaper.GetTrackSendInfo_Value(tr, 0, i, "I_SENDMODE")
        sends[#sends + 1] = drole .. "@" .. tostring(math.floor(mode))
      end

      out[#out + 1] = table.concat({
        role, name, tostring(math.floor(rec)), tostring(math.floor(chans)),
        tostring(math.floor(mainsend)), hw, table.concat(sends, " "),
      }, "|")
    end
  end
  return table.concat(out, SEP)
end

-- ===== poll loop =====

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

-- One shot. REAPER's only startup hook is __startup.eel, which cannot load a
-- Lua file, so this script is registered as an action and invoked by command
-- ID instead. REAPER reloads it on each invocation, so there is no resident
-- loop to keep alive and nothing polling while the studio sits idle.
local request = reaper.GetExtState(SECTION, "req")
if request ~= "" then
  reaper.SetExtState(SECTION, "resp", handle(request), false)
end
