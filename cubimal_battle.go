package main

// Cubimal battles between real players (register G40). Every Cubimal figure
// carries the battle in GameServerJS npc_cubimal_base (line numbers from
// items/npc_cubimal_chick.js; the game's js/cubimal-battle.js has the whole
// account): Panache, Wile or Gumption (battle) sets the figure down at its
// owner's feet, and a second later search 994 pairs it with another player's
// figure set down to fight on the same street within 450 px, or it says
// "Come at me, Bro!" and waits there for the next one. The pair take turns
// insulting each other (onBattleStart 696, onBattleLoop 673, taunt 1065) until
// one's Ego reaches 0 (checkWin 536); the winner gains ten experience points a
// level of the loser's and maybe a level (makeWinner 645), and both go back to
// their packs. The numbers, the insults and the words are the game's
// data/cubimal_battle.json (tools/build-realtime-cubimal-battle.mjs).
//
// The server owns who fights whom, every roll, the Egos and the outcome. A
// figure's level and experience are the player's client's word, from the
// figure's own copy in the save (as every other figure detail is), checked to
// whole numbers within the table's ceilings; its Ego follows from its level.
// The winner's client keeps the new level and experience the server sends.
// Tottly's Toys counts here (feats.count): 1 a win, 1 more for a level.
//
// Client to server:
//
//	{"t":"cubi_battle","c":"npc_cubimal_chick","style":"p|s|r","name":"Sprocket","level":1,"xp":0}   set your figure down to fight (where you stand)
//	{"t":"cubi_pickup"}                                                                              pick your waiting figure up again
//
// Server to client:
//
//	{"t":"cubi_fighters","street":"…","fighters":[Fighter…]}   on joining a street with figures out: those figures
//	{"t":"cubi_fighter","f":Fighter}                            to the street: a figure set down (yours too: the answer to cubi_battle)
//	{"t":"cubi_say","id":7,"text":"Come at me, Bro!","ms":3000} a figure's bubble (the style shouts go to its owner only)
//	{"t":"cubi_vs","a":7,"b":9,"text":"Ada's Chick Cubimal Lvl.1 VS. Bo's Rook Cubimal Lvl.2"}   to the street
//	{"t":"cubi_strike","id":7,"vs":9,"to":512,"speed":150,"text":"…","ego":6,"choke":false}       to the street: 7 insults 9 (and runs to x 512 unless it choked)
//	{"t":"cubi_back","id":7,"to":430,"speed":100}               to the street: 7 walks back to its side
//	{"t":"cubi_line","text":"Rook Cubimal Lvl.2's Ego remaining: 6"}   an activity line (the Ego to both owners)
//	{"t":"cubi_won","id":7,"vs":9,"text":"Ada's … defeated Bo's …","level":2,"xp":0,"earned":10,"level_up":true}   to the street; 9 goes up in a puff, 7's gain is settled
//	{"t":"cubi_home","id":9,"why":"lost|won|picked|left","level":2,"xp":0,"earned":10,"level_up":true}   to the street: the figure leaves the street for its owner's pack
//	{"t":"error","code":"cubi_battle","message":"…"}            refused (busy, slow_down, bad figure, not on a street)
//
// Fighter: {"id":owner,"name":owner's name,"c":class,"label":"Chick Cubimal Lvl.1","level":1,"style":"p","x":…,"y":…,"state":"waiting|fighting","vs":0,"ego":10}
//
// Ours: reachability (apiFindPath) is ground at most sameGroundPx apart in
// height; of several waiting figures in reach the nearest is taken (the source
// took the first its search found); a figure whose owner leaves the street or
// goes offline goes back to the pack at once, and a fight that loses either
// owner is called off with no winner (in the source the figures stayed on the
// street); one figure out at a time a player.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

//go:embed data/cubimal_battle.json
var cubiBattleJSON []byte

type cubiTable struct {
	Styles          []string                              `json:"styles"`
	Shouts          map[string]string                     `json:"shouts"`
	Base            struct{ Level, XP, Next, Health int } `json:"base"`
	HealthPerLevel  int                                   `json:"healthPerLevel"`
	NextFactor      float64                               `json:"nextFactor"`
	XPPerLoserLevel int                                   `json:"xpPerLoserLevel"`
	LevelMax        int                                   `json:"levelMax"`
	XPMax           int                                   `json:"xpMax"`
	InsultLevelCap  int                                   `json:"insultLevelCap"`
	ChokeFrom       int                                   `json:"chokeFrom"`
	ChokeOddsHigh   int                                   `json:"chokeOddsHigh"`
	ChokeOddsLow    int                                   `json:"chokeOddsLow"`
	Insults         []string                              `json:"insults"`
	Text            struct {
		ComeAtMe string `json:"comeAtMe"`
		No       string `json:"no"`
		LevelUp  string `json:"levelUp"`
		XP       string `json:"xp"`
		VS       string `json:"vs"`
		Defeated string `json:"defeated"`
		Ego      string `json:"ego"`
	} `json:"text"`
	Timing struct {
		SearchMs, StartMs, InsultDelayMs, InsultMs, ShoutMs, FirstLoopMs, LoopMs, TauntMs int64
		ComeAtMeMs, LoserHomeMs, WinnerMs, WinnerHomeMs, BubbleMs                         int64
	} `json:"timing"`
	SearchRange  float64           `json:"searchRange"`
	StrikeGap    float64           `json:"strikeGap"`
	MiddleGap    float64           `json:"middleGap"`
	StrikeSpeed  float64           `json:"strikeSpeed"`
	BackSpeed    float64           `json:"backSpeed"`
	SameGroundPx float64           `json:"sameGroundPx"`
	Feat         string            `json:"feat"`
	FeatWin      int64             `json:"featWin"`
	FeatLevel    int64             `json:"featLevel"`
	Labels       map[string]string `json:"labels"`
}

// cubiPlaceGap is the least time between two figures one player sets down (each fight takes seconds).
const cubiPlaceGap = time.Second

type cubiFighter struct {
	owner     int64
	ownerName string
	street    string
	class     string
	name      string
	level     int
	xp        int
	style     string
	x, y      float64
	restX     float64
	ego       int
	fighting  bool
	vs        *cubiFighter
	first     bool
	midSet    bool
	speech    string
	done      bool
	result    map[string]any // a winner's gain, sent with it home
}

type cubiBattles struct {
	h        *Hub
	t        cubiTable
	rng      func() float64
	fighters map[int64]*cubiFighter // by owner
	placedAt map[int64]time.Time
}

func init() { modules = append(modules, newCubiBattles) }

func newCubiBattles(h *Hub) Module {
	m := &cubiBattles{h: h, rng: rand.Float64, fighters: map[int64]*cubiFighter{}, placedAt: map[int64]time.Time{}}
	if err := json.Unmarshal(cubiBattleJSON, &m.t); err != nil {
		panic("realtime: data/cubimal_battle.json: " + err.Error())
	}
	return m
}

func (m *cubiBattles) Register(r *Registry) {
	r.Handle("cubi_battle", m.place)
	r.Handle("cubi_pickup", m.pickup)
	r.OnConnect(func(cl *Client) { m.leave(cl.ID()) })
	r.OnJoin(m.joined)
	r.OnLeave(func(cl *Client, _ string) { m.leave(cl.ID()) })
	r.OnDisconnect(func(cl *Client) { delete(m.placedAt, cl.ID()) })
}

func (m *cubiBattles) clampLevel(v int) int { return max(1, min(m.t.LevelMax, v)) }

// ego is the health a figure starts a fight with (initInstanceProps 33, makeWinner 657).
func (m *cubiBattles) ego(level int) int {
	return m.t.Base.Health + m.t.HealthPerLevel*(m.clampLevel(level)-1)
}

// nextLevel is next_level_amount at a level: intval(amount) x 2.2 a level up (makeWinner 650, 656).
func (m *cubiBattles) nextLevel(level int) int {
	n := m.t.Base.Next
	for l := 1; l < m.clampLevel(level); l++ {
		n = int(math.Floor(float64(n) * m.t.NextFactor))
	}
	return n
}

// label is getLabel 580.
func (m *cubiBattles) label(f *cubiFighter) string {
	n := f.name
	if n == "" {
		n = m.t.Labels[f.class]
	}
	return fmt.Sprintf("%s Lvl.%d", n, f.level)
}

// owned is linkifyPlayer(owner, true) + " " + getLabel().
func (m *cubiBattles) owned(f *cubiFighter) string { return f.ownerName + "'s " + m.label(f) }

func (m *cubiBattles) view(f *cubiFighter) map[string]any {
	state := "waiting"
	if f.fighting {
		state = "fighting"
	}
	vs := int64(0)
	if f.vs != nil {
		vs = f.vs.owner
	}
	return map[string]any{"id": f.owner, "name": f.ownerName, "c": f.class, "label": m.label(f), "level": f.level, "style": f.style,
		"x": math.Round(f.x), "y": math.Round(f.y), "state": state, "vs": vs, "ego": f.ego}
}

func (m *cubiBattles) refuse(cl *Client, message string) { m.h.ErrorLocked(cl, "cubi_battle", message) }

// randInt is common.js randInt.
func (m *cubiBattles) randInt(lo, hi int) int { return lo + int(math.Floor(m.rng()*float64(1+hi-lo))) }

// alive: f is still out and is the same figure (a callback set before it went home does nothing).
func (m *cubiBattles) alive(f *cubiFighter) bool { return !f.done && m.fighters[f.owner] == f }

func (m *cubiBattles) place(cl *Client, frame json.RawMessage) {
	var msg struct {
		C     string `json:"c"`
		Style string `json:"style"`
		Name  string `json:"name"`
		Level int    `json:"level"`
		XP    int    `json:"xp"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		m.refuse(cl, "bad message")
		return
	}
	street := cl.Street()
	if street == "" {
		m.refuse(cl, "not on a street")
		return
	}
	if _, ok := m.t.Labels[msg.C]; !ok {
		m.refuse(cl, "not a figure that battles")
		return
	}
	style := ""
	for _, s := range m.t.Styles {
		if s == msg.Style {
			style = s
		}
	}
	if style == "" {
		m.refuse(cl, "no such style")
		return
	}
	if m.fighters[cl.ID()] != nil {
		m.refuse(cl, "busy")
		return
	}
	now := m.h.Now()
	if at, ok := m.placedAt[cl.ID()]; ok && now.Sub(at) < cubiPlaceGap {
		m.refuse(cl, "slow_down")
		return
	}
	m.placedAt[cl.ID()] = now
	x, y := cl.Pos()
	level := m.clampLevel(msg.Level)
	f := &cubiFighter{owner: cl.ID(), ownerName: cl.Name(), street: street, class: msg.C, name: cleanCopyName(msg.Name, 32),
		level: level, xp: max(0, min(m.t.XPMax, msg.XP)), style: style, x: x, y: y, restX: x, ego: m.ego(level), first: true}
	m.fighters[cl.ID()] = f
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "cubi_fighter", "f": m.view(f)}, 0)
	m.h.After(time.Duration(m.t.Timing.SearchMs)*time.Millisecond, func() { m.search(f) })
}

// search is search 994: pair with another player's waiting figure in reach, or wait.
func (m *cubiBattles) search(f *cubiFighter) {
	if !m.alive(f) || f.vs != nil {
		return
	}
	var cands []*cubiFighter
	for _, o := range m.fighters {
		if o == f || o.owner == f.owner || o.street != f.street || o.done || o.vs != nil {
			continue
		}
		if math.Abs(o.x-f.x) <= m.t.SearchRange && math.Abs(o.y-f.y) <= m.t.SameGroundPx {
			cands = append(cands, o)
		}
	}
	if len(cands) == 0 {
		m.say(f, m.t.Text.ComeAtMe, m.t.Timing.ComeAtMeMs, false)
		return
	}
	sort.Slice(cands, func(i, j int) bool {
		di, dj := math.Abs(cands[i].x-f.x), math.Abs(cands[j].x-f.x)
		if di != dj {
			return di < dj
		}
		return cands[i].owner < cands[j].owner
	})
	o := cands[0]
	f.vs, o.vs = o, f
	f.fighting, o.fighting = true, true
	m.h.BroadcastStreetLocked(f.street, map[string]any{"t": "cubi_vs", "a": f.owner, "b": o.owner, "text": fmt.Sprintf(m.t.Text.VS, m.owned(f), m.owned(o))}, 0)
	m.h.After(time.Duration(m.t.Timing.StartMs)*time.Millisecond, func() { m.strike(f) })
}

func (m *cubiBattles) say(f *cubiFighter, text string, ms int64, ownerOnly bool) {
	msg := map[string]any{"t": "cubi_say", "id": f.owner, "text": text, "ms": ms}
	if ownerOnly {
		if cl := m.h.ClientLocked(f.owner); cl != nil && cl.Street() == f.street {
			m.h.SendLocked(cl, msg)
		}
		return
	}
	m.h.BroadcastStreetLocked(f.street, msg, 0)
}

// styleMod is getType 591-622 as written (no row for Panache against Wile).
func styleMod(mine, theirs string) int {
	switch mine + theirs {
	case "rs", "pr", "sp":
		return 1
	case "rp", "sr":
		return -1
	}
	return 0
}

// strike is onBattleStart 696 for f against its opponent.
func (m *cubiBattles) strike(f *cubiFighter) {
	o := f.vs
	if !m.alive(f) || o == nil || !m.alive(o) {
		return
	}
	mod := styleMod(f.style, o.style)
	lv := f.level
	var index int
	if lv >= m.t.InsultLevelCap {
		index = m.randInt(m.t.InsultLevelCap, m.t.InsultLevelCap+3+mod)
	} else {
		index = m.randInt(lv, lv+3+mod)
	}
	actual := m.randInt(lv, lv+3+mod)
	odds := m.t.ChokeOddsLow
	if lv >= m.t.ChokeFrom {
		odds = m.t.ChokeOddsHigh
	}
	if int(math.Floor(m.rng()*float64(odds))) == 0 {
		index = 0
	}
	index = max(0, min(len(m.t.Insults)-1, index))
	insult := m.t.Insults[index]
	bubble := insult
	if insult == o.speech && index != 0 {
		bubble = m.t.Text.No + insult // showInsult 1057-1061
	}
	if index != 0 {
		if insult == o.speech {
			o.ego -= int(math.Ceil(float64(index) / 2))
		} else {
			o.ego -= actual
		}
	}
	f.speech = insult
	if !f.midSet {
		f.restX = restX(f.x, o.x, m.t.MiddleGap)
		f.midSet = true
	}
	to := f.x
	if index != 0 {
		if o.x < f.x {
			to = o.x + m.t.StrikeGap
		} else {
			to = o.x - m.t.StrikeGap
		}
	}
	m.h.BroadcastStreetLocked(f.street, map[string]any{"t": "cubi_strike", "id": f.owner, "vs": o.owner, "to": math.Round(to), "speed": m.t.StrikeSpeed,
		"text": bubble, "ego": o.ego, "choke": index == 0, "ms": m.t.Timing.InsultMs}, 0)
	f.x = to
	loop := m.t.Timing.LoopMs
	if f.first {
		m.say(f, m.t.Shouts[f.style], m.t.Timing.ShoutMs, true)
		m.say(o, m.t.Shouts[o.style], m.t.Timing.ShoutMs, true)
		f.first, o.first = false, false
		loop = m.t.Timing.FirstLoopMs
	}
	m.h.After(time.Duration(loop)*time.Millisecond, func() { m.loop(f) })
}

// restX is onBattleStart 799-811: the striker's own side of the middle, gap from it.
func restX(x, ox, gap float64) float64 {
	dis := math.Abs(x - ox)
	if ox < x {
		return x - dis/2 + gap
	}
	return x + dis/2 - gap
}

// loop is onBattleLoop 673 + checkWin 536.
func (m *cubiBattles) loop(f *cubiFighter) {
	o := f.vs
	if !m.alive(f) || o == nil || !m.alive(o) {
		return
	}
	m.h.BroadcastStreetLocked(f.street, map[string]any{"t": "cubi_back", "id": f.owner, "to": math.Round(f.restX), "speed": m.t.BackSpeed}, 0)
	f.x = f.restX
	if o.ego <= 0 {
		m.won(f, o)
		return
	}
	line := map[string]any{"t": "cubi_line", "text": fmt.Sprintf(m.t.Text.Ego, m.label(o), o.ego)}
	for _, id := range []int64{f.owner, o.owner} {
		if cl := m.h.ClientLocked(id); cl != nil {
			m.h.SendLocked(cl, line)
		}
	}
	m.h.After(time.Duration(m.t.Timing.TauntMs)*time.Millisecond, func() { m.strike(o) })
}

// won is checkWin 539-571 and makeWinner 645-671. The gain is settled at once (so a winner whose
// owner leaves in the next seconds still keeps it); the bubbles and the trip home keep the source's times.
func (m *cubiBattles) won(w, l *cubiFighter) {
	text := fmt.Sprintf(m.t.Text.Defeated, m.owned(w), m.owned(l))
	l.done, w.done = true, true // neither can be picked up or fought any more
	m.h.After(time.Duration(m.t.Timing.LoserHomeMs)*time.Millisecond, func() { m.home(l, "lost") })
	earned := l.level * m.t.XPPerLoserLevel
	nla := m.nextLevel(w.level)
	feat := m.t.FeatWin
	levelUp := false
	bubble := ""
	if w.xp+earned >= nla && w.level < m.t.LevelMax {
		w.level++
		w.xp = min(m.t.XPMax, w.xp+earned-nla)
		levelUp = true
		feat += m.t.FeatLevel
		bubble = strings.Replace(m.t.Text.LevelUp, "%d", fmt.Sprint(w.level), 1)
	} else {
		w.xp = min(m.t.XPMax, w.xp+earned)
		bubble = strings.Replace(m.t.Text.XP, "%d", fmt.Sprint(earned), 1)
	}
	w.result = map[string]any{"earned": earned, "level_up": levelUp}
	m.h.BroadcastStreetLocked(w.street, map[string]any{"t": "cubi_won", "id": w.owner, "vs": l.owner, "text": text,
		"level": w.level, "xp": w.xp, "earned": earned, "level_up": levelUp}, 0)
	if count, ok := m.h.ProvidedLocked("feats.count").(func(int64, string, string, int64) int64); ok && m.t.Feat != "" {
		count(w.owner, w.ownerName, m.t.Feat, feat)
	}
	m.h.After(time.Duration(m.t.Timing.WinnerMs)*time.Millisecond, func() {
		if m.fighters[w.owner] != w {
			return
		}
		m.say(w, bubble, m.t.Timing.BubbleMs, false)
		m.h.After(time.Duration(m.t.Timing.WinnerHomeMs)*time.Millisecond, func() { m.home(w, "won") })
	})
}

// home is returnToPack 966: the figure leaves the street for its owner's pack.
func (m *cubiBattles) home(f *cubiFighter, why string) {
	if m.fighters[f.owner] != f {
		return
	}
	delete(m.fighters, f.owner)
	f.done = true
	msg := map[string]any{"t": "cubi_home", "id": f.owner, "why": why, "level": f.level, "xp": f.xp, "earned": 0, "level_up": false}
	for k, v := range f.result {
		msg[k] = v
	}
	m.h.BroadcastStreetLocked(f.street, msg, 0)
	// The owner hears it even off the street (it went before the leave), so its pack takes the figure back.
	if cl := m.h.ClientLocked(f.owner); cl != nil && cl.Street() != f.street {
		m.h.SendLocked(cl, msg)
	}
}

func (m *cubiBattles) pickup(cl *Client, _ json.RawMessage) {
	f := m.fighters[cl.ID()]
	// canPickup 525-534: not while it has an opponent, and only by its owner.
	if f == nil || f.vs != nil || f.done {
		m.refuse(cl, "nothing to pick up")
		return
	}
	m.home(f, "picked")
}

// leave sends a player's figure home at once, and calls its fight off (the other goes home too, no winner).
func (m *cubiBattles) leave(id int64) {
	f := m.fighters[id]
	if f == nil {
		return
	}
	o := f.vs
	m.home(f, "left")
	if o != nil && m.fighters[o.owner] == o {
		m.home(o, "left")
	}
}

func (m *cubiBattles) joined(cl *Client) {
	street := cl.Street()
	list := []map[string]any{}
	owners := []int64{}
	for id, f := range m.fighters {
		if f.street == street && !f.done {
			owners = append(owners, id)
		}
	}
	if len(owners) == 0 {
		return // nothing out: nothing to say (the game clears its figures on leaving a street)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	for _, id := range owners {
		list = append(list, m.view(m.fighters[id]))
	}
	m.h.SendLocked(cl, map[string]any{"t": "cubi_fighters", "street": street, "fighters": list})
}
