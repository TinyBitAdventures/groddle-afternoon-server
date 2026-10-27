package main

// The Rook's lasting marks (rook.go): what it rooked stays rooked until it is
// revived, with the doses given so far, and a street it attacked rests its
// hour, across a restart of this server. -rook-file (default realtime-rook.json
// beside -secret-file; off: memory only) is the whole state, written through
// writeFileSynced when something is rooked, a dose is given and an attack
// ends. A fight under way is not kept: a restart ends it (the offering was the
// initiator's, spent on their client), as it ends every session.

import (
	"encoding/json"
	"flag"
	"log"
)

var rookFileFlag = flag.String("rook-file", "", "file that keeps what the Rook rooked until it is revived, and the streets resting after an attack (default: realtime-rook.json beside -secret-file; off: memory only)")

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
}

func (m *rook) load() error {
	var f rookFile
	ok, err := readStateFile("rook", m.path, &f)
	if err != nil || !ok {
		return err
	}
	now := m.h.Now()
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
			if !rookKeyRe.MatchString(key) || !rookableClasses[r.Class] {
				continue
			}
			s.rooked[key] = &rookedThing{class: r.Class, healers: append([]int64(nil), r.Healers...)}
			s.keys[key] = r.Class
			if id := rec.Objs[key]; id != "" {
				s.objs[key] = id
			}
			n++
		}
	}
	log.Printf("rook %s: %d rooked things on %d streets", m.path, n, len(m.streets))
	return nil
}

// save writes the rooked things and the resting streets now; false when it could not.
func (m *rook) save() bool {
	if m.path == "" {
		return true
	}
	now := m.h.Now()
	f := rookFile{V: 1, Streets: map[string]*rookRec{}}
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
