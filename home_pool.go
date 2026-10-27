package main

// Visitors on a home street fill its owner's Magic Rock pool (register G201,
// with homes.go the visiting itself). In the original each new visitor a
// game day added 3% of the owner's cap to the home street's pool of iMG
// (GameServerJS locations/home.js onPlayerEnter 89-97 -> home_log_visitor
// 435-458 -> locations/inc_cultivation.js cultivation_add_img_rewards
// 168-178): on the public half only (is_public: the street, not the house),
// for anyone but the owner. The owner's Magic Rock hands the best earlier
// day over (cultivation_get_img_rewards 244-277).
//
// This module only reports what it sees: a character joining HOME_<owner>
// (the street; homes.go has already let them in with the site's pass) who is
// not the owner. The site keeps the pool and decides the amount from the
// owner's stored level (groddle-afternoon-site src/HomePool.php, POST
// groddle/v1/homes/pool/visit, signed as ledger.go signs), and counts each
// visitor once a game day however often they come and go. Here a visitor is
// told to the site once per owner and game day while this process runs; a
// call the site did not answer is asked again on the next join.
//
// No messages: the owner's game reads the pool from the site at its rock.

import (
	"log"
	"strconv"
	"testing"
)

const ledgerRouteHomeVisit = "/groddle/v1/homes/pool/visit"

func init() { modules = append(modules, newHomePool) }

// homeVisitSite is the site's pool (httpLedger). Calls block; the module makes them off the hub lock.
type homeVisitSite interface {
	homeVisit(owner, visitor int64) ledgerAnswer
}

func (l *httpLedger) homeVisit(owner, visitor int64) ledgerAnswer {
	return l.call(ledgerRouteHomeVisit, map[string]any{"owner": owner, "visitor": visitor})
}

type homePool struct {
	h     *Hub
	site  homeVisitSite // nil: nothing is told (no -ledger-url, or -trade-ledger off)
	spawn func(func())
	told  map[string]bool // owner|visitor|game day, told or being told
	day   int64
}

func newHomePool(h *Hub) Module {
	m := &homePool{h: h, spawn: func(fn func()) { go fn() }, told: map[string]bool{}}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
			}
		}
	}
	return m
}

func (m *homePool) Register(r *Registry) {
	r.OnJoin(m.joined)
}

func (m *homePool) today() int64 {
	return floorDiv(m.h.Now().UnixMilli()-gameEpochMs, gameDayMs)
}

// joined is home_log_visitor for a visitor on the owner's home street.
func (m *homePool) joined(cl *Client) {
	owner, house := homeOwner(cl.Street())
	visitor := cl.ID()
	if owner == 0 || house || owner == visitor || m.site == nil {
		return
	}
	day := m.today()
	if day != m.day {
		// A new game day: yesterday's visitors may count again.
		m.day = day
		clear(m.told)
	}
	key := strconv.FormatInt(owner, 10) + "|" + strconv.FormatInt(visitor, 10) + "|" + strconv.FormatInt(day, 10)
	if m.told[key] {
		return
	}
	m.told[key] = true
	site := m.site
	m.spawn(func() {
		a := site.homeVisit(owner, visitor)
		m.h.After(0, func() {
			switch a.Status {
			case "settled":
				log.Printf("home pool: %d visited %d's home street (game day %d)", visitor, owner, day)
			case "refused":
				// own, no_home: nothing to ask again today.
			default:
				delete(m.told, key)
			}
		})
	})
}
