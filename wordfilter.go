package main

// The word filter (the remake's own addition, Austin 2026-11-02: "include a
// list of bad words to exclude from chat ... turn it into something funny
// like #!@$"). The original Glitch had no swear filter in its server or client
// code. A bad word in a chat line becomes a grawlix of the same length before
// anyone hears it, "fuck" -> "#!@$", the spaces and punctuation around it
// kept; the chat log keeps what was typed and marks the line (chatlog.go).
//
// The list is the game repo's public/js/word-filter-list.js, written here as
// data/word-filter.json by its tools/build-word-filter.mjs (do not edit it by
// hand). The rule is the game's js/word-filter.js, step for step, and the
// site's src/WordFilter.php is the third copy; testdata/word-filter-vectors.json
// (the game's tests/fixtures, copied by the same builder) holds the cases all
// three pass. Only ASCII bytes are looked at (any other byte breaks a word),
// so the byte offsets here give what the game's UTF-16 offsets give.
//
// Where it applies: street chat (hub.go chat), the channels and group chats
// (channels.go channel_say), party chat (party.go party_chat), the Butler's
// tell and announce (butler_im.go), a message left with a Butler
// (homes_help.go butler_leave) and a gift's note (gifts.go). A charades word
// with a bad word is refused (verbs.go). Players' names come from the site,
// which refuses a bad one at sign-up.

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed data/word-filter.json
var wordFilterJSON []byte

// wordFilterSymbols are the grawlix's symbols, taken in turn from the first.
const wordFilterSymbols = "#!@$%&*"

// wordFilterJoinMax is the longest piece a spelled-out word ("f.u.c.k") is joined from.
const wordFilterJoinMax = 2

const wordFilterJoiners = ".-_*'`"

type wordFilterRules struct {
	whole      map[string][][]int
	inside     []wordRuns
	exceptions map[string]bool
}

type wordRuns struct {
	letters string
	counts  []int
}

var wordFilter = mustWordFilter(wordFilterJSON)

func mustWordFilter(raw []byte) *wordFilterRules {
	var list struct {
		Stems      []string `json:"stems"`
		Suffixes   []string `json:"suffixes"`
		Exact      []string `json:"exact"`
		Inside     []string `json:"inside"`
		Exceptions []string `json:"exceptions"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		panic("data/word-filter.json: " + err.Error())
	}
	norm := func(w string) string { return wordForms(lowerASCII(w))[0] }
	r := &wordFilterRules{whole: map[string][][]int{}, exceptions: map[string]bool{}}
	add := func(w string) {
		rr := wordRunsOf(norm(w))
		r.whole[rr.letters] = append(r.whole[rr.letters], rr.counts)
	}
	for _, s := range list.Stems {
		for _, x := range list.Suffixes {
			add(s + x)
		}
	}
	for _, w := range list.Exact {
		add(w)
	}
	for _, w := range list.Inside {
		r.inside = append(r.inside, wordRunsOf(norm(w)))
	}
	for _, w := range list.Exceptions {
		r.exceptions[norm(w)] = true
	}
	return r
}

func isWordLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isWordDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isWordChar(c byte) bool {
	return isWordLetter(c) || isWordDigit(c) || c == '@' || c == '$' || c == '!'
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func wordSwap(c byte) byte {
	switch c {
	case '0':
		return 'o'
	case '3':
		return 'e'
	case '4':
		return 'a'
	case '5':
		return 's'
	case '7':
		return 't'
	case '@':
		return 'a'
	case '$':
		return 's'
	case '!':
		return 'i'
	case 'v':
		return 'u'
	}
	return c
}

// wordForms is rule 4: the swaps, with 1 once as i and once as l.
func wordForms(w string) []string {
	mapped := func(one byte) string {
		b := make([]byte, len(w))
		for i := 0; i < len(w); i++ {
			if w[i] == '1' {
				b[i] = one
			} else {
				b[i] = wordSwap(w[i])
			}
		}
		return string(b)
	}
	if strings.IndexByte(w, '1') >= 0 {
		return []string{mapped('i'), mapped('l')}
	}
	return []string{mapped('i')}
}

// wordRunsOf is rule 5's runs of one letter.
func wordRunsOf(w string) wordRuns {
	var letters []byte
	var counts []int
	for i := 0; i < len(w); i++ {
		if n := len(letters); n > 0 && letters[n-1] == w[i] {
			counts[n-1]++
		} else {
			letters = append(letters, w[i])
			counts = append(counts, 1)
		}
	}
	return wordRuns{string(letters), counts}
}

func atLeast(have, need []int, at int) bool {
	for k, n := range need {
		if have[at+k] < n {
			return false
		}
	}
	return true
}

func (r *wordFilterRules) badForm(f string) bool {
	rr := wordRunsOf(f)
	for _, need := range r.whole[rr.letters] {
		if atLeast(rr.counts, need, 0) {
			return true
		}
	}
	for _, in := range r.inside {
		for from := 0; ; {
			k := strings.Index(rr.letters[from:], in.letters)
			if k < 0 {
				break
			}
			if atLeast(rr.counts, in.counts, from+k) {
				return true
			}
			from += k + 1
		}
	}
	return false
}

// isBadWord is rules 3-5 for one word (its ! already trimmed).
func isBadWord(w string) bool {
	letters, digits := 0, 0
	for i := 0; i < len(w); i++ {
		if isWordLetter(w[i]) {
			letters++
		} else if isWordDigit(w[i]) {
			digits++
		}
	}
	if letters <= digits {
		return false
	}
	fs := wordForms(lowerASCII(w))
	for _, f := range fs {
		if wordFilter.exceptions[f] {
			return false
		}
	}
	for _, f := range fs {
		if wordFilter.badForm(f) {
			return true
		}
	}
	return false
}

// badSpans is rules 1-3: [start, end) byte offsets of every bad word in s, in order.
func badSpans(s string) [][2]int {
	var spans [][2]int
	trimmed := func(idx []int) (int, int, bool) {
		a, b := 0, len(idx)
		for a < b && s[idx[a]] == '!' {
			a++
		}
		for b > a && s[idx[b-1]] == '!' {
			b--
		}
		if a >= b {
			return 0, 0, false
		}
		w := make([]byte, 0, b-a)
		for _, k := range idx[a:b] {
			w = append(w, s[k])
		}
		if !isBadWord(string(w)) {
			return 0, 0, false
		}
		return idx[a], idx[b-1] + 1, true
	}
	span := func(a, b int) []int {
		idx := make([]int, 0, b-a)
		for k := a; k < b; k++ {
			idx = append(idx, k)
		}
		return idx
	}
	n := len(s)
	for i := 0; i < n; {
		if !isWordChar(s[i]) {
			i++
			continue
		}
		var pieces [][2]int
		j := i
		for {
			start := j
			for j < n && isWordChar(s[j]) {
				j++
			}
			pieces = append(pieces, [2]int{start, j})
			if j+1 < n && strings.IndexByte(wordFilterJoiners, s[j]) >= 0 && isWordChar(s[j+1]) {
				j++
			} else {
				break
			}
		}
		joined := false
		if len(pieces) > 1 {
			short := true
			var idx []int
			for _, p := range pieces {
				if p[1]-p[0] > wordFilterJoinMax {
					short = false
				}
				idx = append(idx, span(p[0], p[1])...)
			}
			if short {
				if a, b, ok := trimmed(idx); ok {
					spans = append(spans, [2]int{a, b})
					joined = true
				}
			}
		}
		if !joined {
			for _, p := range pieces {
				if a, b, ok := trimmed(span(p[0], p[1])); ok {
					spans = append(spans, [2]int{a, b})
				}
			}
		}
		i = j
	}
	return spans
}

// grawlix is n of the symbols, in turn from the first.
func grawlix(n int) string {
	b := make([]byte, n)
	for k := range b {
		b[k] = wordFilterSymbols[k%len(wordFilterSymbols)]
	}
	return string(b)
}

// filterWords is the text with every bad word turned into a grawlix; anything else as it was.
func filterWords(s string) string {
	spans := badSpans(s)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		b.WriteString(s[at:sp[0]])
		b.WriteString(grawlix(sp[1] - sp[0]))
		at = sp[1]
	}
	b.WriteString(s[at:])
	return b.String()
}

// hasBadWord says whether the text has a bad word (a charades word with one is refused).
func hasBadWord(s string) bool { return len(badSpans(s)) > 0 }
