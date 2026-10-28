package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var adminNonce int

// siteCall posts one of the console's calls the way src/AdminConsole.php does; header "" signs it now.
func siteCall(t *testing.T, base, call string, body any, header string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	if header == "" {
		adminNonce++
		header = signService(testSecret, "POST", serviceRoutePrefix+call, time.Now().Unix(), fmt.Sprintf("%016x", 0xad000000+adminNonce), raw)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/ws?call="+call, bytes.NewReader(raw))
	if header != "-" {
		req.Header.Set("X-Groddle-Service", header)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func mustStatus(t *testing.T, code int, out map[string]any, wantCode int, want string) {
	t.Helper()
	if code != wantCode || out["status"] != want {
		t.Fatalf("answer %d %v, want %d %q", code, out, wantCode, want)
	}
}

// pong drains frames until a pong, failing on any frame of kind bad.
func pong(t *testing.T, p *peer, bad ...string) {
	t.Helper()
	p.send(map[string]any{"t": "ping"})
	for {
		m := p.any()
		for _, k := range bad {
			if m["t"] == k {
				t.Fatalf("unexpected %v", m)
			}
		}
		if m["t"] == "pong" {
			return
		}
	}
}

func TestAdminOnlineListsConnectedPlayers(t *testing.T) {
	ws, base := startTeleportServer(t)
	ada := signIn(t, ws, 1, "Ada")
	signIn(t, ws, 2, "bob")
	ada.send(map[string]any{"t": "join", "street": "GA5HERE"})
	ada.next("street")
	code, out := siteCall(t, base, "online", map[string]any{}, "")
	mustStatus(t, code, out, 200, "ok")
	players := out["players"].([]any)
	if len(players) != 2 {
		t.Fatalf("players %v", players)
	}
	a, b := players[0].(map[string]any), players[1].(map[string]any)
	if a["name"] != "Ada" || a["street"] != "GA5HERE" || a["id"] != 1.0 || b["name"] != "bob" || b["street"] != "" {
		t.Fatalf("players %v", players)
	}
	if since := a["since"].(float64); since < float64(time.Now().Add(-time.Minute).Unix()) || since > float64(time.Now().Unix()+1) {
		t.Fatalf("since %v", since)
	}
}

func TestAdminCallsRefuseUnsignedBadAndReplayed(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	now := time.Now().Unix()
	calls := map[string]any{
		"online":  map[string]any{},
		"message": map[string]any{"player": 2, "id": "am-00000000000000a1", "text": "hi"},
		"boot":    map[string]any{"player": 2, "reason": "x"},
		"ban":     map[string]any{"player": 2, "reason": "x"},
		"unban":   map[string]any{"player": 2},
		"bans":    map[string]any{"players": []int{2}},
		"give":    map[string]any{"player": 2, "op": "admin-give-00000000000000a1", "items": []any{map[string]any{"c": "cherry", "n": 1}}},
		"refill":  map[string]any{"player": 2},
	}
	for call, body := range calls {
		raw, _ := json.Marshal(body)
		for name, header := range map[string]string{
			"no header":      "-",
			"garbage":        "garbage",
			"wrong secret":   signService([]byte(strings.Repeat("w", 40)), "POST", serviceRoutePrefix+call, now, "00000000000000c1", raw),
			"teleport route": signService(testSecret, "POST", serviceRouteTeleport, now, "00000000000000c2", raw),
			"too old":        signService(testSecret, "POST", serviceRoutePrefix+call, now-120, "00000000000000c3", raw),
			"other body":     signService(testSecret, "POST", serviceRoutePrefix+call, now, "00000000000000c4", []byte(`{"player":3}`)),
		} {
			if code, out := siteCall(t, base, call, body, header); code != http.StatusUnauthorized || out["status"] != "refused" {
				t.Errorf("%s %s: %d %v", call, name, code, out)
			}
		}
	}
	// One good call works once; the same header again is a replay.
	raw, _ := json.Marshal(calls["refill"])
	good := signService(testSecret, "POST", serviceRoutePrefix+"refill", now, "00000000000000d1", raw)
	code, out := siteCall(t, base, "refill", calls["refill"], good)
	mustStatus(t, code, out, 200, "sent")
	bob.next("admin_refill")
	code, out = siteCall(t, base, "refill", calls["refill"], good)
	mustStatus(t, code, out, 401, "refused")
	// Nothing a refused call asked for reached Bob, and he is still connected and not banned.
	pong(t, bob, "admin_message", "booted", "admin_give", "admin_refill")
}

func TestAdminKindsAreNeverClientMessages(t *testing.T) {
	ws, _ := startTeleportServer(t)
	ada := signIn(t, ws, 1, "Ada")
	bob := signIn(t, ws, 2, "Bob")
	for _, kind := range []string{"online", "message", "boot", "ban", "unban", "bans", "give", "refill", "admin_message", "booted", "admin_give", "admin_refill"} {
		bob.send(map[string]any{"t": kind, "player": 1, "text": "hi", "reason": "x", "players": []int{1}})
		if e := bob.next("error"); e["code"] != "bad_frame" {
			t.Fatalf("%s: %v", kind, e)
		}
	}
	pong(t, ada, "admin_message", "booted", "admin_give", "admin_refill")
}

func TestAdminMessageLiveAndQueuedForNextConnect(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	code, out := siteCall(t, base, "message", map[string]any{"player": 2, "id": "am-0000000000000001", "text": "  Hello\x07 <b>there</b>\n\n\n\nSecond line  "}, "")
	mustStatus(t, code, out, 200, "sent")
	m := bob.next("admin_message")
	if m["text"] != "Hello <b>there</b>\n\nSecond line" || m["id"] != "am-0000000000000001" {
		t.Fatalf("message %q", m["text"])
	}
	// Offline: kept, and handed over at the next connect.
	code, out = siteCall(t, base, "message", map[string]any{"player": 9, "id": "am-0000000000000002", "text": strings.Repeat("é", adminMaxText+40)}, "")
	mustStatus(t, code, out, 200, "queued")
	zed := signIn(t, ws, 9, "Zed")
	got := zed.next("admin_message")
	if n := len([]rune(got["text"].(string))); n != adminMaxText {
		t.Fatalf("capped to %d", n)
	}
	// Only once.
	zed.conn.CloseNow()
	time.Sleep(50 * time.Millisecond)
	zed2 := signIn(t, ws, 9, "Zed")
	pong(t, zed2, "admin_message")
	for _, bad := range []map[string]any{{"player": 2, "id": "am-1", "text": "x"}, {"player": 2, "id": "am-0000000000000003", "text": " \n "}, {"player": 0, "id": "am-0000000000000004", "text": "x"}} {
		if code, out := siteCall(t, base, "message", bad, ""); code != 400 || out["status"] != "bad" {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
}

func TestAdminBootSignsOutWithTheReason(t *testing.T) {
	ws, base := startTeleportServer(t)
	ada := signIn(t, ws, 1, "Ada")
	bob := signIn(t, ws, 2, "Bob")
	ada.send(map[string]any{"t": "join", "street": "GA5HERE"})
	ada.next("street")
	bob.send(map[string]any{"t": "join", "street": "GA5HERE"})
	bob.next("street")
	ada.next("joined")
	code, out := siteCall(t, base, "boot", map[string]any{"player": 2, "reason": "Take a\nbreak"}, "")
	mustStatus(t, code, out, 200, "sent")
	if m := bob.next("booted"); m["reason"] != "Take a break" || m["banned"] != false {
		t.Fatalf("booted %v", m)
	}
	if c := bob.closeCode(); c != closeBooted {
		t.Fatalf("closed with %d", c)
	}
	if left := ada.next("left"); left["id"] != 2.0 {
		t.Fatalf("left %v", left)
	}
	_, out = siteCall(t, base, "online", map[string]any{}, "")
	if n := len(out["players"].([]any)); n != 1 {
		t.Fatalf("still listed: %v", out)
	}
	// Not banned: they may come back.
	signIn(t, ws, 2, "Bob")
	code, out = siteCall(t, base, "boot", map[string]any{"player": 77}, "")
	mustStatus(t, code, out, 200, "offline")
}

func TestAdminBanRefusesAtAuthAndUnbanLetsBack(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	code, out := siteCall(t, base, "ban", map[string]any{"player": 2, "reason": "Spam"}, "")
	mustStatus(t, code, out, 200, "sent")
	if m := bob.next("booted"); m["banned"] != true || m["reason"] != "Spam" {
		t.Fatalf("booted %v", m)
	}
	if c := bob.closeCode(); c != closeBanned {
		t.Fatalf("closed with %d", c)
	}
	// A good ticket for a banned account is refused at auth.
	again := dial(t, ws)
	again.send(map[string]any{"t": "auth", "ticket": ticketFor(t, 2, "Bob")})
	if c := again.closeCode(); c != closeBanned {
		t.Fatalf("banned reconnect closed with %d", c)
	}
	// Banning someone offline needs nobody online.
	code, out = siteCall(t, base, "ban", map[string]any{"player": 5}, "")
	mustStatus(t, code, out, 200, "banned")
	code, out = siteCall(t, base, "unban", map[string]any{"player": 2}, "")
	mustStatus(t, code, out, 200, "unbanned")
	signIn(t, ws, 2, "Bob")
}

func TestAdminBansListReplacesAndBootsTheNamed(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	siteCall(t, base, "ban", map[string]any{"player": 5}, "")
	code, out := siteCall(t, base, "bans", map[string]any{"players": []int{2, 7}}, "")
	mustStatus(t, code, out, 200, "ok")
	if out["count"] != 2.0 {
		t.Fatalf("count %v", out)
	}
	bob.next("booted")
	if c := bob.closeCode(); c != closeBanned {
		t.Fatalf("closed with %d", c)
	}
	signIn(t, ws, 5, "Five") // 5 is no longer on the list
	if code, out := siteCall(t, base, "bans", map[string]any{"players": []int{0}}, ""); code != 400 || out["status"] != "bad" {
		t.Fatalf("bad id %d %v", code, out)
	}
	if code, out := siteCall(t, base, "bans", map[string]any{}, ""); code != 400 || out["status"] != "bad" {
		t.Fatalf("no list %d %v", code, out)
	}
}

func TestAdminBansSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	bansFileForTest = filepath.Join(dir, "bans.json")
	t.Cleanup(func() { bansFileForTest = "" })
	_, base := startTeleportServer(t)
	siteCall(t, base, "ban", map[string]any{"player": 4}, "")
	siteCall(t, base, "ban", map[string]any{"player": 3}, "")
	b, err := os.ReadFile(bansFileForTest)
	if err != nil || string(b) != `{"banned":[3,4]}` {
		t.Fatalf("file %s %v", b, err)
	}
	ws2, _ := startTeleportServer(t)
	again := dial(t, ws2)
	again.send(map[string]any{"t": "auth", "ticket": ticketFor(t, 4, "Four")})
	if c := again.closeCode(); c != closeBanned {
		t.Fatalf("after a restart closed with %d", c)
	}
}

func TestAdminGiveDeliversLiveAndOfflineIsANoOp(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	give := map[string]any{"player": 2, "op": "admin-give-0123456789abcdef", "items": []any{map[string]any{"c": "cherry", "n": 3}}, "currants": 50, "text": "An administrator gave you 3 Cherries and 50 currants."}
	code, out := siteCall(t, base, "give", give, "")
	mustStatus(t, code, out, 200, "sent")
	m := bob.next("admin_give")
	items := m["items"].([]any)
	if m["op"] != "admin-give-0123456789abcdef" || m["currants"] != 50.0 || m["text"] != "An administrator gave you 3 Cherries and 50 currants." || len(items) != 1 || items[0].(map[string]any)["c"] != "cherry" || items[0].(map[string]any)["n"] != 3.0 {
		t.Fatalf("give %v", m)
	}
	give["player"] = 8
	code, out = siteCall(t, base, "give", give, "")
	mustStatus(t, code, out, 200, "offline")
	for _, bad := range []map[string]any{
		{"player": 2, "op": "trade-1", "currants": 5},
		{"player": 2, "op": "admin-give-0123456789abcdef"},
		{"player": 2, "op": "admin-give-0123456789abcdef", "currants": -1},
		{"player": 2, "op": "admin-give-0123456789abcdef", "items": []any{map[string]any{"c": "Cherry!", "n": 1}}},
		{"player": 2, "op": "admin-give-0123456789abcdef", "items": []any{map[string]any{"c": "cherry", "n": 0}}},
		{"player": 2, "op": "admin-give-0123456789abcdef", "items": []any{map[string]any{"c": "cherry", "n": 1}, map[string]any{"c": "cherry", "n": 1}}},
	} {
		if code, out := siteCall(t, base, "give", bad, ""); code != 400 || out["status"] != "bad" {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	pong(t, bob, "admin_give")
}

func TestAdminRefillLiveAndOffline(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	code, out := siteCall(t, base, "refill", map[string]any{"player": 2}, "")
	mustStatus(t, code, out, 200, "sent")
	bob.next("admin_refill")
	code, out = siteCall(t, base, "refill", map[string]any{"player": 3}, "")
	mustStatus(t, code, out, 200, "offline")
}

func TestAdminCallSignatureMatchesTheSite(t *testing.T) {
	// The same vector as spike tests/g407-admin-console.test.js (ServiceCall::sign in PHP).
	got := signService([]byte(strings.Repeat("x", 40)), "POST", serviceRoutePrefix+"boot", 1700000000, "0123456789abcdef", []byte(`{"player":7,"reason":"Spam"}`))
	if got != bootVector {
		t.Fatalf("signature %s", got)
	}
}

const bootVector = "1700000000.0123456789abcdef.9MY3KNwUctqbzzlpAAxyoTdMJzr9DOkqYm5bW0LQN-Y"
