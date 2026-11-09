package main

// Moderation signals with no words in them (Austin 2026-11-09: IM abuse is caught from
// content-free signals; no moderator ever reads an IM, 2026-11-09). Per
// sender, this server counts what it sees anyway and keeps no text:
//
//   - im_fanout: IMs to many different players in a short window (spam goes wide);
//   - im_refused: IMs refused because the recipient has too many unread from them ("full")
//     or for the rate ("slow_down"): someone writing faster than anyone answers;
//   - im_filtered: IMs the word filter changed (a count; the words are filtered before they
//     are kept or sent, wordfilter.go, and never stored as typed);
//   - chat_refused: channel and Local lines refused for the rate or a silence (the chat log's
//     own "slow_down" and "silenced").
//
// Past a threshold (ours, below, chosen for spam: a busy honest player stays under them), one
// flag goes to the site's moderation queue through the chat store's signed batches
// (chatstore.go, {"k":"flag","id":7,"name":"Ada","signal":"im_fanout","n":9,"window":600}),
// at most once a signal an hour for one player. The site adds what it knows (how new the
// account is, how often it was blocked lately: groddle-afternoon-site src/Moderation.php) and
// a moderator decides; nothing here acts on a flag. The counts are memory only.

import "time"

const (
	// IMs to this many different players within modFanoutWindow raise im_fanout.
	modFanoutFlag   = 8
	modFanoutWindow = 10 * time.Minute
	// This many IMs refused ("full" or "slow_down") within modRefusedWindow raise im_refused.
	modRefusedFlag   = 10
	modRefusedWindow = 10 * time.Minute
	// This many IMs the word filter changed within modFilteredWindow raise im_filtered.
	modFilteredFlag   = 5
	modFilteredWindow = time.Hour
	// This many channel or Local lines refused within modChatWindow raise chat_refused.
	modChatFlag   = 12
	modChatWindow = 10 * time.Minute
	// A signal flags one player at most this often.
	modFlagEvery = time.Hour
)

type modSignals struct {
	imTo     map[int64]time.Time // recipient -> the last IM to them
	refused  []time.Time
	filtered []time.Time
	chat     []time.Time
	flagged  map[string]time.Time
}

func (m *moderation) signalsOf(id int64) *modSignals {
	if m.signals == nil {
		m.signals = map[int64]*modSignals{}
	}
	s := m.signals[id]
	if s == nil {
		s = &modSignals{imTo: map[int64]time.Time{}, flagged: map[string]time.Time{}}
		m.signals[id] = s
	}
	return s
}

// within keeps the times inside window and adds now.
func within(list []time.Time, now time.Time, window time.Duration) []time.Time {
	kept := list[:0]
	for _, t := range list {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	return append(kept, now)
}

// flag sends one flag to the site unless this signal flagged the player within modFlagEvery.
func (m *moderation) flag(id int64, name, signal string, n int, window time.Duration) {
	s := m.signalsOf(id)
	now := m.h.now()
	if at, ok := s.flagged[signal]; ok && now.Sub(at) < modFlagEvery {
		return
	}
	s.flagged[signal] = now
	m.h.storeChatLocked(chatEntry{K: "flag", ID: id, Name: name, Signal: signal, N: n, Window: int(window / time.Second)})
}

// signalIMSentLocked counts an IM that went: who to, and whether the word filter changed it.
func (h *Hub) signalIMSentLocked(from int64, name string, to int64, filtered bool) {
	m := h.moderationOf()
	if m == nil {
		return
	}
	s := m.signalsOf(from)
	now := h.now()
	s.imTo[to] = now
	n := 0
	for who, at := range s.imTo {
		if now.Sub(at) < modFanoutWindow {
			n++
		} else {
			delete(s.imTo, who)
		}
	}
	if n >= modFanoutFlag {
		m.flag(from, name, "im_fanout", n, modFanoutWindow)
	}
	if filtered {
		s.filtered = within(s.filtered, now, modFilteredWindow)
		if len(s.filtered) >= modFilteredFlag {
			m.flag(from, name, "im_filtered", len(s.filtered), modFilteredWindow)
		}
	}
}

// signalIMRefusedLocked counts an IM refused as "full" or "slow_down".
func (h *Hub) signalIMRefusedLocked(from int64, name string) {
	m := h.moderationOf()
	if m == nil {
		return
	}
	s := m.signalsOf(from)
	s.refused = within(s.refused, h.now(), modRefusedWindow)
	if len(s.refused) >= modRefusedFlag {
		m.flag(from, name, "im_refused", len(s.refused), modRefusedWindow)
	}
}

// signalChatRefusedLocked counts a channel or Local line refused (the chat log's why).
func (h *Hub) signalChatRefusedLocked(id int64, name string) {
	m := h.moderationOf()
	if m == nil || id <= 0 {
		return
	}
	s := m.signalsOf(id)
	s.chat = within(s.chat, h.now(), modChatWindow)
	if len(s.chat) >= modChatFlag {
		m.flag(id, name, "chat_refused", len(s.chat), modChatWindow)
	}
}

// forgetSignalsLocked drops a player's counts once they have none worth keeping (a disconnect).
func (m *moderation) forgetSignals(id int64) {
	s := m.signals[id]
	if s == nil {
		return
	}
	now := m.h.now()
	for _, at := range s.flagged {
		if now.Sub(at) < modFlagEvery {
			return // keep the flag times, so a reconnect can't flag again at once
		}
	}
	delete(m.signals, id)
}
