package runner

import (
	"context"
	"reflect"
	"testing"
)

type recordingRunner struct{ got []Spec }

func (r *recordingRunner) Run(_ context.Context, spec Spec) (Result, error) {
	r.got = append(r.got, spec)
	return Result{}, nil
}

func TestNicePrefixesInstalledCommandsWithNiceOnUnix(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		inner := &recordingRunner{}
		r := niceFor(goos, inner, 10)
		_, err := r.Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "true"}, Dir: "/work", Env: []string{"A=1"}})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", goos, err)
		}
		want := []string{"-n", "10", "sh", "-c", "true"}
		if len(inner.got) != 1 || inner.got[0].Name != "nice" || !reflect.DeepEqual(inner.got[0].Args, want) {
			t.Fatalf("%s: got %+v, want nice %v", goos, inner.got, want)
		}
		if inner.got[0].Dir != "/work" || len(inner.got[0].Env) != 1 {
			t.Errorf("%s: Dir and Env must carry over, got %+v", goos, inner.got[0])
		}
	}
}

func TestNiceLeavesCommandsAloneOnOtherPlatforms(t *testing.T) {
	inner := &recordingRunner{}
	niceFor("windows", inner, 10).Run(context.Background(), Spec{Name: "sh", Args: []string{"x"}})
	if got := inner.got[0]; got.Name != "sh" || !reflect.DeepEqual(got.Args, []string{"x"}) {
		t.Errorf("got %+v, want the spec unchanged", got)
	}
}

func TestNiceLeavesMissingExecutablesForTheInnerRunnerToReport(t *testing.T) {
	// Wrapping a missing tool in nice would turn "not installed" into an
	// opaque exit 127, so the spec must reach the inner runner untouched.
	inner := &recordingRunner{}
	niceFor("linux", inner, 10).Run(context.Background(), Spec{Name: "definitely-not-a-real-tool-xyz"})
	if got := inner.got[0]; got.Name != "definitely-not-a-real-tool-xyz" {
		t.Errorf("got %+v, want the spec unchanged", got)
	}
}

func TestNiceRunsARealCommand(t *testing.T) {
	res, err := Nice(New(), 10).Run(context.Background(), Spec{Name: "sh", Args: []string{"-c", "echo ok"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "ok\n" {
		t.Errorf("Stdout = %q, want ok", res.Stdout)
	}
}
