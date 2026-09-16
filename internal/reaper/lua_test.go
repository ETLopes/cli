package reaper

import (
	"os/exec"
	"strings"
	"testing"
)

// The bridge runs inside REAPER where Go tests cannot reach it, so its pure
// logic is exercised through a Lua interpreter with the REAPER API stubbed.
// This caught a split() bug that silently dropped the last field of every
// request, which would have broken every command the bridge handles.
func TestBridgeLuaLogic(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("lua not installed; skipping bridge logic checks (brew install lua)")
	}

	cmd := exec.Command(lua, "bridge_test.lua")
	cmd.Dir = "luatest"
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bridge logic checks failed:\n%s", out)
	}
	if !strings.Contains(string(out), "ALL LUA CHECKS PASSED") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

// A syntax error would only surface inside REAPER, where it is invisible to
// this project's tests, so it is caught here instead.
func TestBridgeLuaParses(t *testing.T) {
	luac, err := exec.LookPath("luac")
	if err != nil {
		t.Skip("luac not installed; skipping syntax check")
	}
	if out, err := exec.Command(luac, "-p", "bridge.lua").CombinedOutput(); err != nil {
		t.Fatalf("bridge.lua does not parse:\n%s", out)
	}
}
