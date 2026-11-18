package main

// Pot Luck Day's Harvest Table (G982, Austin 2026-11-17: "one shared Harvest Table by the Groddle
// Forest Junction signpost"; the game's js/pot-luck-feast.js and features/pot-luck-feast-feature.js).
// Ours: Glitch never ran a Thanksgiving event (the game's inventions register, pot-luck-table).
//
// One table, everyone's: what anyone lays on it stays there for everyone on the street to see, and
// the table fills over the week. The server holds it: the offerings (the newest feastKeep, with the
// count of all of them), and each player's game day of their last offering, so a player's first
// offering of a game day is decided here, once ("first": the game gives that player a little mood
// and favor with Pot). When Pot Luck Day ends the table is packed away for everyone: a timer at the
// window's end clears it and tells the street, and a table read after the end (a state file, a
// restart) or from another year is cleared as it is read.
//
// Client to server:
//
//	{"t":"feast_lay","c":"pumpkin_pie"}
//	{"t":"feast_sync"}   asks for feast_state again (a page that started listening after its join)
//
// Refused, in this order, as feast_no {"c","code","message"} with the game's lines
// (js/pot-luck-feast.js TABLE_LINES): another street ("not_here"), out of season ("no_season"), a
// class that is no food (data/trade.json food: a dish or a crop; "not_dish"), a lay while the last
// one waits ("slow_down"). Then the site's ledger takes the item, a whole one (objects_uses.go, as a
// Piggy's food), and refuses one the player does not hold ("no_item"); no answer refuses too
// ("no_answer"), and nothing changes. Without the ledger (-trade-ledger off, tests) the item is the
// client's word and "use" is left out.
//
// Server to client:
//
//	{"t":"feast_state","street":"G…","on":true,"rows":[{"c","by","name"}],"total":12}   after each join on the table's street
//	{"t":"feast_laid","street":"G…","row":{"c","by","name"},"total":13}                 to everyone else there
//	{"t":"feast_laid",…,"mine":true,"first":true,"use":{…}}                              to the one who laid it
//	{"t":"feast_state","street":"G…","on":false,"rows":[],"total":0}                    the season over: packed away
//
// The table survives a restart (-feast-file, default realtime-feast.json beside -secret-file; off:
// memory only). It also counts each kind of food laid this season ("counts": class -> lays, only
// classes the table takes, so at most the food classes), which Thanksgiving Day's feast draws from
// (G1009); the 40 rows alone cannot say what the whole week brought.

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"
)

const (
	// The table's street, keyed without its first letter (G or L): Groddle Forest Junction.
	feastStreetKey = "LI3272LOTD1B1F"
	// Offerings the table remembers to draw (js/pot-luck-feast.js TABLE_RULES.keep).
	feastKeep = 40
	// A player's lays come no faster than this (ours).
	feastEvery = 300 * time.Millisecond

	feastTextNoSeason = "The Harvest Table is packed away until Pot Luck Day."
	feastTextNotDish  = "Only a dish or a crop belongs on the Harvest Table."
	feastTextNotHere  = "The Harvest Table is in Groddle Forest Junction."
)

// feastExtra: food the game designed (no GSJS file, so not in data/trade.json until it is rebuilt).
var feastExtra = map[string]bool{"harvest_stuffing": true}

var feastFileFlag = flag.String("feast-file", "", "file that keeps Pot Luck Day's Harvest Table, its offerings and each player's last offering day (default: realtime-feast.json beside -secret-file; off: memory only)")

// feastFileForTest lets a test give the module a state file; under go test the flag is ignored.
var feastFileForTest string

type feastRow struct {
	C    string `json:"c"`
	By   int64  `json:"by"`
	Name string `json:"name"`
}

// feastSaved is the state file.
type feastSaved struct {
	Key    string          `json:"key"`
	Rows   []feastRow      `json:"rows"`
	Total  int             `json:"total"`
	Counts map[string]int  `json:"counts,omitempty"`
	Daily  map[int64]int64 `json:"daily"`
}

type harvestTable struct {
	h      *Hub
	key    string // the season's year ("pot_luck_day:2026"), "" when packed away
	rows   []feastRow
	total  int
	counts map[string]int  // class -> lays this season (G1009)
	daily  map[int64]int64 // player -> game day of their last offering
	layAt  map[int64]time.Time
	uses   *useAsker
	path   string
	ending func() bool // the end-of-season timer's stop
}

func init() { modules = append(modules, newHarvestTable) }

func newHarvestTable(h *Hub) Module {
	m := &harvestTable{h: h, counts: map[string]int{}, daily: map[int64]int64{}, layAt: map[int64]time.Time{}, uses: newUseAsker(h, "feast: the Harvest Table")}
	if m.path = statePath(feastFileForTest, feastFileFlag, "realtime-feast.json"); m.path != "" {
		var s feastSaved
		if ok, err := readStateFile("feast", m.path, &s); err != nil {
			log.Printf("feast %s: %v (the Harvest Table is kept in memory only)", m.path, err)
			m.path = ""
		} else if ok {
			m.key, m.rows, m.total = s.Key, s.Rows, s.Total
			if s.Daily != nil {
				m.daily = s.Daily
			}
			for c, n := range s.Counts {
				if n > 0 && feastTakes(c) {
					m.counts[c] = n
				}
			}
			if len(m.rows) > feastKeep {
				m.rows = m.rows[len(m.rows)-feastKeep:]
			}
		}
	}
	// A table from a season that is over (a restart after the end, another year) is packed away.
	m.pack(h.Now())
	return m
}

func (m *harvestTable) Register(r *Registry) {
	r.Handle("feast_lay", m.lay)
	r.Handle("feast_sync", func(cl *Client, _ json.RawMessage) { m.joined(cl) })
	r.OnJoin(m.joined)
	r.OnDisconnect(func(cl *Client) { delete(m.layAt, cl.ID()) })
	r.OnShutdown(func() { m.save() })
	// For tests and other modules: the table's count and its newest offering.
	r.Provide("feast.table", func() (int, []feastRow) { return m.total, append([]feastRow(nil), m.rows...) })
	// Each kind of food laid this season and how many (a copy), for Thanksgiving Day's feast (G1009).
	r.Provide("feast.counts", func() map[string]int { return m.layCounts() })
}

// feastTakes: is this class food the table takes (a dish or a crop, or the game's own dish)?
func feastTakes(c string) bool {
	_, food := pigFoods[c]
	return food || feastExtra[c]
}

// layCounts is a copy of the season's per-class lay counts.
func (m *harvestTable) layCounts() map[string]int {
	out := make(map[string]int, len(m.counts))
	for c, n := range m.counts {
		out[c] = n
	}
	return out
}

func isFeastStreet(street string) bool { return len(street) > 1 && street[1:] == feastStreetKey }

// seasonKey is the season's year on the calendar the seasons read, or "" out of season.
func feastSeasonKey(now time.Time) string {
	if !potLuckOn(now) {
		return ""
	}
	y, _, _, _ := seasonDate(now)
	return fmt.Sprintf("pot_luck_day:%d", y)
}

// pack clears the table when its season is not the one on the calendar now; reports whether it did.
func (m *harvestTable) pack(now time.Time) bool {
	key := feastSeasonKey(now)
	if key == m.key {
		return false
	}
	had := m.total > 0 || len(m.rows) > 0 || len(m.daily) > 0 || len(m.counts) > 0
	m.key, m.rows, m.total, m.counts, m.daily = key, nil, 0, map[string]int{}, map[int64]int64{}
	if had {
		log.Printf("feast: the Harvest Table is packed away (season %q)", key)
	}
	m.saveSoon()
	return true
}

// settle packs the table away when its season is over and arms the end-of-season timer (the
// season's last second; a held QA date never ends). Reports whether the table changed season.
func (m *harvestTable) settle() bool {
	now := m.h.Now()
	cleared := m.pack(now)
	if m.key != "" && m.ending == nil {
		if left := potLuckLeft(now); left > 0 && left < 300*24*time.Hour {
			m.ending = m.h.After(left+time.Second, func() {
				m.ending = nil
				if m.settle() {
					m.broadcastState()
				}
			})
		}
	}
	return cleared
}

func (m *harvestTable) state(street string) map[string]any {
	rows := m.rows
	if rows == nil {
		rows = []feastRow{}
	}
	return map[string]any{"t": "feast_state", "street": street, "on": m.key != "", "rows": rows, "total": m.total}
}

// broadcastState tells every table street (G… and L…) the table as it is now.
func (m *harvestTable) broadcastState() {
	for _, street := range []string{"G" + feastStreetKey, "L" + feastStreetKey} {
		m.h.BroadcastStreetLocked(street, m.state(street), 0)
	}
}

func (m *harvestTable) joined(cl *Client) {
	street := cl.Street()
	if !isFeastStreet(street) {
		return
	}
	if m.settle() {
		m.broadcastState()
		return
	}
	m.h.SendLocked(cl, m.state(street))
}

func (m *harvestTable) refuse(cl *Client, c, code, message string) {
	m.h.SendLocked(cl, map[string]any{"t": "feast_no", "c": c, "code": code, "message": message})
}

// lay is one offering: the refusals, then the ledger takes the item, then it is on the table.
func (m *harvestTable) lay(cl *Client, frame json.RawMessage) {
	var f struct {
		C string `json:"c"`
	}
	if json.Unmarshal(frame, &f) != nil || f.C == "" || len(f.C) > 64 {
		return
	}
	street := cl.Street()
	if !isFeastStreet(street) {
		m.refuse(cl, f.C, "not_here", feastTextNotHere)
		return
	}
	if m.settle() {
		m.broadcastState()
	}
	if m.key == "" {
		m.refuse(cl, f.C, "no_season", feastTextNoSeason)
		return
	}
	if !feastTakes(f.C) {
		m.refuse(cl, f.C, "not_dish", feastTextNotDish)
		return
	}
	now := m.h.Now()
	if at, ok := m.layAt[cl.ID()]; ok && now.Sub(at) < feastEvery {
		m.refuse(cl, f.C, "slow_down", "Slow down a little.")
		return
	}
	m.layAt[cl.ID()] = now
	id, name, key := cl.ID(), cl.Name(), m.key
	m.uses.ask(cl, f.C, 1, func(use map[string]any, code, message string) {
		if code != "" {
			m.refuse(cl, f.C, code, message)
			return
		}
		// The season ended while the ledger answered: the item is given back, the table stays packed.
		if m.settle() {
			m.broadcastState()
		}
		if m.key == "" || m.key != key {
			if use != nil {
				m.uses.giveBack(use, id)
			}
			m.refuse(cl, f.C, "no_season", feastTextNoSeason)
			return
		}
		row := feastRow{C: f.C, By: id, Name: name}
		m.rows = append(m.rows, row)
		if len(m.rows) > feastKeep {
			m.rows = m.rows[len(m.rows)-feastKeep:]
		}
		m.total++
		m.counts[f.C]++
		day := floorDiv(m.h.Now().UnixMilli()-gameEpochMs, gameDayMs)
		last, had := m.daily[id]
		first := !had || last != day
		m.daily[id] = day
		m.saveSoon()
		log.Printf("feast: %d laid %s on the Harvest Table (%d in all, first today %v)", id, f.C, m.total, first)
		street := cl.Street()
		if !isFeastStreet(street) {
			street = "G" + feastStreetKey
		}
		msg := map[string]any{"t": "feast_laid", "street": street, "row": row, "total": m.total}
		m.h.BroadcastStreetLocked(street, msg, id)
		mine := map[string]any{"t": "feast_laid", "street": street, "row": row, "total": m.total, "mine": true, "first": first}
		if use != nil {
			mine["use"] = use
		}
		m.h.SendLocked(cl, mine)
	})
}

func (m *harvestTable) saved() feastSaved {
	return feastSaved{Key: m.key, Rows: m.rows, Total: m.total, Counts: m.counts, Daily: m.daily}
}

// saveSoon writes the table off the hub lock (statewrite.go).
func (m *harvestTable) saveSoon() {
	if m.path == "" {
		return
	}
	b, _ := json.Marshal(m.saved())
	if err := writeStateSoon(m.path, b); err != nil {
		log.Printf("feast %s: %v", m.path, err)
	}
}

func (m *harvestTable) save() {
	m.saveSoon()
}
