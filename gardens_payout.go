package main

// G940 (report #469): the gardens follow the 2012 snaps, so a street's beds can change between two
// layouts (data/gardens.json): the remake's made-up herb bed is gone from the seven 2012 crop-garden
// streets, and beds came and moved. A plot the state file keeps on a bed the layout no longer has is
// dropped when the file is read (gardens_persist.go load); before it goes, what is growing in it is
// owed to its planter, by mail (Austin 2026-11-17), as the site's one-time garden payout paid the old
// private plots (groddle-afternoon-site src/GardenPayout.php):
//
//   - a plot with seeds, shoots or a crop whose planter is known pays its crop's full produces_count
//     (GardenPayout's rule, Austin 2026-11-07: no Croppery I / Herbalism I halving); a ripe crop
//     whose planter's 60 seconds had run is anyone's to pick and has nobody to pay (counted, logged);
//   - each planter's crops go as one credit, why "garden" (credits.go), with an id made from the
//     plots it pays, so the same plots always make the same id: the site's ledger takes an id once
//     and sends the crops as letters from nobody (src/LedgerCredits.php), which the player collects
//     through the ledger like any letter;
//   - the payouts wait in the state file ("payouts") until the credits module has them in its journal
//     (it keeps them across restarts until the site says done), and are handed over when the first
//     player connects after the start. A restart before the state file is written again reads the
//     old plots again and makes the same payouts, the same ids: nobody is paid twice.
//
// Logged by counts only, never a player's name.

import (
	"hash/fnv"
	"log"
	"sort"
	"strconv"
	"strings"
)

// gardenPayout is one planter's crops from plots their layout no longer has.
type gardenPayout struct {
	ID    string         `json:"id"`
	User  int64          `json:"user"`
	Items map[string]int `json:"items"`
}

// gardenOwed is one dropped plot's crop for its planter (load's input to gardenPayouts).
type gardenOwed struct {
	user   int64
	street string
	id     string
	crop   string
	n      int
}

// owedOf is what a plot the layout no longer has pays, or false: seeds, shoots or a crop of a known
// seed, with a planter. Ripe crops past the planter's lock have planter 0 (gardens.go advance).
func (w *gardenWorld) owedOf(street, id string, r *gardenRec) (gardenOwed, bool) {
	if r == nil || r.Planter <= 0 || (r.State != "seeds" && r.State != "shoots" && r.State != "crop") {
		return gardenOwed{}, false
	}
	seed, ok := w.seeds[r.Seed]
	if !ok || seed.crop == "" || seed.produces <= 0 {
		return gardenOwed{}, false
	}
	return gardenOwed{user: r.Planter, street: street, id: id, crop: seed.crop, n: seed.produces}, true
}

// gardenPayouts sums the owed plots per planter into payouts, each id cr-g-<16 hex> from the plots
// it pays (street, plot id, crop), in a stable order.
func gardenPayouts(owed []gardenOwed) []gardenPayout {
	by := map[int64][]gardenOwed{}
	for _, o := range owed {
		by[o.user] = append(by[o.user], o)
	}
	users := make([]int64, 0, len(by))
	for u := range by {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	out := make([]gardenPayout, 0, len(users))
	for _, u := range users {
		list := by[u]
		sort.Slice(list, func(i, j int) bool {
			if list[i].street != list[j].street {
				return list[i].street < list[j].street
			}
			return list[i].id < list[j].id
		})
		h := fnv.New64a()
		h.Write([]byte(strconv.FormatInt(u, 10)))
		items := map[string]int{}
		for _, o := range list {
			h.Write([]byte("|" + o.street + "|" + o.id + "|" + o.crop))
			items[o.crop] += o.n
		}
		out = append(out, gardenPayout{ID: "cr-g-" + strconv.FormatUint(h.Sum64(), 16), User: u, Items: items})
	}
	return out
}

// addPayouts keeps new payouts not already waiting (the same id: the same plots).
func (g *gardens) addPayouts(list []gardenPayout) int {
	have := map[string]bool{}
	for _, p := range g.payouts {
		have[p.ID] = true
	}
	n := 0
	for _, p := range list {
		if p.ID == "" || p.User <= 0 || len(p.Items) == 0 || have[p.ID] {
			continue
		}
		have[p.ID] = true
		g.payouts = append(g.payouts, p)
		n++
	}
	return n
}

// payOut hands every waiting payout to the credits module (credits.put, under the hub lock). One it
// does not take (no ledger: -trade-ledger off, tests) waits in the state file for a later start.
func (g *gardens) payOut() {
	if len(g.payouts) == 0 {
		return
	}
	put, ok := g.h.ProvidedLocked("credits.put").(func(id string, user int64, why string, got tradeGoods) bool)
	if !ok {
		return
	}
	kept := g.payouts[:0]
	players, crops := 0, 0
	for _, p := range g.payouts {
		goods := tradeGoods{Items: []tradeItem{}}
		classes := make([]string, 0, len(p.Items))
		for c := range p.Items {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		n := 0
		for _, c := range classes {
			if p.Items[c] > 0 {
				goods.Items = append(goods.Items, tradeItem{C: c, N: p.Items[c]})
				n += p.Items[c]
			}
		}
		if put(p.ID, p.User, "garden", goods) {
			players++
			crops += n
			continue
		}
		kept = append(kept, p)
	}
	g.payouts = kept
	if players > 0 {
		log.Printf("gardens: %d players' crops from beds the layout no longer has handed to the ledger's mail (%d crops, %d waiting)", players, crops, len(kept))
		g.saveSoon()
	}
}

// payoutSummary is load's log line for the plots it drops with something growing in them.
func payoutSummary(paid []gardenPayout, ownerless int) string {
	crops := 0
	for _, p := range paid {
		for _, n := range p.Items {
			crops += n
		}
	}
	parts := []string{strconv.Itoa(len(paid)) + " planters owed " + strconv.Itoa(crops) + " crops by mail"}
	if ownerless > 0 {
		parts = append(parts, strconv.Itoa(ownerless)+" ripe crops past their planter's lock (anyone's to pick) go with nobody to pay")
	}
	return strings.Join(parts, ", ")
}
