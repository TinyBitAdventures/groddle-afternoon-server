package main

// The shared quoins' state file (quoins.go): a quoin someone grabbed stays gone
// for everyone until its respawn_time is up (the longest are an hour), and each
// player's grabs this game day keep counting towards quoinCreditDaily, across a
// restart of this server. Without it a restart put every grabbed quoin back at
// once and started the day's count again, so the same quoins could be grabbed
// and credited twice. -quoins-file (default realtime-quoins.json beside
// -secret-file; off: memory only) is the whole state, written through
// writeFileSynced within a second of a grab or a respawn. The respawn timers
// are armed again on the first connect after a start; a quoin whose time ran
// out while the server was down is simply back.

import (
	"encoding/json"
	"flag"
	"log"
	"strconv"
	"time"
)

var quoinsFileFlag = flag.String("quoins-file", "", "file that keeps which shared quoins are gone until when, and each player's grabs this game day (default: realtime-quoins.json beside -secret-file; off: memory only)")

// quoinsFileForTest lets a test give the module a state file; under go test the flag is ignored.
var quoinsFileForTest string

type quoinsFile struct {
	V     int                         `json:"v"`
	Gone  map[string]map[string]int64 `json:"gone"`  // street -> spot index -> Unix ms it comes back
	Day   int64                       `json:"day"`   // the game day of grabs
	Grabs map[int64]int               `json:"grabs"` // player -> grabs that day
}

func (m *quoinsModule) today() int64 {
	return floorDiv(m.h.Now().UnixMilli()-gameEpochMs, gameDayMs)
}

func (m *quoinsModule) markDue(street string, i int, at time.Time) {
	if m.path == "" {
		return
	}
	if at.IsZero() {
		delete(m.due[street], i)
		if len(m.due[street]) == 0 {
			delete(m.due, street)
		}
		return
	}
	if m.due[street] == nil {
		m.due[street] = map[int]time.Time{}
	}
	m.due[street][i] = at
}

func (m *quoinsModule) load() error {
	var f quoinsFile
	ok, err := readStateFile("quoins", m.path, &f)
	if err != nil || !ok {
		return err
	}
	now := m.h.Now()
	n := 0
	for street, list := range f.Gone {
		spots := m.spotsOf(street)
		for k, ms := range list {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(spots) || !timeOf(ms).After(now) {
				continue
			}
			m.markDue(street, i, timeOf(ms))
			n++
		}
	}
	if f.Day == m.today() {
		for id, c := range f.Grabs {
			if id > 0 && c > 0 {
				m.grabs[id] = quoinDay{day: f.Day, n: c}
			}
		}
	}
	m.resumed = n == 0
	for street, list := range m.due {
		if m.gone[street] == nil {
			m.gone[street] = map[int]func() bool{}
		}
		for i := range list {
			// Gone until resume arms its timer (a claim before that is refused, as it should be).
			m.gone[street][i] = func() bool { return false }
		}
	}
	log.Printf("quoins %s: %d quoins gone, %d players' grabs today", m.path, n, len(m.grabs))
	return nil
}

// resume arms the respawn timers the file's gone quoins wait for.
func (m *quoinsModule) resume() {
	if m.resumed {
		return
	}
	m.resumed = true
	now := m.h.Now()
	for street, list := range m.due {
		for i, at := range list {
			street, i := street, i
			m.gone[street][i] = m.h.After(max(0, at.Sub(now)), func() { m.respawn(street, i) })
		}
	}
}

func (m *quoinsModule) save() bool {
	if m.path == "" {
		return true
	}
	f := quoinsFile{V: 1, Gone: map[string]map[string]int64{}, Day: m.today(), Grabs: map[int64]int{}}
	for street, list := range m.due {
		out := make(map[string]int64, len(list))
		for i, at := range list {
			out[strconv.Itoa(i)] = at.UnixMilli()
		}
		f.Gone[street] = out
	}
	for id, g := range m.grabs {
		if g.day == f.Day && g.n > 0 {
			f.Grabs[id] = g.n
		}
	}
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("quoins %s: %v", m.path, err)
		return false
	}
	return true
}

func (m *quoinsModule) saveSoon() {
	if m.path == "" || m.saveAt {
		return
	}
	m.saveAt = true
	m.h.After(time.Second, func() {
		m.saveAt = false
		m.save()
	})
}
