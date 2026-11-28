package main

import (
	"net/http"
	"testing"
	"time"
)

// A buddy going on and off over and over must not fill their friends' screens
// with "is online" and "went offline" (Austin 2026-11-28). The lists stay live;
// only what is said aloud (no "quiet") is held back.

// pass moves the rig's clock on by d and fires every timer that would have run by then.
func (r *buddyRig) pass(d time.Duration, since time.Time) {
	r.now = r.now.Add(d)
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if !tm.stopped && !since.Add(tm.d).After(r.now) {
			tm.stopped = true
			tm.fn()
		}
	}
}

// aloud is the frames of kind a player would be shown a notice for.
func aloud(frames []map[string]any, kind string) int {
	n := 0
	for _, f := range frames {
		if f["t"] == kind && f["quiet"] != true {
			n++
		}
	}
	return n
}

func count(frames []map[string]any, kind string) int {
	n := 0
	for _, f := range frames {
		if f["t"] == kind {
			n++
		}
	}
	return n
}

func TestAFlappingBuddyIsAnnouncedOncePerWindow(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	r.join(a, "GA1")
	r.watch(a, []int64{2}, nil, nil)
	drain(a)
	b := r.connect(2, "Bo")
	r.join(b, "GA2")
	r.watch(b, []int64{1}, nil, nil)
	if f := drain(a); aloud(f, "buddy_online") != 1 {
		t.Fatalf("Bo's first time online is said: %v", f)
	}
	// Bo goes off for good (past any grace) and comes back, five times in about three minutes.
	var heard []map[string]any
	for i := 0; i < 5; i++ {
		r.now = r.now.Add(2 * time.Second)
		gone := r.now
		r.hub.Disconnect(b)
		r.pass(35*time.Second, gone)
		b = r.connect(2, "Bo")
		r.join(b, "GA2")
		r.watch(b, []int64{1}, nil, nil)
		heard = append(heard, drain(a)...)
	}
	// The dot follows every one of them.
	if count(heard, "buddy_offline") != 5 || count(heard, "buddy_online") != 5 {
		t.Fatalf("the list follows each change: %s", buddyKinds(heard))
	}
	// Said aloud: the one "went offline" after the online that was said, and nothing more.
	if on, off := aloud(heard, "buddy_online"), aloud(heard, "buddy_offline"); on != 0 || off != 1 {
		t.Fatalf("aloud: %d online, %d offline (want 0 and 1): %v", on, off, heard)
	}
	// A short stay after a quiet online leaves quietly.
	r.now = r.now.Add(time.Minute)
	gone := r.now
	r.hub.Disconnect(b)
	r.pass(35*time.Second, gone)
	if f := drain(a); count(f, "buddy_offline") != 1 || aloud(f, "buddy_offline") != 0 {
		t.Fatalf("a short stay after a quiet online leaves quietly: %v", f)
	}
	// Five minutes on from the last "is online", the next coming online is said again.
	r.now = r.now.Add(5 * time.Minute)
	b = r.connect(2, "Bo")
	r.watch(b, []int64{1}, nil, nil)
	if f := drain(a); aloud(f, "buddy_online") != 1 {
		t.Fatalf("after the window it is said again: %v", f)
	}
}

func TestReloadsAndPlayHereSayNothing(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, []int64{2}, nil, nil)
	b := r.connect(2, "Bo")
	r.join(b, "GA2")
	r.watch(b, []int64{1}, nil, nil)
	r.now = r.now.Add(10 * time.Minute)
	drain(a)
	// Three reloads in a few seconds, then a slow one (a phone that took 20 s to come back).
	for _, away := range []time.Duration{time.Second, 2 * time.Second, time.Second, 20 * time.Second} {
		gone := r.now
		r.hub.Disconnect(b)
		r.pass(away, gone)
		b = r.connect(2, "Bo")
		r.join(b, "GA2")
		r.watch(b, []int64{1}, nil, nil)
	}
	// Play here: a second tab takes the character over (closeReplaced), and the first takes it back.
	b2 := r.connect(2, "Bo")
	r.join(b2, "GA2")
	r.watch(b2, []int64{1}, nil, nil)
	b = r.connect(2, "Bo")
	r.join(b, "GA2")
	r.watch(b, []int64{1}, nil, nil)
	r.hub.Disconnect(b2) // the replaced socket ending changes nothing
	r.pass(10*time.Minute, r.now)
	if f := drain(a); len(f) != 0 {
		t.Fatalf("a reload or Play here is not leaving: %v", f)
	}
}

func TestAppearOfflineFlappingIsQuiet(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, []int64{2}, nil, nil)
	b := r.connect(2, "Bo")
	r.watch(b, []int64{1}, nil, nil)
	drain(a)
	drain(b)
	var heard []map[string]any
	for i := 0; i < 4; i++ {
		for _, body := range []string{`{"player":2,"off":true}`, `{"player":2,"off":false}`} {
			if code, _ := presenceHideService(r.hub, []byte(body)); code != http.StatusOK {
				t.Fatal(code)
			}
			r.now = r.now.Add(time.Second)
		}
		heard = append(heard, drain(a)...)
	}
	if count(heard, "buddy_offline") != 4 || count(heard, "buddy_online") != 4 {
		t.Fatalf("the list follows the switch: %s", buddyKinds(heard))
	}
	if on, off := aloud(heard, "buddy_online"), aloud(heard, "buddy_offline"); on != 0 || off != 1 {
		t.Fatalf("aloud: %d online, %d offline (want 0 and 1)", on, off)
	}
}

func TestABlockFlipIsNeverSaidAloud(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	c := r.connect(3, "Cy")
	r.watch(c, []int64{1}, nil, nil)
	r.watch(a, []int64{3}, nil, nil)
	drain(c)
	var heard []map[string]any
	for i := 0; i < 3; i++ {
		r.now = r.now.Add(buddyWatchEvery * buddyWatchBurst)
		r.watch(a, nil, nil, []int64{3})
		r.watch(a, []int64{3}, nil, nil)
		heard = append(heard, drain(c)...)
	}
	if count(heard, "buddy_offline") != 3 || count(heard, "buddy_online") != 3 {
		t.Fatalf("the list follows the block: %s", buddyKinds(heard))
	}
	if aloud(heard, "buddy_online")+aloud(heard, "buddy_offline") != 0 {
		t.Fatalf("a block or un-block is a list change, not a coming or going: %v", heard)
	}
}

func TestALongStayEndsAloud(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, []int64{2}, nil, nil)
	b := r.connect(2, "Bo")
	r.watch(b, []int64{1}, nil, nil)
	// Off (said), back a minute later (quiet), then twenty minutes of play: the end of that is said.
	gone := r.now
	r.hub.Disconnect(b)
	r.pass(time.Minute, gone)
	b = r.connect(2, "Bo")
	r.watch(b, []int64{1}, nil, nil)
	drain(a)
	r.now = r.now.Add(20 * time.Minute) // within the watch token's hour
	gone = r.now
	r.hub.Disconnect(b)
	r.pass(time.Minute, gone)
	if f := drain(a); aloud(f, "buddy_offline") != 1 {
		t.Fatalf("leaving after a long stay is said: %v", f)
	}
}
