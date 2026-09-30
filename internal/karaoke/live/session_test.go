package live

import (
	"testing"
)

const testSeconds = 11.0

func TestBackingTrackAloneScoresNearZeroAndASingerFollowingTheReferenceScoresHigh(t *testing.T) {
	fx := newFixture(t, testSeconds)
	cal := knownCalibration(t, 48000, &roomA, &roomB)
	sess, _ := start(t, runSpec{fx: fx, rate: 48000, cal: cal, rooms: []mic{
		{room: roomA, singer: &singer{latency: 0.05, level: 0.15}},
		{room: roomB}, // nobody singing: the backing bleed only
	}})
	res, err := sess.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.Incomplete || len(res.Players) != 2 {
		t.Fatalf("results = %+v", res)
	}
	t.Logf("singer %d, backing only %d", res.Players[0].Score, res.Players[1].Score)
	if got := res.Players[0].Score; got < 8500 {
		t.Errorf("the singer following the reference scored %d, want at least 8500", got)
	}
	if got := res.Players[1].Score; got > 300 {
		t.Errorf("the backing track alone scored %d, want at most 300", got)
	}
	if res.Players[0].Name != "Player 1" || res.Players[1].Name != "Player 2" {
		t.Errorf("names = %q, %q", res.Players[0].Name, res.Players[1].Name)
	}
}
