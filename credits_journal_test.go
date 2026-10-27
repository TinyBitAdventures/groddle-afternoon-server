package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func down(int, []creditEvent) ([]string, error) {
	return nil, errors.New("dial tcp: connection refused")
}

// journalledOutbox is an outbox on a journal at path, as newCredits makes one.
func journalledOutbox(t *testing.T, site creditSite, path string) *creditOutbox {
	t.Helper()
	o := newCreditOutbox(site)
	if err := o.journalled(path); err != nil {
		t.Fatal(err)
	}
	return o
}

func journalLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	for sc := bufio.NewScanner(f); sc.Scan(); {
		n++
	}
	return n
}

func TestCreditsTheSiteHasNotTakenOutliveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credits.jsonl")
	// The site is down: three credits wait.
	o := journalledOutbox(t, &fakeCreditSite{answer: down}, path)
	for i := 0; i < 3; i++ {
		o.add(event(i))
	}
	if left, err := o.flushOnce(); left != 3 || err == nil {
		t.Fatalf("the site is down: %d left, %v", left, err)
	}
	// Restarted, with the site back: the same three go, ids and all, and the run loop is woken for them.
	site := &fakeCreditSite{answer: func(n int, batch []creditEvent) ([]string, error) {
		return []string{batch[0].ID}, nil // the first only, this time
	}}
	o = journalledOutbox(t, site, path)
	if o.pending() != 3 || len(o.wake) != 1 {
		t.Fatalf("after the restart %d wait (woken %d)", o.pending(), len(o.wake))
	}
	o.flushOnce()
	if len(site.batches) != 1 || len(site.batches[0]) != 3 {
		t.Fatalf("sent %v", site.batches)
	}
	for i, ev := range site.batches[0] {
		if want := event(i); ev.ID != want.ID || !sameCredit(ev, want) {
			t.Fatalf("credit %d came back as %+v", i, ev)
		}
	}
	// Another restart: only the two the site has not named done.
	site = &fakeCreditSite{answer: allDone}
	o = journalledOutbox(t, site, path)
	if o.pending() != 2 || o.queue[0].ID != event(1).ID || o.queue[1].ID != event(2).ID {
		t.Fatalf("after the second restart: %+v", o.queue)
	}
	if left, err := o.flushOnce(); left != 0 || err != nil {
		t.Fatalf("then all done: %d %v", left, err)
	}
	// And after that, nothing waits: the journal is empty once reopened.
	o = journalledOutbox(t, &fakeCreditSite{answer: down}, path)
	if o.pending() != 0 || len(o.wake) != 0 || journalLines(t, path) != 0 {
		t.Fatalf("all done, yet %d wait (%d lines)", o.pending(), journalLines(t, path))
	}
	// New credits after a restart go on the journal too.
	o.add(event(7))
	o = journalledOutbox(t, &fakeCreditSite{answer: down}, path)
	if o.pending() != 1 || o.queue[0].ID != event(7).ID {
		t.Fatalf("a credit after the restart: %+v", o.queue)
	}
}

func TestATornCreditsJournalLineIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credits.jsonl")
	o := journalledOutbox(t, &fakeCreditSite{answer: down}, path)
	o.add(event(0))
	o.add(event(1))
	// A crash mid-write leaves half a line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"add":{"id":"cr-q-torn","us`)
	f.Close()
	o = journalledOutbox(t, &fakeCreditSite{answer: down}, path)
	if o.pending() != 2 || o.queue[0].ID != event(0).ID || o.queue[1].ID != event(1).ID {
		t.Fatalf("kept %+v", o.queue)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "torn") || journalLines(t, path) != 2 {
		t.Fatalf("the reopened journal reads %s", b)
	}
}

func TestTheCreditsJournalIsCompacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credits.jsonl")
	o := journalledOutbox(t, &fakeCreditSite{answer: allDone}, path)
	for i := 0; i < creditCompactAt; i++ {
		o.add(creditEvent{ID: "cr-q-c-" + strings.Repeat("x", 1+i%30) + string(rune('a'+i/30%26)) + string(rune('a'+i/780)), User: 7, Why: "quoin", Got: tradeGoods{Currants: 1}})
		if i%50 == 49 {
			o.flushOnce()
		}
	}
	if o.journal.lines >= creditCompactAt || journalLines(t, path) != o.journal.lines {
		t.Fatalf("the journal holds %d lines (%d on disk)", o.journal.lines, journalLines(t, path))
	}
	if o.pending() != 0 {
		t.Fatalf("%d wait", o.pending())
	}
}

func TestAnUnreadableCreditsJournalKeepsCreditsInMemory(t *testing.T) {
	dir := t.TempDir()
	o := newCreditOutbox(&fakeCreditSite{answer: down})
	if err := o.journalled(dir); err == nil { // a directory is no journal
		t.Fatal("a directory opened as a journal")
	}
	o.add(event(0))
	if o.pending() != 1 || o.journal != nil {
		t.Fatalf("memory only: %d, %v", o.pending(), o.journal)
	}
}
