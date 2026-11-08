package main

// The Rook's lasting marks (rook.go): what it rooked stays rooked until it is
// revived, with the doses given so far, a street it attacked rests its hour,
// a player who met it rests their 6 hours (rookPlayerRest), and the next
// scheduled attack waits for its time ("next", a day after the last one
// started: Austin 2026-11-04), across a restart of this server. -rook-file
// (default realtime-rook.json beside -secret-file; off: memory only) is the
// whole state, written through writeFileSynced when something is rooked, a dose
// is given, a player meets a Rook, a scheduled attack starts, an attack ends and
// the server stops. Rests that are over are dropped on load and save. A file
// without "next" (written before the daily rule) loads as before, and the day
// counts from that first load (rook.go newRook), which writes it at once. A
// fight under way is not kept: a restart ends it (the offering was the
// initiator's, spent on their client), as it ends every session.

import (
	"encoding/json"
	"flag"
	"log"
	"time"
)

var rookFileFlag = flag.String("rook-file", "", "file that keeps what the Rook rooked until it is revived, the streets resting after an attack, the players resting after meeting a Rook and when the next scheduled attack may come (default: realtime-rook.json beside -secret-file; off: memory only)")

// rookFileForTest lets a test give the module a state file; under go test the flag is ignored.
var rookFileForTest string

type rookRec struct {
	Rooked map[string]rookedRec `json:"rooked,omitempty"`
	Objs   map[string]string    `json:"objs,omitempty"` // a rooked key's shared tree (objects.go id)
	Rest   int64                `json:"rest,omitempty"` // Unix ms the street rests until
}

type rookedRec struct {
	Class   string  `json:"class"`
	Healers []int64 `json:"healers,omitempty"`
}

type rookFile struct {
	V       int                 `json:"v"`
	Streets map[string]*rookRec `json:"streets"`
	Met     map[int64]int64     `json:"met,omitempty"`  // player (user id) -> Unix ms they rest from the Rook until
	Next    int64               `json:"next,omitempty"` // Unix ms the next scheduled attack may start from (rookDaily)
}

func (m *rook) load() error {
	var f rookFile
	ok, err := readStateFile("rook", m.path, &f)
	if err != nil || !ok {
		return err
	}
	now := m.h.Now()
	m.next = timeOf(f.Next) // zero when the file has none: newRook counts the day from now
	for id, ms := range f.Met {
		if until := timeOf(ms); id > 0 && until.After(now) {
			m.met[id] = until
		}
	}
	n := 0
	for tsid, rec := range f.Streets {
		if _, known := m.w.streets[tsid]; !known || rec == nil {
			continue
		}
		rest := timeOf(rec.Rest)
		if len(rec.Rooked) == 0 && !rest.After(now) {
			continue
		}
		s := m.street(tsid)
		s.restUntil = rest
		for key, r := range rec.Rooked {
			if class := rookKeyClass(key); class == "" || !rookableClasses[r.Class] || (rookPatchKeyRe.MatchString(key) && class != r.Class) {
				continue
			}
			s.rooked[key] = &rookedThing{class: r.Class, healers: append([]int64(nil), r.Healers...)}
			if rookKeyRe.MatchString(key) {
				s.keys[key] = r.Class // a Patch tree's key comes from the patches module (wingBeat)
			}
			if id := rec.Objs[key]; id != "" {
				s.objs[key] = id
			}
			n++
		}
	}
	log.Printf("rook %s: %d rooked things on %d streets, %d players resting, next scheduled attack from %s", m.path, n, len(m.streets), len(m.met), rookNextText(m.next))
	return nil
}

// save writes the rooked things and the resting streets now; false when it could not.
func (m *rook) save() bool {
	now := m.h.Now()
	for id, until := range m.met {
		if !until.After(now) {
			delete(m.met, id)
		}
	}
	if m.path == "" {
		return true
	}
	f := rookFile{V: 1, Streets: map[string]*rookRec{}, Next: msOf(m.next)}
	for id, until := range m.met {
		if f.Met == nil {
			f.Met = map[int64]int64{}
		}
		f.Met[id] = msOf(until)
	}
	for tsid, s := range m.streets {
		if len(s.rooked) == 0 && !s.restUntil.After(now) {
			continue
		}
		rec := &rookRec{Rest: msOf(s.restUntil)}
		for key, r := range s.rooked {
			if rec.Rooked == nil {
				rec.Rooked = map[string]rookedRec{}
			}
			rec.Rooked[key] = rookedRec{Class: r.class, Healers: append([]int64(nil), r.healers...)}
			if id := s.objs[key]; id != "" {
				if rec.Objs == nil {
					rec.Objs = map[string]string{}
				}
				rec.Objs[key] = id
			}
		}
		f.Streets[tsid] = rec
	}
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("rook %s: %v", m.path, err)
		return false
	}
	return true
}

func rookNextText(t time.Time) string {
	if t.IsZero() {
		return "a day after this load (none kept)"
	}
	return t.UTC().Format(time.RFC3339)
}
