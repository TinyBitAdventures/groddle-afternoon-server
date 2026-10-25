package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type echoModule struct {
	h      *Hub
	events []string
}

func (m *echoModule) Register(r *Registry) {
	r.Handle("echo", func(cl *Client, frame json.RawMessage) {
		m.h.BroadcastStreetLocked(cl.Street(), map[string]any{"t": "echoed", "id": cl.ID()}, 0)
	})
	r.OnConnect(func(cl *Client) { m.events = append(m.events, "connect "+cl.Name()) })
	r.OnJoin(func(cl *Client) { m.events = append(m.events, "join "+cl.Street()) })
	r.OnLeave(func(cl *Client, street string) { m.events = append(m.events, "leave "+street) })
	r.OnDisconnect(func(cl *Client) { m.events = append(m.events, "offline "+cl.Name()) })
}

func TestModulesGetTheirKindsAndHooks(t *testing.T) {
	var mod *echoModule
	saved := modules
	modules = append(append([]moduleFactory{}, saved...), func(h *Hub) Module { mod = &echoModule{h: h}; return mod })
	defer func() { modules = saved }()

	var fired []func()
	hub := NewHub(nil)
	hub.afterFunc = func(d time.Duration, fn func()) func() bool { fired = append(fired, fn); return func() bool { return true } }
	cl := hub.Connect(Claims{Sub: 1, Name: "Ada"})
	<-cl.send
	hub.Handle(cl, []byte(`{"t":"join","street":"ONE"}`))
	<-cl.send
	hub.Handle(cl, []byte(`{"t":"echo"}`))
	if got := string(<-cl.send); !strings.Contains(got, `"echoed"`) {
		t.Fatalf("the module's kind answered %s", got)
	}
	ran := false
	hub.After(time.Minute, func() { ran = hub.ClientLocked(1) != nil })
	fired[0]()
	if !ran {
		t.Fatal("After runs its func with the lock held and the hub usable")
	}
	hub.Handle(cl, []byte(`{"t":"join","street":"TWO"}`))
	hub.Disconnect(cl)
	want := "connect Ada,join ONE,leave ONE,join TWO,leave TWO,offline Ada"
	if got := strings.Join(mod.events, ","); got != want {
		t.Fatalf("hooks ran %s", got)
	}
}

func TestACoreKindCannotBeTaken(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering chat should panic")
		}
	}()
	(&Registry{h: NewHub(nil)}).Handle("chat", nil)
}
