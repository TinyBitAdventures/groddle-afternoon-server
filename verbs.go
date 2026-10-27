package main

// What one player does to another (Track M, lane VERBS): the item
// verbs of the original that need a real player at the other end.
//
//   - The player menu's emotes (main.js:3547-3580 lists every is_emote verb of
//     the pack's items when the target is online and on your street, and
//     neither of you blocks the other): the Emotional Bear's Hug, High Five,
//     Kiss and Moon (items/emotional_bear.js 54-400) and the Plank's Splank
//     (items/plank.js 30-89).
//   - Two pack verbs that ask whom (requires_target_pc, not emotes): the
//     Watering Can's Soak (items/watering_can.js 273-332) and a Musicblock's
//     Play for (musicblock_*.js verbs.play_for 94-252).
//   - Charades (items/potion_charades.js 511-552, catalog_buffs.js charades
//     400-426, main.js 1766-1793, location.js 889-892): the drinker picks a
//     secret word the server keeps; the first other player on the street to
//     say it in chat wins 50 iMG and 250 currants.
//   - Epic Blowout (locations/home.js 197-218): a house party, a player's
//     home (HOME_<id> or HOUSE_<id>, a POL that is not public) with 12
//     players or more, 11 of them guests under the influence (Buzzed, Smashed,
//     Purple Junk or No-No Powder). It is checked when someone comes in while
//     the owner is there (home.js:55-67) and when someone there gets one of
//     those buffs (inc_buffs.js:199-201).
//
// The server decides who did what to whom: the target is a real player, on
// your street, online, within reach, neither of you blocking the other. Each
// side's game applies its own side once from the server's word, the amounts
// fixed by the verb (js/player-verbs-mp.js holds them, cited), never by
// the doer's claim. Counters (Hanky Splanky, Super Soaker, Blocks That Rock,
// Tickle Me Emo, Rising Moon, the garlic kisses) count only a verb_done.
//
// Client to server:
//
//	{"t":"verb_do","verb":"hug|high_five|kiss|moon|splank|soak|play_for","to":9,"ref":"…","block":"musicblock_d_green_01","garlic":false}
//	{"t":"charades_start","word":"…"}            the word the drinker chose (cleaned here)
//	{"t":"verbs_influence","on":true}             one of the four party buffs is on (or none is)
//
// Server to client:
//
//	{"t":"verb_done","verb":"hug","ref":"…","status":"ok|refused","text":"…","to":{"id":9,"name":"Bo"},"id":"v-…","block":"…","garlic":false}
//	{"t":"verb_got","verb":"hug","id":"v-…","from":{"id":7,"name":"Ada"},"block":"…","garlic":false}
//	{"t":"charades_started","status":"ok|refused","text":"…","word":"…","ends":1730000000000}   to the drinker
//	{"t":"charades_on","from":{"id":7,"name":"Ada"},"ends":…,"text":"Ada is playing Charades. …"}   to the street (and to a joiner)
//	{"t":"charades_end","why":"guessed|ruined|timeout|left","word":"…","winner":{…}}            to the drinker
//	{"t":"charades_over","from":{…},"why":"…","winner":{…}|null,"text":"…"}                   to the rest of the street
//	{"t":"charades_won","from":{…},"xp":50,"currants":250,"text":"…"}                          to the winner
//	{"t":"party_blowout","owner":7}                                                           to each player at the party
//
// Rules kept from the source:
//   - Not yourself ("You can't do that to yourself.", main.js:3166), not a
//     player who is not there (3177) or offline (3182), not one who blocks you
//     ("That player has blocked you.", 3189); within verb_radius 800 px
//     (config_prod.js:37; "That player is too far away. Try moving a little
//     closer.", main.js:3217). Play for asks the street's own players
//     ("Hey! Where'd they go?", musicblock_*.js 113-117).
//   - No emotes in a race ("You're in a race! Save your emotional outbursts
//     for later.", emotional_bear.js 66; "That would be unsportspersonlike of
//     you.", plank.js 44): RACE_ and CROWNS_ streets.
//   - Charades needs someone else on the street ("You need other people around
//     to play Charades. Trying to play with yourself is grossly
//     inappropriate.", potion_charades.js:512), one game at a time ("You are
//     already playing charades.", 514); the word is the input's first 32
//     characters, a-z, 0-9, space and dash, trimmed, lower case (520); an
//     empty word keeps the potion (536). The game lasts 120 s (the buff); a
//     chat line containing the word wins it for its speaker (indexOf, main.js
//     1769), or ruins it when the drinker says it (1770-1775); leaving the
//     street ends it (location.js:889-892, then on_remove's "Nobody guessed").
//   - Epic Blowout: 12 players and 11 influenced guests (home.js:198, 213);
//     the owner may be influenced but is not counted.
//
// Ours: one verb a second per player, a burst of four (the source's energy
// cost was the only brake; the energy is the client's here); the word is
// never sent to anyone but its drinker; the 250 currants are a signed credit
// (credits.go, why "charades") so the winner's next save may show them; a
// player is told of a blowout once per stay at that home; whether a player
// is under the influence is that player's game's word (verbs_influence),
// acceptable for a badge that moves nothing of value. GRODDLE_VERBS_DEV=1
// uses the source's config.is_dev thresholds (3 players, 3 influenced,
// home.js:198, 213) so four players can see a blowout in QA.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	verbRadius     = 800 // config_prod.js:37 verb_radius
	verbBurst      = 4
	verbEvery      = time.Second
	charadesFor    = 120 * time.Second // catalog_buffs.js charades duration 120
	charadesWordN  = 32                // potion_charades.js:520 substr(0, 32)
	charadesXP     = 50                // main.js:1781
	charadesPrize  = 250               // main.js:1782
	blowoutPlayers = 12                // home.js:198 activePlayers.length >= 12
	blowoutHigh    = 11                // home.js:213 drugged >= 11
	blowoutDevN    = 3                 // home.js:198, 213 config.is_dev

	verbTextSelf      = "You can't do that to yourself."                                                                        // main.js:3166
	verbTextNotThere  = "You can't do that to a player that isn't there!"                                                       // main.js:3177
	verbTextOffline   = "That player has gone offline."                                                                         // main.js:3182
	verbTextBlockedBy = "That player has blocked you."                                                                          // main.js:3189
	verbTextFar       = "That player is too far away. Try moving a little closer."                                              // main.js:3217
	verbTextGone      = "Hey! Where'd they go?"                                                                                 // musicblock_*.js:115
	verbTextBearRace  = "You're in a race! Save your emotional outbursts for later."                                            // emotional_bear.js:66
	verbTextPlankRace = "That would be unsportspersonlike of you."                                                              // plank.js:44
	verbTextSoakSelf  = "You can't soak yourself!"                                                                              // watering_can.js:303
	verbTextSlow      = "Slow down a little."                                                                                   // ours (hub.go's chat words)
	verbTextBad       = "That is not something you can do to another player."                                                   // ours
	charadesTextAlone = "You need other people around to play Charades. Trying to play with yourself is grossly inappropriate." // potion_charades.js:512
	charadesTextAgain = "You are already playing charades."                                                                     // potion_charades.js:514
	charadesTextWhere = "You need to be on a street to play Charades."                                                          // ours
)

var (
	verbRefRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	verbBlockRe = regexp.MustCompile(`^musicblock_[a-z0-9_]{1,40}$`)
	charadesBad = regexp.MustCompile(`(?i)[^a-z0-9 -]`)
)

// verbRule is one verb: an emote of the player menu (race says why it is off in a race) or a pack verb.
type verbRule struct {
	race string
}

var verbRules = map[string]verbRule{
	"hug":       {race: verbTextBearRace},
	"high_five": {race: verbTextBearRace},
	"kiss":      {race: verbTextBearRace},
	"moon":      {race: verbTextBearRace},
	"splank":    {race: verbTextPlankRace},
	"soak":      {},
	"play_for":  {},
}

// The four buffs home.js:207 counts as under the influence (js/player-verbs-mp.js INFLUENCE_BUFFS).

func init() {
	modules = append(modules, newVerbs)
}

type charadesGame struct {
	street string
	word   string
	ends   time.Time
	stop   func() bool
}

type verbs struct {
	h     *Hub
	boot  string
	seq   uint64
	dev   bool
	rates map[int64]*bucket
	games map[int64]*charadesGame // drinker -> game
	high  map[int64]bool          // players under the influence (their game's word)
	blown map[string]map[int64]bool
}

func newVerbs(h *Hub) Module {
	var b [4]byte
	rand.Read(b[:])
	return &verbs{h: h, boot: hex.EncodeToString(b[:]), dev: os.Getenv("GRODDLE_VERBS_DEV") == "1",
		rates: map[int64]*bucket{}, games: map[int64]*charadesGame{}, high: map[int64]bool{}, blown: map[string]map[int64]bool{}}
}

func (m *verbs) Register(r *Registry) {
	r.Handle("verb_do", m.do)
	r.Handle("charades_start", m.charadesStart)
	r.Handle("verbs_influence", m.influence)
	r.OnChat(m.chat)
	r.OnJoin(m.joined)
	r.OnLeave(m.left)
	r.OnDisconnect(func(cl *Client) {
		delete(m.rates, cl.ID())
		delete(m.high, cl.ID())
	})
}

func verbWho(cl *Client) map[string]any { return map[string]any{"id": cl.ID(), "name": cl.Name()} }

func verbRaceStreet(street string) bool {
	return strings.HasPrefix(street, "RACE_") || strings.HasPrefix(street, "CROWNS_")
}

// —— Verbs on another player ——

func (m *verbs) do(cl *Client, frame json.RawMessage) {
	var msg struct {
		Verb   string `json:"verb"`
		To     int64  `json:"to"`
		Ref    string `json:"ref"`
		Block  string `json:"block"`
		Garlic bool   `json:"garlic"`
	}
	_ = json.Unmarshal(frame, &msg)
	ref := msg.Ref
	if !verbRefRe.MatchString(ref) {
		ref = ""
	}
	refuse := func(text string) {
		m.h.SendLocked(cl, map[string]any{"t": "verb_done", "verb": msg.Verb, "ref": ref, "status": "refused", "text": text})
	}
	rule, known := verbRules[msg.Verb]
	if !known || msg.To <= 0 || cl.Street() == "" {
		msg.Verb = ""
		refuse(verbTextBad)
		return
	}
	if msg.Verb == "play_for" && !verbBlockRe.MatchString(msg.Block) {
		refuse(verbTextBad)
		return
	}
	if msg.Verb != "play_for" {
		msg.Block = ""
	}
	if msg.Verb != "kiss" {
		msg.Garlic = false
	}
	rb := m.rates[cl.ID()]
	if rb == nil {
		rb = &bucket{}
		m.rates[cl.ID()] = rb
	}
	if !rb.take(m.h.Now(), verbBurst, verbEvery) {
		refuse(verbTextSlow)
		return
	}
	if msg.To == cl.ID() {
		if msg.Verb == "soak" {
			refuse(verbTextSoakSelf)
		} else {
			refuse(verbTextSelf)
		}
		return
	}
	if rule.race != "" && verbRaceStreet(cl.Street()) {
		refuse(rule.race)
		return
	}
	t := m.h.ClientLocked(msg.To)
	switch {
	case t == nil || !t.Online():
		if msg.Verb == "play_for" {
			refuse(verbTextGone)
		} else {
			refuse(verbTextOffline)
		}
		return
	case t.Street() != cl.Street():
		if msg.Verb == "play_for" {
			refuse(verbTextGone)
		} else {
			refuse(verbTextNotThere)
		}
		return
	}
	if ign, ok := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool); ok {
		if ign(t.ID(), cl.ID()) {
			refuse(verbTextBlockedBy)
			return
		}
		if ign(cl.ID(), t.ID()) {
			refuse("You are blocking " + t.Name() + ".") // main.js:3569
			return
		}
	}
	ax, ay := cl.Pos()
	tx, ty := t.Pos()
	if math.Hypot(ax-tx, ay-ty) > verbRadius {
		refuse(verbTextFar)
		return
	}
	m.seq++
	id := "v-" + m.boot + "-" + strconv.FormatUint(m.seq, 36)
	got := map[string]any{"t": "verb_got", "verb": msg.Verb, "id": id, "from": verbWho(cl)}
	done := map[string]any{"t": "verb_done", "verb": msg.Verb, "ref": ref, "status": "ok", "id": id, "to": verbWho(t)}
	if msg.Block != "" {
		got["block"], done["block"] = msg.Block, msg.Block
	}
	if msg.Verb == "kiss" {
		got["garlic"], done["garlic"] = msg.Garlic, msg.Garlic
	}
	m.h.SendLocked(t, got)
	m.h.SendLocked(cl, done)
	log.Printf("verbs: %d %s %d on %s (%s)", cl.ID(), msg.Verb, t.ID(), cl.Street(), id)
}

// —— Charades ——

// cleanCharadesWord is potion_charades.js:520: the first 32 characters, a-z 0-9 space and dash, trimmed, lower case.
func cleanCharadesWord(s string) string {
	r := []rune(s)
	if len(r) > charadesWordN {
		r = r[:charadesWordN]
	}
	return strings.ToLower(strings.TrimSpace(charadesBad.ReplaceAllString(string(r), "")))
}

func (m *verbs) charadesStart(cl *Client, frame json.RawMessage) {
	var msg struct {
		Word string `json:"word"`
	}
	_ = json.Unmarshal(frame, &msg)
	refuse := func(why, text string) {
		m.h.SendLocked(cl, map[string]any{"t": "charades_started", "status": "refused", "why": why, "text": text})
	}
	street := cl.Street()
	if street == "" {
		refuse("where", charadesTextWhere)
		return
	}
	if len(m.h.StreetLocked(street)) < 2 {
		refuse("alone", charadesTextAlone)
		return
	}
	if m.games[cl.ID()] != nil {
		refuse("again", charadesTextAgain)
		return
	}
	word := cleanCharadesWord(msg.Word)
	if word == "" {
		// potion_charades.js:536: no word, no game; the potion stays.
		refuse("empty", "")
		return
	}
	g := &charadesGame{street: street, word: word, ends: m.h.Now().Add(charadesFor)}
	id := cl.ID()
	g.stop = m.h.After(charadesFor, func() {
		if m.games[id] == g {
			m.endCharades(id, g, "timeout", nil)
		}
	})
	m.games[id] = g
	m.h.SendLocked(cl, map[string]any{"t": "charades_started", "status": "ok", "word": word, "ends": g.ends.UnixMilli()})
	m.h.BroadcastStreetLocked(street, m.charadesOn(cl, g), id)
	log.Printf("verbs: %d plays charades on %s", id, street)
}

func (m *verbs) charadesOn(cl *Client, g *charadesGame) map[string]any {
	// potion_charades.js:528
	return map[string]any{"t": "charades_on", "from": verbWho(cl), "ends": g.ends.UnixMilli(), "text": cl.Name() + " is playing Charades. Guess their secret word to win a prize!"}
}

// endCharades ends one game: why guessed (winner set), ruined, timeout or left.
func (m *verbs) endCharades(id int64, g *charadesGame, why string, winner *Client) {
	delete(m.games, id)
	if g.stop != nil {
		g.stop()
	}
	cl := m.h.ClientLocked(id)
	name := "Somebody"
	from := map[string]any{"id": id, "name": name}
	if cl != nil {
		name = cl.Name()
		from = verbWho(cl)
	}
	end := map[string]any{"t": "charades_end", "why": why, "word": g.word}
	over := map[string]any{"t": "charades_over", "from": from, "why": why, "winner": nil}
	switch why {
	case "guessed":
		end["winner"] = verbWho(winner)
		over["winner"] = verbWho(winner)
		over["text"] = winner.Name() + " guessed correctly! +50 iMG, +250 currants" // main.js:1780
	case "ruined":
		over["text"] = name + " ruined their own game of charades by saying the answer." // main.js:1771
	default:
		// catalog_buffs.js:416 on_remove, when nobody guessed (also after leaving, location.js:889-892).
		over["text"] = name + "'s game of charades is over. Nobody guessed '" + g.word + "'."
	}
	if cl != nil && cl.Online() {
		m.h.SendLocked(cl, end)
	}
	for _, o := range m.h.StreetLocked(g.street) {
		if o.ID() != id && (winner == nil || o.ID() != winner.ID()) {
			m.h.SendLocked(o, over)
		}
	}
	if winner != nil {
		m.h.SendLocked(winner, map[string]any{"t": "charades_won", "from": from, "xp": charadesXP, "currants": charadesPrize, "text": winner.Name() + " guessed correctly! +50 iMG, +250 currants"})
		creditLocked(m.h, winner.ID(), "charades", tradeGoods{Currants: charadesPrize})
	}
	log.Printf("verbs: %d charades %s on %s", id, why, g.street)
}

// chat is main.js:1766-1793, after the line is said: every game on the street whose word the line contains.
func (m *verbs) chat(cl *Client, text string) {
	street := cl.Street()
	if street == "" || len(m.games) == 0 {
		return
	}
	lower := strings.ToLower(text)
	for _, o := range m.h.StreetLocked(street) {
		g := m.games[o.ID()]
		if g == nil || g.street != street || !strings.Contains(lower, g.word) {
			continue
		}
		if o.ID() == cl.ID() {
			m.endCharades(o.ID(), g, "ruined", nil)
		} else {
			m.endCharades(o.ID(), g, "guessed", cl)
		}
	}
}

// —— Joining and leaving ——

func (m *verbs) joined(cl *Client) {
	street := cl.Street()
	for id, g := range m.games {
		if g.street == street && id != cl.ID() {
			if o := m.h.ClientLocked(id); o != nil {
				m.h.SendLocked(cl, m.charadesOn(o, g))
			}
		}
	}
	// home.js:55-67: someone came in while the owner is there.
	if owner, _ := homeOwner(street); owner > 0 {
		if o := m.h.ClientLocked(owner); o != nil && o.Street() == street {
			m.blowout(street)
		}
	}
}

func (m *verbs) left(cl *Client, street string) {
	if g := m.games[cl.ID()]; g != nil && g.street == street {
		m.endCharades(cl.ID(), g, "left", nil)
	}
	if b := m.blown[street]; b != nil {
		delete(b, cl.ID())
		if len(b) == 0 {
			delete(m.blown, street)
		}
	}
}

// —— Epic Blowout ——

func (m *verbs) influence(cl *Client, frame json.RawMessage) {
	var msg struct {
		On bool `json:"on"`
	}
	_ = json.Unmarshal(frame, &msg)
	was := m.high[cl.ID()]
	if msg.On {
		m.high[cl.ID()] = true
	} else {
		delete(m.high, cl.ID())
	}
	// inc_buffs.js:199-201: a buff applied in a home that is not public.
	if msg.On && !was {
		if owner, _ := homeOwner(cl.Street()); owner > 0 {
			m.blowout(cl.Street())
		}
	}
}

// blowout is home.js checkEpicBlowout (197-218) on a home street.
func (m *verbs) blowout(street string) {
	owner, _ := homeOwner(street)
	if owner <= 0 {
		return
	}
	need, high := blowoutPlayers, blowoutHigh
	if m.dev {
		need, high = blowoutDevN, blowoutDevN
	}
	here := m.h.StreetLocked(street)
	if len(here) < need {
		return
	}
	n := 0
	for _, o := range here {
		if o.ID() != owner && m.high[o.ID()] {
			n++
		}
	}
	if n < high {
		return
	}
	b := m.blown[street]
	if b == nil {
		b = map[int64]bool{}
		m.blown[street] = b
	}
	for _, o := range here {
		if !b[o.ID()] {
			b[o.ID()] = true
			m.h.SendLocked(o, map[string]any{"t": "party_blowout", "owner": owner})
		}
	}
	log.Printf("verbs: epic blowout at %s (%d players, %d influenced)", street, len(here), n)
}
