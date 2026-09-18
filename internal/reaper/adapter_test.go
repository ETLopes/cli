package reaper

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ETLopes/cli/internal/studio"
)

// fakeREAPER emulates the web interface plus a bridge that answers requests,
// so the wire protocol can be exercised without REAPER running.
type fakeREAPER struct {
	mu    sync.Mutex
	state map[string]string
	// handle produces the bridge's payload for an operation. Returning an
	// error makes the bridge reply with an err response.
	handle func(op string, args []string) (string, error)
	// deaf makes the bridge ignore requests, as if the script were not loaded.
	deaf  bool
	calls []string
}

func newFakeREAPER() *fakeREAPER {
	return &fakeREAPER{
		state:  map[string]string{},
		handle: func(string, []string) (string, error) { return "ok", nil },
	}
}

func (f *fakeREAPER) server(t *testing.T) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)

	a := &Adapter{client: &Client{BaseURL: srv.URL, HTTP: srv.Client()}}
	return a
}

func (f *fakeREAPER) serve(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/_/")
	for _, cmd := range strings.Split(raw, ";") {
		decoded, err := url.PathUnescape(cmd)
		if err != nil {
			decoded = cmd
		}
		parts := strings.Split(decoded, "/")

		f.mu.Lock()
		switch {
		case parts[0] == "TRANSPORT":
			w.Write([]byte("TRANSPORT\t0\t0.0\t0\t1.1.00\n"))
		case parts[0] == "SET" && parts[1] == "EXTSTATE" && len(parts) >= 5:
			key := parts[3]
			value := strings.Join(parts[4:], "/")
			f.state[key] = value
			if key == "req" && !f.deaf {
				f.respond(value)
			}
		case parts[0] == "GET" && parts[1] == "EXTSTATE" && len(parts) >= 4:
			w.Write([]byte("EXTSTATE\t" + parts[2] + "\t" + parts[3] + "\t" + f.state[parts[3]] + "\n"))
		}
		f.mu.Unlock()
	}
}

// respond runs the fake bridge. The caller holds the lock.
func (f *fakeREAPER) respond(request string) {
	fields := strings.Split(request, fieldSep)
	if len(fields) < 2 {
		return
	}
	seq, op, args := fields[0], fields[1], fields[2:]
	f.calls = append(f.calls, op+"("+strings.Join(args, ",")+")")

	payload, err := f.handle(op, args)
	if err != nil {
		f.state["resp"] = seq + fieldSep + "err" + fieldSep + err.Error()
		return
	}
	f.state["resp"] = seq + fieldSep + "ok" + fieldSep + payload
}

func (f *fakeREAPER) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestPingReportsREAPER(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(op string, _ []string) (string, error) {
		if op != "ping" {
			t.Errorf("unexpected op %q", op)
		}
		return "REAPER 7.80/OSX64", nil
	}

	info, err := f.server(t).Ping(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "REAPER" || !strings.Contains(info.Version, "7.80") {
		t.Errorf("info = %+v, want REAPER 7.80", info)
	}
}

// REAPER answering while the script is absent is a different problem from
// REAPER being unreachable, and must be reported as such.
func TestBridgeNotLoadedIsDistinct(t *testing.T) {
	f := newFakeREAPER()
	f.deaf = true
	a := f.server(t)

	// Shorten the wait so the test does not sit for the full timeout.
	_, err := a.callWithTimeout(context.Background(), 200*time.Millisecond, "ping")
	var notLoaded *BridgeNotLoadedError
	if !errors.As(err, &notLoaded) {
		t.Fatalf("error = %v (%T), want BridgeNotLoadedError", err, err)
	}
	if !strings.Contains(err.Error(), "cli studio install") {
		t.Errorf("error should say how to fix it, got: %v", err)
	}
}

// A response left behind by an earlier run must never be read as this one's.
func TestStaleResponseIsIgnored(t *testing.T) {
	f := newFakeREAPER()
	f.state["resp"] = "999-old" + fieldSep + "ok" + fieldSep + "stale payload"
	f.deaf = true
	a := f.server(t)

	_, err := a.callWithTimeout(context.Background(), 200*time.Millisecond, "ping")
	if err == nil {
		t.Fatal("a stale response should not satisfy a new request")
	}
	if strings.Contains(err.Error(), "stale payload") {
		t.Errorf("stale payload leaked into the result: %v", err)
	}
}

func TestBridgeErrorsSurface(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(string, []string) (string, error) {
		return "", errors.New("no managed track for guitar")
	}
	err := f.server(t).SetMonitorMute(context.Background(), true)
	if err == nil {
		t.Fatal("expected the bridge error to surface")
	}
	if !strings.Contains(err.Error(), "no managed track") {
		t.Errorf("error = %v, want the bridge's message", err)
	}
}

func TestSetupSendsFullTopology(t *testing.T) {
	f := newFakeREAPER()
	var gotInstruments, gotBuses string
	f.handle = func(op string, args []string) (string, error) {
		if op == "setup" && len(args) == 2 {
			gotInstruments, gotBuses = args[0], args[1]
		}
		return "created|track Guitar|input 3, mono" + fieldSep + "unchanged|bus MAIN|", nil
	}

	report, err := f.server(t).Setup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"guitar:Guitar:3", "dtx:DTX:7", "mic1:Mic 1:1"} {
		if !strings.Contains(gotInstruments, want) {
			t.Errorf("instrument spec missing %q; got %q", want, gotInstruments)
		}
	}
	// Buses are sent as zero-based channels with a mono flag: MAIN stereo on
	// 0 (outputs 1/2), and each cue mono on its own output.
	for _, want := range []string{"main:MAIN:0:0", "cue1:CUE 1:2:1", "cue8:CUE 8:9:1"} {
		if !strings.Contains(gotBuses, want) {
			t.Errorf("bus spec missing %q; got %q", want, gotBuses)
		}
	}

	if len(report.Actions) != 2 {
		t.Fatalf("got %d actions, want 2", len(report.Actions))
	}
	if report.Actions[0].Kind != "created" || report.Actions[0].Object != "track Guitar" {
		t.Errorf("action = %+v", report.Actions[0])
	}
	if !report.Changed() {
		t.Error("a report containing a creation should count as changed")
	}
}

func TestSetupReportUnchangedIsNotChanged(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(string, []string) (string, error) {
		return "unchanged|track Guitar|" + fieldSep + "unchanged|bus MAIN|", nil
	}
	report, err := f.server(t).Setup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Changed() {
		t.Error("a report of only unchanged actions should not count as changed")
	}
}

func TestSetSendLevelAddressesTheRightObjects(t *testing.T) {
	f := newFakeREAPER()
	a := f.server(t)

	if err := a.SetSendLevel(context.Background(), 2, "guitar", 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := f.called()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "setsend(cue2,guitar,3.00") {
		t.Errorf("call = %v, want setsend(cue2,guitar,3.00)", calls)
	}

	if err := a.SetSendLevel(context.Background(), 9, "guitar", 0); err == nil {
		t.Error("cue 9 does not exist and should be refused before any call")
	}
	if err := a.SetSendLevel(context.Background(), 1, "trombone", 0); err == nil {
		t.Error("an unknown instrument should be refused before any call")
	}
}

// The adapter sends the plugin name, since that is what REAPER can look up.
func TestSetEffectSendsThePluginName(t *testing.T) {
	f := newFakeREAPER()
	a := f.server(t)
	if err := a.SetEffect(context.Background(), "guitar", "overdrive", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	eff, _ := studio.LookupEffect("guitar", "overdrive")
	if got := f.called(); len(got) != 1 || !strings.Contains(got[0], eff.Plugin) {
		t.Errorf("call = %v, want the plugin %q", got, eff.Plugin)
	}
}

func TestSnapshotReadsStateBack(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(op string, _ []string) (string, error) {
		if op != "snapshot" {
			return "ok", nil
		}
		return strings.Join([]string{
			"send|cue1|guitar|3.00",
			"send|cue1|bass|-2.00",
			"monvol|-6.00",
			"monmute|0",
			"fx|guitar|JS: Distortion|1",
		}, fieldSep), nil
	}

	session, err := f.server(t).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cue, _ := session.Cue(1)
	if cue.Level("guitar") != 3 {
		t.Errorf("cue 1 guitar = %v, want 3", cue.Level("guitar"))
	}
	if cue.Level("bass") != -2 {
		t.Errorf("cue 1 bass = %v, want -2", cue.Level("bass"))
	}
	if m := session.Monitor(); m.Volume != -6 || m.Muted {
		t.Errorf("monitor = %+v, want -6 dB unmuted", m)
	}
	if !session.EffectEnabled("guitar", "overdrive") {
		t.Error("overdrive should be read back as enabled")
	}
}

// REAPER holds plugins the studio does not manage; their presence is normal.
func TestSnapshotIgnoresUnmanagedEntries(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(op string, _ []string) (string, error) {
		if op != "snapshot" {
			return "ok", nil
		}
		return strings.Join([]string{
			"fx|guitar|Some Third Party Reverb|1",
			"send|cue1|guitar|1.00",
			"nonsense",
			"",
		}, fieldSep), nil
	}
	session, err := f.server(t).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("unmanaged entries should not fail the read: %v", err)
	}
	cue, _ := session.Cue(1)
	if cue.Level("guitar") != 1 {
		t.Errorf("cue 1 guitar = %v, want 1", cue.Level("guitar"))
	}
}

// The bridge protocol shares one request slot and one response slot in
// REAPER's extended state. Concurrent calls -- which the mixer produces every
// time an arrow key is held -- must not overwrite each other's request before
// it has been answered.
func TestConcurrentCallsDoNotInterfere(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(op string, args []string) (string, error) {
		if len(args) > 0 {
			return args[0], nil // echo the argument so a mix-up is visible
		}
		return "ok", nil
	}
	a := f.server(t)

	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = a.callWithTimeout(context.Background(), 3*time.Second,
				"setmonvol", strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent call %d failed: %v", i, err)
		}
	}
}

// A tuner processes nothing audible: its whole value is the readout. Loading
// it without opening its window looks to the player exactly like nothing
// happened, which is what made this worth fixing.
func TestDisplayEffectsCarryTheShowFlag(t *testing.T) {
	f := newFakeREAPER()
	a := f.server(t)

	if err := a.SetEffect(context.Background(), "guitar", "tuner", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := f.called()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if got := setfxArg(t, calls[0], showUIArg); got != "1" {
		t.Errorf("call = %q, want it to request the window be shown", calls[0])
	}

	// An audible effect must not steal focus by opening a window.
	f2 := newFakeREAPER()
	a2 := f2.server(t)
	if err := a2.SetEffect(context.Background(), "guitar", "overdrive", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := setfxArg(t, f2.called()[0], showUIArg); got != "0" {
		t.Errorf("call = %q, want no window for an audible effect", f2.called()[0])
	}
}

// Argument positions within a recorded setfx call.
const (
	showUIArg  = 3
	initialArg = 4
)

// setfxArg pulls one argument out of a recorded "setfx(a,b,c,d,e)" call.
// Reading by position rather than by matching the end of the string means
// adding another argument does not silently invalidate the assertion.
func setfxArg(t *testing.T, call string, index int) string {
	t.Helper()
	open := strings.IndexByte(call, '(')
	if open < 0 || !strings.HasSuffix(call, ")") {
		t.Fatalf("unrecognised call %q", call)
	}
	args := strings.Split(call[open+1:len(call)-1], ",")
	if index >= len(args) {
		t.Fatalf("call %q has no argument %d", call, index)
	}
	return args[index]
}

// A compressor that loads with its threshold at 0 dBFS never engages, so
// switching it on does nothing a player can hear. Starting values must reach
// the plugin.
func TestCompressorCarriesAWorkingThreshold(t *testing.T) {
	f := newFakeREAPER()
	a := f.server(t)

	if err := a.SetEffect(context.Background(), "bass", "compressor", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := setfxArg(t, f.called()[0], initialArg)
	if !strings.HasPrefix(got, "Threshold=") {
		t.Fatalf("initial params = %q, want a Threshold", got)
	}
	// -18 dB as the linear scalar the plugin expects.
	var v float64
	if _, err := fmt.Sscanf(strings.TrimPrefix(got, "Threshold="), "%g", &v); err != nil {
		t.Fatalf("unparsable threshold %q", got)
	}
	if db := 20 * math.Log10(v); math.Abs(db-(-18)) > 0.01 {
		t.Errorf("threshold is %.2f dB, want -18", db)
	}
}

// The bridge's reply is not taken on trust: anything that is not a real effect
// in that instrument's chain is ignored, so an unexpected response cannot turn
// into a list of repairs that never happened.
func TestAdoptIgnoresUnrecognisedNames(t *testing.T) {
	f := newFakeREAPER()
	f.handle = func(op string, args []string) (string, error) {
		if op == "adopt" {
			// One real effect, and two things that are not.
			return "overdrive,nonsense,input 3, mono", nil
		}
		return "unchanged|bus MAIN|", nil
	}

	report, err := f.server(t).Setup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range report.Actions {
		if strings.Contains(a.Object, "nonsense") || strings.Contains(a.Object, "mono") {
			t.Errorf("an unrecognised name was reported as repaired: %q", a.Object)
		}
	}
	// The real one should still be adopted, for guitar which has an overdrive.
	var sawReal bool
	for _, a := range report.Actions {
		if strings.Contains(a.Object, "Guitar overdrive") {
			sawReal = true
		}
	}
	if !sawReal {
		t.Error("a genuine adoption should still be reported")
	}
}
