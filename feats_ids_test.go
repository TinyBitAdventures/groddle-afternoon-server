package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// G968 (root R10): the game keeps every counted report owed until the server answers it and asks
// again after a lost answer, a dropped socket or a pace refusal. A report with an id never counts
// twice: done answers "dup", a report the pace took part of counts only the rest on the next ask.
func TestFeatReportIdsCountOnceWhateverTheRetries(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "animal_love")
	r := newFeatRig(t, "", newFakeFeatLedger(), start.Add(10*time.Minute), nil)
	a := r.player(1, "Ada")
	drain(a)

	got := r.add(a, "animal_love", 3, "id", "fo-aaaaaaaa01")
	if num(got["n"]) != 3 || got["id"] != "fo-aaaaaaaa01" || num(got["done"]) != 3 || num(got["want"]) != 3 {
		t.Fatalf("counted, the id and its done/want echoed: %v", got)
	}
	// The answer was lost: the same report asked again counts nothing.
	got = r.add(a, "animal_love", 3, "id", "fo-aaaaaaaa01")
	if num(got["n"]) != 0 || got["reason"] != "dup" || num(got["done"]) != 3 || num(got["total"]) != 3 {
		t.Fatalf("asked again: dup, the total unchanged: %v", got)
	}

	// The burst caps one ask; the id carries the rest, which counts at the pace on later asks, once.
	got = r.add(a, "animal_love", 20, "id", "fo-aaaaaaaa02")
	if num(got["n"]) != 7 || num(got["done"]) != 7 || num(got["want"]) != 20 {
		t.Fatalf("what the bucket held (10 less the 3): %v", got)
	}
	got = r.add(a, "animal_love", 20, "id", "fo-aaaaaaaa02")
	if num(got["n"]) != 0 || got["reason"] != "pace" || num(got["done"]) != 7 {
		t.Fatalf("an empty bucket: pace, nothing remembered as counted: %v", got)
	}
	r.now = r.now.Add(60 * time.Second) // the bucket is full again (10)
	got = r.add(a, "animal_love", 20, "id", "fo-aaaaaaaa02")
	if num(got["n"]) != 10 || num(got["done"]) != 17 {
		t.Fatalf("the next ten: %v", got)
	}
	r.now = r.now.Add(60 * time.Second)
	got = r.add(a, "animal_love", 999, "id", "fo-aaaaaaaa02") // the first want stands
	if num(got["n"]) != 3 || num(got["done"]) != 20 || num(got["want"]) != 20 {
		t.Fatalf("only the rest of the report's twenty: %v", got)
	}
	got = r.add(a, "animal_love", 20, "id", "fo-aaaaaaaa02")
	if got["reason"] != "dup" || num(got["total"]) != 23 || num(got["mine"]) != 23 {
		t.Fatalf("all twenty counted once: %v", got)
	}

	// A refusal for pace is not remembered: the id counts when the bucket has room.
	r.add(a, "animal_love", 10)
	got = r.add(a, "animal_love", 1, "id", "fo-aaaaaaaa03")
	if got["reason"] != "pace" || num(got["want"]) != 1 {
		t.Fatalf("paced: %v", got)
	}
	r.now = r.now.Add(2 * time.Second)
	if got = r.add(a, "animal_love", 1, "id", "fo-aaaaaaaa03"); num(got["n"]) != 1 {
		t.Fatalf("asked again after the pace: counted: %v", got)
	}

	// A malformed id is no id: the report counts as before.
	r.now = r.now.Add(time.Minute)
	if got = r.add(a, "animal_love", 1, "id", "<b>"); num(got["n"]) != 1 || got["id"] != nil {
		t.Fatalf("no id: %v", got)
	}
	// Not today's Feat names the id, so the game drops that report.
	if got = r.add(a, "tottlys_toys", 1, "id", "fo-aaaaaaaa04"); got["reason"] != "not_today" || got["id"] != "fo-aaaaaaaa04" {
		t.Fatalf("not_today with the id: %v", got)
	}
	// The window per player is bounded.
	for i := range featKeepIds + 5 {
		r.now = r.now.Add(3 * time.Second)
		r.add(a, "animal_love", 1, "id", fmt.Sprintf("fo-win%08d", i))
	}
	if n := len(r.m.current().Ids[1]); n != featKeepIds {
		t.Fatalf("ids kept: %d", n)
	}
}

// A first-visit Qurazy asked again after its answer was lost is "dup", not counted again.
func TestFeatReportIdsKeepActsOnce(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "qurazy_quoin_collecting")
	r := newFeatRig(t, "", newFakeFeatLedger(), start.Add(10*time.Minute), nil)
	a := r.player(1, "Ada")
	drain(a)
	got := r.add(a, "qurazy_quoin_collecting", 1, "act", "quoin", "street", "GA5101HF7F429V6", "id", "fo-qqqqqqqq01")
	if num(got["n"]) != 1 || got["street"] != "GA5101HF7F429V6" || got["id"] != "fo-qqqqqqqq01" {
		t.Fatalf("a first visit: %v", got)
	}
	got = r.add(a, "qurazy_quoin_collecting", 1, "act", "quoin", "street", "GA5101HF7F429V6", "id", "fo-qqqqqqqq01")
	if got["reason"] != "dup" || num(got["mine"]) != 1 {
		t.Fatalf("asked again: %v", got)
	}
}

// A donation's id is settled as soon as the server takes the report on (counted, or carried at the pace).
func TestFeatReportIdsSettleADonation(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "donate_sparkly")
	r := newFeatRig(t, "", newFakeFeatLedger(), start.Add(10*time.Minute), nil)
	a := r.player(1, "Ada")
	drain(a)
	got := donate(r, a, 80, "id", "fo-dddddddd01")
	if num(got["done"]) != 80 || num(got["want"]) != 80 || num(got["n"]) != 50 {
		t.Fatalf("fifty at the pace, the rest carried, the id settled: %v", got)
	}
	got = donate(r, a, 80, "id", "fo-dddddddd01")
	if got["reason"] != "dup" || num(got["n"]) != 0 {
		t.Fatalf("asked again: %v", got)
	}
	if c := r.m.current().Carry[1]; c != 30 {
		t.Fatalf("carried once: %d", c)
	}
}

// The ids are in the state file with the counts: a restart between the count and the retry keeps them.
func TestFeatReportIdsOutliveARestart(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "tottlys_toys")
	path := filepath.Join(t.TempDir(), "feats.json")
	r := newFeatRig(t, path, newFakeFeatLedger(), start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	drain(a)
	r.add(a, "tottlys_toys", 4, "id", "fo-rrrrrrrr01")  // the first contribution: saved at once
	r.add(a, "tottlys_toys", 12, "id", "fo-rrrrrrrr02") // 6 of 12 (the bucket held 10)
	r.fire(time.Second)                                 // the save within a second
	r2 := newFeatRig(t, path, newFakeFeatLedger(), r.now.Add(time.Hour), nil)
	a = r2.player(1, "Ada")
	drain(a)
	got := r2.add(a, "tottlys_toys", 4, "id", "fo-rrrrrrrr01")
	if got["reason"] != "dup" || num(got["mine"]) != 10 {
		t.Fatalf("after the restart: dup, ten counted: %v", got)
	}
	got = r2.add(a, "tottlys_toys", 12, "id", "fo-rrrrrrrr02")
	if num(got["n"]) != 6 || num(got["done"]) != 12 || num(got["mine"]) != 16 {
		t.Fatalf("the rest of the twelve, once: %v", got)
	}
}
