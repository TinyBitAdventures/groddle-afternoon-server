package main

// The daily prize count per pair of players (G734). A game between real players pays its winner
// (races.go: a race, Grab 'Em Good or The Great Hog Haul; crowns.go: Game of Crowns; mayhem.go: Math
// Mayhem; verbs.go: Charades), and the same two players could otherwise play each other for prizes
// all day. Each win is counted for the pair it makes with every other player in that game (the
// drinker for Charades), per game and per UTC day, and pays while every one of those pairs has been
// paid fewer than prizePairDaily wins that day. Past it the game still plays, it just stops paying:
// no ledger credit, and the winner's prize message says so ("prize": false, or no iMG and currants).
//
// Ours: the source had no such count (inc_games.js games_end_race 1330-1390, games_end_quoin_grab
// 1529-1600, games_end_math_mayhem 1048-1155, main.js 1780-1786 always paid). prizePairDaily is
// generous: the same two players winning 20 games of one kind against each other in a day is more
// than a long evening of honest play. Kept in memory: a restart of this server starts the day's
// count again.

import (
	"strconv"
	"time"
)

const prizePairDaily = 20

// prizeBook is the day's paid wins, by game and pair ("race|3|7", the smaller id first).
type prizeBook struct {
	day  string
	paid map[string]int
}

func prizePair(game string, a, b int64) string {
	if a > b {
		a, b = b, a
	}
	return game + "|" + strconv.FormatInt(a, 10) + "|" + strconv.FormatInt(b, 10)
}

// prizeLocked says whether a win of game by winner over others pays, and counts it when it does.
// Others that are the winner, or 0, are not counted; a win over nobody pays.
func (h *Hub) prizeLocked(game string, winner int64, others []int64) bool {
	day := h.Now().UTC().Format(time.DateOnly)
	if h.prizes == nil || h.prizes.day != day {
		h.prizes = &prizeBook{day: day, paid: map[string]int{}}
	}
	keys := map[string]bool{}
	for _, o := range others {
		if o == 0 || o == winner {
			continue
		}
		k := prizePair(game, winner, o)
		if h.prizes.paid[k] >= prizePairDaily {
			return false
		}
		keys[k] = true
	}
	for k := range keys {
		h.prizes.paid[k]++
	}
	return true
}
