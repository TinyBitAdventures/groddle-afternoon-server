package main

// A copy's own words on the way between players (2026-11-04): a Cubimal's name, a Draught of Giant
// Amicability's Giant, a Still's hopper and Hooch, a lit pumpkin's phrases, a Teleportation Script's
// destination. The game keeps them beside
// each copy (js/tools.js row.tags) and sends them as an item's "t", beside its wear "r", on a
// one-copy stack: trades, gifts (a Gift Box's contents too) and shared ground drops. Each is checked
// here before it is kept or passed on, by the rules the game writes into data/trade.json ("tags", and
// each class's "tag" kind), the same rules as the game's js/copy-tags.js and the site's
// src/CopyTags.php (the three agree on tests/fixtures/copy-tag-vectors.json in the game repo):
//
//   - name: the characters a Cubimal name keeps (letters, digits, spaces and the word filter's
//     symbols), at most max of them, the word filter, spaces trimmed (npc_cubimal_chick.js
//     onInputBoxResponse 41). A figure that has won a battle (G40, cubimal_battle.go) keeps
//     {"name","level","xp"} (its instance props level and xp): the name as above ("" for none),
//     level a whole number from 1 to levelMax, xp from 0 to xpMax; at level 1 with no xp it is
//     just the name again.
//   - phrases: at most max; each one line with no control characters or angle brackets, spaces
//     trimmed, at most len characters, the word filter, trimmed; empty ones dropped (lit_pumpkin_base).
//     U+FFFD (what an invalid character becomes) is dropped too.
//   - giant: one of the giants (potion_favor.js config.giants).
//   - still: {"hopper","hooch","workingSince"} whole numbers, at most hopper and hooch, and a start
//     no later than now (still.js 30-31).
//   - script: a Teleportation Script's {"tsid","x","y","label","hubId","hubName","imbued"}
//     (teleportation_script.js 298-319): tsid and hubId letters, digits, _ and -, at most id; x and y
//     whole numbers within coord (null when not known); label and hubName as one phrase of len. No
//     tsid is no words.
//
// Words a class does not keep, words on a stack of more than one, or words that come out empty are
// dropped: the copy goes without them, as before. Only the side giving a copy sends its words; a
// pickup takes the stack's own (drops.go), never a client's.

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// copyTagMax is the most bytes one copy's words may take once checked.
const copyTagMax = 2048

type copyTagRules struct {
	Name struct {
		Max      int   `json:"max"`
		LevelMax int64 `json:"levelMax"`
		XPMax    int64 `json:"xpMax"`
	} `json:"name"`
	Phrases struct {
		Max int `json:"max"`
		Len int `json:"len"`
	} `json:"phrases"`
	Giant struct {
		Giants []string `json:"giants"`
	} `json:"giant"`
	Still struct {
		Hopper int64 `json:"hopper"`
		Hooch  int64 `json:"hooch"`
	} `json:"still"`
	Script struct {
		ID    int   `json:"id"`
		Len   int   `json:"len"`
		Coord int64 `json:"coord"`
	} `json:"script"`
}

type cubimalTag struct {
	Name  string `json:"name"`
	Level int64  `json:"level"`
	XP    int64  `json:"xp"`
}

type stillTag struct {
	Hopper       int64 `json:"hopper"`
	Hooch        int64 `json:"hooch"`
	WorkingSince int64 `json:"workingSince"`
}

type scriptTag struct {
	Tsid    string `json:"tsid"`
	X       *int64 `json:"x"`
	Y       *int64 `json:"y"`
	Label   string `json:"label"`
	HubID   string `json:"hubId"`
	HubName string `json:"hubName"`
	Imbued  bool   `json:"imbued"`
}

// cleanTag checks an item's words in place: kept in their checked form, or dropped.
func (t *tradeTable) cleanTag(it *tradeItem, now time.Time) {
	if len(it.T) == 0 {
		it.T = nil
		return
	}
	kind := t.Items[it.C].Tag
	if kind == "" || it.N != 1 {
		it.T = nil
		return
	}
	it.T = t.Tags.clean(kind, it.T, now)
}

// clean is the checked words of kind, as JSON, or nil.
func (r *copyTagRules) clean(kind string, raw json.RawMessage, now time.Time) json.RawMessage {
	var out any
	switch kind {
	case "name":
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if v := cleanCopyName(s, r.Name.Max); v != "" {
				out = v
			}
			break
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m == nil {
			return nil
		}
		name := ""
		if json.Unmarshal(m["name"], &s) == nil {
			name = cleanCopyName(s, r.Name.Max)
		}
		v := cubimalTag{Name: name, Level: max(1, copyTagWhole(m["level"], r.Name.LevelMax)), XP: copyTagWhole(m["xp"], r.Name.XPMax)}
		if v.Level > 1 || v.XP > 0 {
			out = v
		} else if name != "" {
			out = name
		}
	case "phrases":
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			return nil
		}
		if len(list) > r.Phrases.Max {
			list = list[:r.Phrases.Max]
		}
		var kept []string
		for _, p := range list {
			var s string
			if json.Unmarshal(p, &s) != nil {
				continue
			}
			if v := cleanCopyPhrase(s, r.Phrases.Len); v != "" {
				kept = append(kept, v)
			}
		}
		if len(kept) > 0 {
			out = kept
		}
	case "giant":
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		for _, g := range r.Giant.Giants {
			if g == s {
				out = s
			}
		}
	case "still":
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m == nil {
			return nil
		}
		v := stillTag{
			Hopper:       copyTagWhole(m["hopper"], r.Still.Hopper),
			Hooch:        copyTagWhole(m["hooch"], r.Still.Hooch),
			WorkingSince: copyTagWhole(m["workingSince"], now.UnixMilli()),
		}
		if v.Hopper > 0 || v.Hooch > 0 || v.WorkingSince > 0 {
			out = v
		}
	case "script":
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m == nil {
			return nil
		}
		tsid := scriptID(m["tsid"], r.Script.ID)
		if tsid == "" {
			return nil
		}
		var imbued bool
		if json.Unmarshal(m["imbued"], &imbued) != nil {
			imbued = false
		}
		out = scriptTag{
			Tsid:    tsid,
			X:       scriptCoord(m["x"], r.Script.Coord),
			Y:       scriptCoord(m["y"], r.Script.Coord),
			Label:   scriptText(m["label"], r.Script.Len),
			HubID:   scriptID(m["hubId"], r.Script.ID),
			HubName: scriptText(m["hubName"], r.Script.Len),
			Imbued:  imbued,
		}
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil || len(b) > copyTagMax {
		return nil
	}
	return b
}

// copyTagWhole is a JSON number as a whole number from 0 to max (anything else is 0).
func copyTagWhole(raw json.RawMessage, max int64) int64 {
	var f float64
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f != f || f <= 0 {
		return 0
	}
	if f >= float64(max) {
		return max
	}
	n := int64(f)
	if n < 0 {
		return 0
	}
	return n
}

// scriptID is a JSON string of letters, digits, _ and -, at most max ("" for anything else).
func scriptID(raw json.RawMessage, max int) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || s == "" || len(s) > max {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c == '-' || isWordLetter(c) || isWordDigit(c)) {
			return ""
		}
	}
	return s
}

// scriptCoord is a JSON number as a whole number within max, toward zero (nil for anything else).
func scriptCoord(raw json.RawMessage, max int64) *int64 {
	var f *float64
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f == nil || *f != *f {
		return nil
	}
	var n int64
	switch {
	case *f >= float64(max):
		n = max
	case *f <= -float64(max):
		n = -max
	default:
		n = int64(*f)
	}
	return &n
}

// scriptText is a JSON string as one phrase ("" for anything else).
func scriptText(raw json.RawMessage, n int) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return cleanCopyPhrase(s, n)
}

// cleanCopyName keeps a Cubimal name's characters, at most max of them, then the word filter.
func cleanCopyName(s string, max int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || isWordLetter(c) || isWordDigit(c) || strings.IndexByte(wordFilterSymbols, c) >= 0 {
			b.WriteByte(c)
		}
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
	}
	return strings.Trim(filterWords(out), " ")
}

// cleanCopyPhrase is one pumpkin phrase: one line, no control characters or angle brackets, at most n characters.
func cleanCopyPhrase(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError || r == '<' || r == '>':
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || (r >= 0x7f && r < 0xa0) || r == 0x2028 || r == 0x2029:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " ")
	if utf8.RuneCountInString(out) > n {
		out = string([]rune(out)[:n])
	}
	return strings.Trim(filterWords(out), " ")
}
