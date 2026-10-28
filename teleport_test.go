package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var teleportNonce int

// startTeleportServer is startServer with the HTTP base kept for the site's calls.
func startTeleportServer(t *testing.T) (ws, base string) {
	t.Helper()
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), NewHub(nil), nil))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", srv.URL
}

// siteTeleport posts a teleport the way src/AdminTeleport.php does; header "" signs it with secret now.
func siteTeleport(t *testing.T, base string, body any, header string) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	if header == "" {
		teleportNonce++
		header = signService(testSecret, "POST", serviceRouteTeleport, time.Now().Unix(), fmt.Sprintf("%016x", teleportNonce), raw)
	}
	return postService(t, base, raw, header)
}

func postService(t *testing.T, base string, raw []byte, header string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/ws?call=teleport", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if header != "-" {
		req.Header.Set("X-Groddle-Service", header)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var a struct{ Status string }
	json.NewDecoder(res.Body).Decode(&a)
	return res.StatusCode, a.Status
}

func TestTeleportSignedCallMovesAnOnlinePlayer(t *testing.T) {
	ws, base := startTeleportServer(t)
	ada := signIn(t, ws, 1, "Ada")
	bob := signIn(t, ws, 2, "Bob")
	ada.send(map[string]any{"t": "join", "street": "GA5STUCK", "x": 10, "y": 20})
	ada.next("street")
	bob.send(map[string]any{"t": "join", "street": "GA5STUCK", "x": 30, "y": 40})
	bob.next("street")
	ada.next("joined")

	code, status := siteTeleport(t, base, map[string]any{"player": 2, "street": "GA5FREE", "x": 120.04, "y": 540}, "")
	if code != 200 || status != "sent" {
		t.Fatalf("answer %d %q", code, status)
	}
	m := bob.next("teleport")
	if m["street"] != "GA5FREE" || m["x"] != 120.0 || m["y"] != 540.0 || m["by"] != "admin" {
		t.Fatalf("teleport message %v", m)
	}
	// Dropped from the old street as a leave would be: the others see them go.
	if left := ada.next("left"); left["id"] != 2.0 {
		t.Fatalf("left %v", left)
	}
	// And they join the new street once their game is there.
	bob.send(map[string]any{"t": "join", "street": "GA5FREE", "x": 120, "y": 540})
	if st := bob.next("street"); st["street"] != "GA5FREE" {
		t.Fatalf("joined %v", st)
	}
}

func TestTeleportWithoutASpotSendsNone(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	if code, status := siteTeleport(t, base, map[string]any{"player": 2, "street": "GA5FREE"}, ""); code != 200 || status != "sent" {
		t.Fatalf("answer %d %q", code, status)
	}
	m := bob.next("teleport")
	if _, has := m["x"]; has {
		t.Fatalf("a spot nobody named: %v", m)
	}
	if _, has := m["y"]; has || m["street"] != "GA5FREE" {
		t.Fatalf("message %v", m)
	}
}

func TestTeleportRefusesUnsignedAndBadSignatures(t *testing.T) {
	ws, base := startTeleportServer(t)
	bob := signIn(t, ws, 2, "Bob")
	body := []byte(`{"player":2,"street":"GA5FREE"}`)
	now := time.Now().Unix()
	wrong := []byte(strings.Repeat("w", 40))
	cases := map[string]string{
		"no header":      "-",
		"empty":          "garbage",
		"wrong secret":   signService(wrong, "POST", serviceRouteTeleport, now, "00000000000000a1", body),
		"another route":  signService(testSecret, "POST", ledgerRouteTrade, now, "00000000000000a2", body),
		"another method": signService(testSecret, "PUT", serviceRouteTeleport, now, "00000000000000a3", body),
		"too old":        signService(testSecret, "POST", serviceRouteTeleport, now-120, "00000000000000a4", body),
		"other body":     signService(testSecret, "POST", serviceRouteTeleport, now, "00000000000000a5", []byte(`{"player":3,"street":"GA5FREE"}`)),
	}
	for name, header := range cases {
		if code, status := postService(t, base, body, header); code != http.StatusUnauthorized || status != "refused" {
			t.Errorf("%s: %d %q", name, code, status)
		}
	}
	// A good call works once; the same header again is refused (a replay).
	good := signService(testSecret, "POST", serviceRouteTeleport, now, "00000000000000b1", body)
	if code, status := postService(t, base, body, good); code != 200 || status != "sent" {
		t.Fatalf("good call %d %q", code, status)
	}
	bob.next("teleport")
	if code, status := postService(t, base, body, good); code != http.StatusUnauthorized || status != "refused" {
		t.Fatalf("replay %d %q", code, status)
	}
	// Nothing but the one good call reached Bob.
	bob.send(map[string]any{"t": "ping"})
	for {
		m := bob.any()
		if m["t"] == "teleport" {
			t.Fatalf("a refused call moved Bob: %v", m)
		}
		if m["t"] == "pong" {
			break
		}
	}
}

func TestTeleportOfSomeoneOfflineIsANoOp(t *testing.T) {
	ws, base := startTeleportServer(t)
	signIn(t, ws, 2, "Bob")
	if code, status := siteTeleport(t, base, map[string]any{"player": 99, "street": "GA5FREE"}, ""); code != 200 || status != "offline" {
		t.Fatalf("unknown player %d %q", code, status)
	}
}

func TestTeleportRefusesWhatItCannotSend(t *testing.T) {
	_, base := startTeleportServer(t)
	for _, body := range []any{
		map[string]any{"player": 0, "street": "GA5FREE"},
		map[string]any{"player": 2, "street": "not a street!"},
		map[string]any{"player": 2},
		"just text",
	} {
		if code, status := siteTeleport(t, base, body, ""); code != http.StatusBadRequest || status != "bad" {
			t.Errorf("%v: %d %q", body, code, status)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/ws?call=other", strings.NewReader(`{}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown call %d", res.StatusCode)
	}
}

func TestTeleportIsNeverAClientMessage(t *testing.T) {
	ws, _ := startTeleportServer(t)
	ada := signIn(t, ws, 1, "Ada")
	bob := signIn(t, ws, 2, "Bob")
	ada.send(map[string]any{"t": "join", "street": "GA5STUCK"})
	ada.next("street")
	bob.send(map[string]any{"t": "join", "street": "GA5STUCK"})
	bob.next("street")
	ada.next("joined")
	// Bob asks the socket to teleport Ada: refused, and nobody moves.
	bob.send(map[string]any{"t": "teleport", "player": 1, "street": "GA5FREE"})
	if e := bob.next("error"); e["code"] != "bad_frame" {
		t.Fatalf("error %v", e)
	}
	ada.send(map[string]any{"t": "ping"})
	for {
		m := ada.any()
		if m["t"] == "teleport" || m["t"] == "left" {
			t.Fatalf("a client's frame moved someone: %v", m)
		}
		if m["t"] == "pong" {
			break
		}
	}
}

func TestTeleportSignatureMatchesTheSite(t *testing.T) {
	// The same vector as spike tests/g406-admin-teleport.test.js (ServiceCall::sign in PHP).
	got := signService([]byte(strings.Repeat("x", 40)), "POST", serviceRouteTeleport, 1700000000, "0123456789abcdef", []byte(`{"player":7,"street":"GA5FREE"}`))
	if got != teleportVector {
		t.Fatalf("signature %s", got)
	}
}

const teleportVector = "1700000000.0123456789abcdef.1vZgXq97IkOr-sNZUsK_sS2XVlbKbSpwYfU7tJapfhk"

// any reads the next frame, whatever its kind.
func (p *peer) any() map[string]any {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := p.conn.Read(ctx)
	if err != nil {
		p.t.Fatalf("waiting for a frame: %v", err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}
