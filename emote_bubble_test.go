package main

import (
	"testing"
	"time"
)

// A musicblock's or a skill package's bubble word reaches the street whatever the player's chat:
// Glitch's announcements had no chat gate (musicblock_*.js 153, inc_skill_packages.js 294-314).
// Chat text stays blocked under the cone and past the chat allowance, and other emote words keep
// the chat allowance.
func TestBubbleWordsGoPastTheChatGate(t *testing.T) {
	r := newChanRig(t)
	const street = "GA5101HF7F429V5"
	a := r.hub.Connect(Claims{Sub: 7, Name: "Ada", Iat: r.now.Unix(), Cone: true})
	drain(a)
	b := r.connect(9, "Bo")
	c := r.connect(11, "Cy")
	for _, cl := range []*Client{a, b, c} {
		r.send(cl, map[string]any{"t": "join", "street": street})
	}
	drain(a)
	drain(b)
	drain(c)
	emoted := func(cl *Client) []string {
		var out []string
		for _, m := range kinds(drain(cl), "emoted") {
			out = append(out, m["e"].(string))
		}
		return out
	}

	// Under the cone: chat text reaches nobody, the musicblock's bubble (sp_i_<block>_<s>) does.
	r.send(a, map[string]any{"t": "chat", "text": "hello"})
	if got := kinds(drain(b), "said"); len(got) != 0 {
		t.Fatalf("chat from under the cone: %v", got)
	}
	drain(a)
	r.send(a, map[string]any{"t": "emote", "e": "sp_i_ab_30"})
	if got := emoted(b); len(got) != 1 || got[0] != "sp_i_ab_30" {
		t.Fatalf("a silenced player's musicblock bubble: %v", got)
	}

	// Cy spends the whole chat allowance on chat lines; a line more is refused, the bubble still goes.
	for i := 0; i < chatBurst; i++ {
		r.send(c, map[string]any{"t": "chat", "text": "la la la"})
	}
	drain(b)
	r.send(c, map[string]any{"t": "chat", "text": "one too many"})
	if got := kinds(drain(b), "said"); len(got) != 0 {
		t.Fatalf("chat past the allowance: %v", got)
	}
	if errs := kinds(drain(c), "error"); len(errs) != 1 || errs[0]["code"] != "slow_down" {
		t.Fatalf("told to slow down: %v", errs)
	}
	for _, w := range []string{"sp_i_ab_30", "sp_mine_4", "sp_im_cd_12", "sp_off_0"} {
		r.send(c, map[string]any{"t": "emote", "e": w})
	}
	if got := emoted(b); len(got) != 4 || got[0] != "sp_i_ab_30" || got[3] != "sp_off_0" {
		t.Fatalf("bubbles past the chat allowance: %v", got)
	}
	// Other emote words still count against chat's allowance.
	r.send(c, map[string]any{"t": "emote", "e": "happy"})
	if got := emoted(b); len(got) != 0 {
		t.Fatalf("an emotion past the chat allowance: %v", got)
	}
	// A word that only looks like one is chat's: no letters past the shape, no free text.
	r.now = r.now.Add(10 * time.Second)
	r.send(c, map[string]any{"t": "emote", "e": "sp_buy_gold_now"})
	if got := emoted(b); len(got) != 1 || got[0] != "sp_buy_gold_now" {
		// sp_buy_gold_now has no seconds: not a bubble word, so it went on chat's (refilled) allowance.
		t.Fatalf("an ordinary word on the chat allowance: %v", got)
	}
	if bubbleWordRe.MatchString("sp_buy_gold_now") || bubbleWordRe.MatchString("hello there") || !bubbleWordRe.MatchString("sp_i_ab_30") {
		t.Fatal("bubbleWordRe takes only the game's bubble words")
	}

	// The bubbles have their own allowance: a flood is cut off.
	r.now = r.now.Add(time.Minute)
	for i := 0; i < bubbleBurst+5; i++ {
		r.send(c, map[string]any{"t": "emote", "e": "sp_mine_4"})
	}
	if got := emoted(b); len(got) != bubbleBurst {
		t.Fatalf("a flood of bubbles: %d went, want %d", len(got), bubbleBurst)
	}
}
