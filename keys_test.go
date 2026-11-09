package main

import (
	"testing"
	"time"
)

// keysHub is a hub with only the keys module (on test data), fake timers and a credits stub.
func keysHub(t *testing.T, d *keyData) (*Hub, *keysModule, *[]*fakeTimer, *[]string) {
	t.Helper()
	var mod *keysModule
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { mod = newKeysWith(h, d); return mod }}
	t.Cleanup(func() { modules = saved })
	clock := time.Unix(1_800_000_000, 0)
	hub := NewHub(func() time.Time { return clock })
	timers := &[]*fakeTimer{}
	hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &fakeTimer{d: d, fn: fn}
		*timers = append(*timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	credits := &[]string{}
	hub.provided["credits.add"] = func(user int64, why string, g tradeGoods) string {
		for _, it := range g.Items {
			*credits = append(*credits, why+":"+it.C)
		}
		return "cr-t-test"
	}
	return hub, mod, timers, credits
}

func testKeyData() *keyData {
	d, err := parseKeyData([]byte(`{"respawn":300,"spots":[["GKTEST",300,1263,4],["GKTEST",3650,1276,4],["GKOTHER",130,1253,1]]}`))
	if err != nil {
		panic(err)
	}
	return d
}

func TestKeysEmbeddedData(t *testing.T) {
	d, err := parseKeyData(doorKeysJSON)
	if err != nil {
		t.Fatal(err)
	}
	if d.Respawn != 300 {
		t.Fatalf("respawn %d, want Austin's 5 minutes", d.Respawn)
	}
	if len(d.spots) < 30 {
		t.Fatalf("only %d spots", len(d.spots))
	}
	// Shillrigg Tiers holds the Teal-Red Hexagon Key (key 4), bottom left.
	ok := false
	for _, i := range d.byStr["GCR11SBEGQL13D9"] {
		if d.spots[i].Key == 4 && d.spots[i].X < 600 {
			ok = true
		}
	}
	if !ok {
		t.Fatal("no key 4 spot on Shillrigg Tiers")
	}
}

func TestKeysSharedPickup(t *testing.T) {
	hub, _, timers, credits := keysHub(t, testKeyData())
	a := joinAt(hub, 7, "Ada", "GKTEST", 400, 1263)
	b := joinAt(hub, 8, "Bea", "GKTEST", 900, 1263)
	frames(a)
	frames(b)

	// Both see the street's two keys lying there.
	for _, cl := range []*Client{a, b} {
		send(hub, cl, map[string]any{"t": "key_sync", "street": "GKTEST"})
		m := lastOf(t, cl, "keys")
		keys := m["keys"].([]any)
		if len(keys) != 2 || keys[0].(map[string]any)["here"] != true || keys[1].(map[string]any)["here"] != true {
			t.Fatalf("keys %v", keys)
		}
	}

	// Ada picks spot 0's key: hers, gone for Bea.
	send(hub, a, map[string]any{"t": "key_take", "street": "GKTEST", "i": 0})
	got := lastOf(t, a, "key_got")
	if got["key"] != float64(4) || got["i"] != float64(0) {
		t.Fatalf("got %v", got)
	}
	taken := lastOf(t, b, "key_taken")
	if taken["id"] != float64(7) || taken["name"] != "Ada" {
		t.Fatalf("taken %v", taken)
	}
	if len(*credits) != 1 || (*credits)[0] != "take:door_key_4" {
		t.Fatalf("credits %v", *credits)
	}

	// Bea asks for it next: it is gone; a re-sync says so and when it comes back.
	send(hub, b, map[string]any{"t": "key_take", "street": "GKTEST", "i": 0})
	if r := lastOf(t, b, "key_refused"); r["why"] != "gone" {
		t.Fatalf("refused %v", r)
	}
	send(hub, b, map[string]any{"t": "key_sync", "street": "GKTEST"})
	k0 := lastOf(t, b, "keys")["keys"].([]any)[0].(map[string]any)
	if k0["here"] != false || k0["back"] != float64(300000) {
		t.Fatalf("after the take %v", k0)
	}

	// Five minutes after the pickup it lies there again, for both.
	if len(*timers) != 1 || (*timers)[0].d != 5*time.Minute {
		t.Fatalf("timers %v", *timers)
	}
	(*timers)[0].fn()
	for _, cl := range []*Client{a, b} {
		if back := lastOf(t, cl, "key_back"); back["i"] != float64(0) {
			t.Fatalf("back %v", back)
		}
	}
	send(hub, b, map[string]any{"t": "key_take", "street": "GKTEST", "i": 0})
	if lastOf(t, b, "key_got")["key"] != float64(4) {
		t.Fatal("Bea could not take the key that came back")
	}
}

func TestKeysRefusals(t *testing.T) {
	hub, _, _, credits := keysHub(t, testKeyData())
	a := joinAt(hub, 7, "Ada", "GKTEST", 3650, 1276)
	frames(a)
	// Spot 0 is 3350 px away (verb_radius 800).
	send(hub, a, map[string]any{"t": "key_take", "street": "GKTEST", "i": 0})
	if r := lastOf(t, a, "key_refused"); r["why"] != "far" {
		t.Fatalf("far %v", r)
	}
	// Spot 2 lies on another street.
	send(hub, a, map[string]any{"t": "key_take", "street": "GKTEST", "i": 2})
	if r := lastOf(t, a, "key_refused"); r["why"] != "unknown" {
		t.Fatalf("unknown %v", r)
	}
	// A street the player is not on.
	send(hub, a, map[string]any{"t": "key_take", "street": "GKOTHER", "i": 2})
	if r := lastOf(t, a, "key_refused"); r["why"] != "street" {
		t.Fatalf("street %v", r)
	}
	if len(*credits) != 0 {
		t.Fatalf("credited %v", *credits)
	}
	// A sync for a street with no keys is an empty list; one for another street is ignored.
	b := joinAt(hub, 8, "Bea", "GNOKEYS", 0, 0)
	frames(b)
	send(hub, b, map[string]any{"t": "key_sync", "street": "GNOKEYS"})
	if keys := lastOf(t, b, "keys")["keys"].([]any); len(keys) != 0 {
		t.Fatalf("keys %v", keys)
	}
	send(hub, b, map[string]any{"t": "key_sync", "street": "GKTEST"})
	noneOf(t, b, "keys")
}

func TestKeysRespawnFromEnv(t *testing.T) {
	t.Setenv("GRODDLE_KEYS_RESPAWN", "20")
	_, mod, _, _ := keysHub(t, testKeyData())
	if mod.respawn != 20*time.Second {
		t.Fatalf("respawn %v", mod.respawn)
	}
}
