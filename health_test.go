package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const procStatA = `cpu  10000 500 3000 80000 1000 0 200 300 0 0
cpu0 5000 250 1500 40000 500 0 100 150 0 0
intr 1 2 3
`

// 60 s later: 1,200 jiffies of work (incl. 30 stolen), 4,800 idle and iowait.
const procStatB = `cpu  11000 500 3150 84700 1100 0 220 330 0 0
cpu0 5500 250 1575 42350 550 0 110 165 0 0
`

const netDevA = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 99999999     100    0    0    0     0          0         0 99999999     100    0    0    0     0       0          0
  eth0: 1000000    2000    0    0    0     0          0         0  2000000    3000    0    0    0     0       0          0
  eth1:  500000     100    0    0    0     0          0         0   100000     100    0    0    0     0       0          0
`

const netDevB = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 99999999     100    0    0    0     0          0         0 99999999     100    0    0    0     0       0          0
  eth0: 4000000    2000    0    0    0     0          0         0  8000000    3000    0    0    0     0       0          0
  eth1:  500000     100    0    0    0     0          0         0   100000     100    0    0    0     0       0          0
`

const meminfo = `MemTotal:        4009152 kB
MemFree:          201000 kB
MemAvailable:    2780000 kB
Buffers:           90000 kB
`

const selfStatus = `Name:	groddle-realtime
VmPeak:	 1300000 kB
VmRSS:	   90112 kB
Threads:	9
`

func TestHealthParsesProcFiles(t *testing.T) {
	a, ok := parseProcStat(procStatA)
	if !ok || a.total != 10000+500+3000+80000+1000+0+200+300 || a.idle != 81000 || a.steal != 300 {
		t.Fatalf("stat %+v %v", a, ok)
	}
	if _, ok := parseProcStat("intr 1 2\n"); ok {
		t.Fatal("no cpu line is no reading")
	}
	l1, l5, l15, ok := parseLoadavg("0.41 0.38 0.30 2/161 4567\n")
	if !ok || l1 != 0.41 || l5 != 0.38 || l15 != 0.30 {
		t.Fatalf("loadavg %v %v %v %v", l1, l5, l15, ok)
	}
	if _, _, _, ok := parseLoadavg("bad"); ok {
		t.Fatal("bad loadavg")
	}
	m := parseKB(meminfo, "MemTotal", "MemAvailable")
	if m["MemTotal"] != 4009152 || m["MemAvailable"] != 2780000 || len(m) != 2 {
		t.Fatalf("meminfo %v", m)
	}
	if rss := parseKB(selfStatus, "VmRSS"); rss["VmRSS"] != 90112 {
		t.Fatalf("status %v", rss)
	}
	rx, tx, ok := parseNetDev(netDevA)
	if !ok || rx != 1500000 || tx != 2100000 {
		t.Fatalf("net/dev %d %d %v (loopback left out, the rest summed)", rx, tx, ok)
	}
}

// fakeProc writes a /proc of fixture files.
func fakeProc(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHealthSampleFromAProcOfFixtures(t *testing.T) {
	proc := t.TempDir()
	fakeProc(t, proc, map[string]string{"stat": procStatA, "net/dev": netDevA, "loadavg": "0.41 0.38 0.30 2/161 4567\n", "meminfo": meminfo, "self/status": selfStatus})
	p := &healthProbe{proc: proc, dir: t.TempDir()}
	at := time.Date(2026, 11, 11, 14, 2, 30, 0, time.UTC)
	p.baseline(at)
	healthFramesIn.Add(50)
	healthFramesOut.Add(120)
	healthLagMax.Store(0)
	healthNoteLag(time.Now().Add(-7 * time.Millisecond))
	healthNoteLag(time.Now().Add(-2 * time.Millisecond)) // the slowest is kept
	fakeProc(t, proc, map[string]string{"stat": procStatB, "net/dev": netDevB})
	s := p.sample(at.Add(30 * time.Second)) // the part minute after a start: rates scaled to a minute

	if s.T != time.Date(2026, 11, 11, 14, 3, 0, 0, time.UTC).Unix() {
		t.Fatalf("t %d is not the minute", s.T)
	}
	// dt 6000 jiffies, idle 4800: 20% busy, 30 stolen: 0.5%.
	if s.C == nil || *s.C != 20 || s.St == nil || *s.St != 0.5 {
		t.Fatalf("cpu %v steal %v", s.C, s.St)
	}
	if *s.L1 != 0.41 || *s.L5 != 0.38 || *s.L15 != 0.3 {
		t.Fatal("load")
	}
	if *s.Mt != 3915 || *s.Mu != 1200 || *s.Rss != 88 {
		t.Fatalf("memory mt %v mu %v rss %v", *s.Mt, *s.Mu, *s.Rss)
	}
	// 3 MB in and 6 MB out in 30 s: 6 MB and 12 MB a minute.
	if *s.Ni != 6000000 || *s.No != 12000000 {
		t.Fatalf("net %v %v", *s.Ni, *s.No)
	}
	if *s.Mi != 100 || *s.Mo != 240 {
		t.Fatalf("frames %v %v", *s.Mi, *s.Mo)
	}
	if s.Lag == nil || *s.Lag < 7 || *s.Lag > 50 {
		t.Fatalf("lag %v", s.Lag)
	}
	if s.N < 1 || s.Hp == nil || s.G < 1 || s.Du == nil || s.Df == nil {
		t.Fatalf("process and disk %+v", s)
	}
	// The next minute: nothing moved (no jiffy passed is no CPU reading), the lag starts again.
	s = p.sample(at.Add(90 * time.Second))
	if s.C != nil || *s.Ni != 0 || *s.Mi != 0 || s.Lag != nil {
		t.Fatalf("a quiet minute: %v %v %v %v", s.C, *s.Ni, *s.Mi, s.Lag)
	}
	// A counter that went back (an interface reset) is no reading, never a negative rate.
	fakeProc(t, proc, map[string]string{"net/dev": netDevA})
	if s = p.sample(at.Add(150 * time.Second)); s.Ni != nil || s.No != nil {
		t.Fatalf("reset: %v %v", s.Ni, s.No)
	}
}

func TestHealthSampleWithoutProcLeavesThoseOut(t *testing.T) {
	p := &healthProbe{proc: filepath.Join(t.TempDir(), "none"), dir: t.TempDir()}
	at := time.Now()
	p.baseline(at)
	s := p.sample(at.Add(time.Minute))
	if s.C != nil || s.St != nil || s.L1 != nil || s.Mt != nil || s.Mu != nil || s.Rss != nil || s.Ni != nil || s.No != nil {
		t.Fatalf("no /proc: %+v", s)
	}
	if s.N < 1 || s.Hp == nil || s.Pc == nil || s.Du == nil || s.Mi == nil {
		t.Fatalf("what every machine has: %+v", s)
	}
	b, _ := json.Marshal(s)
	for _, k := range []string{`"c"`, `"l1"`, `"mt"`, `"ni"`, `"rss"`} {
		if strings.Contains(string(b), k) {
			t.Fatalf("%s written as null: %s", k, b)
		}
	}
}

func TestHealthLineRoundTrip(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	s := healthSample{T: 1762869780, P: 112, W: 14, S: 63, C: f(73.4), St: f(1.2), Pc: f(41.8), L1: f(1.84), L5: f(1.61), L15: f(1.42), N: 2,
		Mt: f(3915), Mu: f(2210), Rss: f(412.5), Hp: f(188.2), G: 1450, Du: f(19.4), Df: f(30.6), Ni: f(31457280), No: f(52428800), Mi: f(24000), Mo: f(91000), Lag: f(38.4)}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), `{"t":1762869780,`) {
		t.Fatalf("a line starts with its time: %s", b)
	}
	if len(b) > 300 {
		t.Fatalf("a busy line is %d bytes: %s", len(b), b)
	}
	t.Logf("a busy line, %d bytes: %s", len(b)+1, b)
	var back healthSample
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip %+v", back)
	}
	fast, ok := parseHealthLine(b)
	if !ok || !reflect.DeepEqual(fast, s) {
		t.Fatalf("the fast reader %+v", fast)
	}
	if fast, ok := parseHealthLine([]byte(`{"t":60,"p":0,"w":0,"s":0,"n":8,"g":12,"hp":2.5,"new":1}`)); !ok || fast.T != 60 || fast.N != 8 || fast.C != nil || *fast.Hp != 2.5 {
		t.Fatalf("a Mac line, with a key from a later build: %+v", fast)
	}
	if fast, ok := parseHealthLine([]byte(`{"t": 60, "p": 3}`)); !ok || fast.P != 3 {
		t.Fatalf("any other JSON goes the slow way: %+v %v", fast, ok)
	}
	for _, bad := range []string{`{"t":17628`, `{"t":1,"p":"x"}`, `[]`} {
		if _, ok := parseHealthLine([]byte(bad)); ok {
			t.Fatalf("parsed %q", bad)
		}
	}
	if at, ok := lineTime(b); !ok || at != s.T {
		t.Fatalf("lineTime %d %v", at, ok)
	}
	for _, bad := range []string{``, `{"p":1}`, `{"t":x}`, `[1]`} {
		if _, ok := lineTime([]byte(bad)); ok {
			t.Fatalf("lineTime %q", bad)
		}
	}
}

func TestHealthBucketSizes(t *testing.T) {
	for _, c := range []struct {
		span   int64
		points int
		want   int64
	}{
		{86400, 1500, 60}, {86400, 1000, 120}, {7 * 86400, 1500, 600}, {30 * 86400, 1500, 1800},
		{90 * 86400, 1500, 7200}, {365 * 86400, 1500, 21600}, {3 * 365 * 86400, 1500, 86400}, {20 * 365 * 86400, 1500, 5 * 86400}, {10, 1500, 60},
	} {
		if got := healthBucket(c.span, c.points); got != c.want {
			t.Fatalf("span %d points %d: %d, want %d", c.span, c.points, got, c.want)
		}
	}
}

func TestHealthRowsBucketMaxMeanAndNewest(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	r := newHealthRows(300)
	base := int64(1762869600) // on a 5 minute boundary
	r.add(&healthSample{T: base, P: 4, C: f(10), L1: f(0.5), Du: f(9.1), N: 2, G: 100})
	r.add(&healthSample{T: base + 60, P: 9, C: f(30), L1: f(1.5), Du: f(9.2), N: 2, G: 90, Lag: f(12)})
	r.add(&healthSample{T: base + 240, P: 5, N: 2, G: 80}) // no CPU reading this minute
	r.add(&healthSample{T: base + 600, P: 1, C: f(5), N: 2})
	r.add(&healthSample{T: base + 30, P: 2, Du: f(9.0), N: 2}) // a line out of order (a clock stepped back) joins its bucket
	r.flush()
	if len(r.rows) != 2 {
		t.Fatalf("rows %d (an empty bucket is no row)", len(r.rows))
	}
	col := func(row []*float64, name string) *float64 {
		for i, n := range healthFields() {
			if n == name {
				return row[i]
			}
		}
		t.Fatalf("no field %s", name)
		return nil
	}
	a := r.rows[0]
	for name, want := range map[string]float64{"t": float64(base), "p": 9, "pa": 5, "c": 20, "cx": 30, "l1": 1, "l1x": 1.5, "du": 9.2, "g": 100, "lag": 12, "n": 2} {
		if v := col(a, name); v == nil || *v != want {
			t.Fatalf("%s %v, want %v", name, v, want)
		}
	}
	if col(a, "mt") != nil || col(a, "ni") != nil {
		t.Fatal("a value no sample had is null")
	}
	if b := r.rows[1]; *col(b, "t") != float64(base+600) || *col(b, "p") != 1 || col(b, "lag") != nil {
		t.Fatalf("second bucket %v", b)
	}
}

func TestHealthCallIsOnlyTheSitesSignedPost(t *testing.T) {
	dir := t.TempDir()
	saved := healthDirForTest
	healthDirForTest = dir
	h := NewHub(nil)
	healthDirForTest = saved
	t.Cleanup(h.Shutdown)
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), h, nil))
	t.Cleanup(srv.Close)
	ws, base := "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", srv.URL
	ada := signIn(t, ws, 1, "Ada")
	ada.send(map[string]any{"t": "join", "street": "GA5HERE"})
	ada.next("street")

	// History in two months (a month file each), then a sample now with Ada on her street.
	l := h.healthOf()
	old := time.Date(2026, 10, 31, 23, 58, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if err := l.append(healthSample{T: old.Add(time.Duration(i) * time.Minute).Unix(), P: 10 + i, N: 2}); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "2026-11.jsonl.bad"), []byte("not a month"), 0o600)
	f, _ := os.OpenFile(filepath.Join(dir, "2026-11.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{\"t\":17628\n") // a torn line is skipped
	f.Close()
	now := l.take(time.Now())
	if now.P != 1 || now.S != 1 || now.W != 0 {
		t.Fatalf("counts %+v", now)
	}
	for _, name := range []string{"2026-10.jsonl", "2026-11.jsonl"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v", name, err)
		}
	}

	code, out := siteCall(t, base, "health", map[string]any{}, "-")
	mustStatus(t, code, out, 401, "refused")
	raw, _ := json.Marshal(map[string]any{})
	other := signService(testSecret, "POST", serviceRoutePrefix+"chatlog", time.Now().Unix(), "00000000000ea171", raw)
	code, out = siteCall(t, base, "health", map[string]any{}, other)
	mustStatus(t, code, out, 401, "refused")
	for _, bad := range []map[string]any{{"since": -1}, {"points": healthPointsMax + 1}, {"since": 100, "until": 50}, {"since": "x"}} {
		code, out = siteCall(t, base, "health", bad, "")
		mustStatus(t, code, out, 400, "bad")
	}

	// Everything: since 0 starts at the first sample.
	code, out = siteCall(t, base, "health", map[string]any{"since": 0}, "")
	mustStatus(t, code, out, 200, "ok")
	rows := out["rows"].([]any)
	if out["first"].(float64) != float64(old.Unix()) || len(rows) < 2 {
		t.Fatalf("first %v rows %d", out["first"], len(rows))
	}
	fields := out["fields"].([]any)
	if fields[0] != "t" || fields[1] != "p" || len(rows[0].([]any)) != len(fields) {
		t.Fatalf("fields %v", fields)
	}
	m := out["machine"].(map[string]any)
	if m["cpus"].(float64) < 1 || m["version"] != version || m["uptime"] == nil {
		t.Fatalf("machine %v", m)
	}
	if n := out["now"].(map[string]any); n["p"].(float64) != 1 {
		t.Fatalf("now %v", n)
	}

	// October's last two minutes only, a minute a row.
	code, out = siteCall(t, base, "health", map[string]any{"since": old.Unix(), "until": old.Add(2 * time.Minute).Unix()}, "")
	mustStatus(t, code, out, 200, "ok")
	rows = out["rows"].([]any)
	if out["bucket"].(float64) != 60 || len(rows) != 2 || rows[0].([]any)[1].(float64) != 10 || rows[1].([]any)[1].(float64) != 11 {
		t.Fatalf("bucket %v rows %v", out["bucket"], rows)
	}
	// Bucketed: the four history minutes in one row, the most players kept.
	code, out = siteCall(t, base, "health", map[string]any{"since": old.Unix(), "until": old.Add(4 * time.Minute).Unix(), "points": 1}, "")
	mustStatus(t, code, out, 200, "ok")
	if rows = out["rows"].([]any); out["bucket"].(float64) != 300 || len(rows) > 2 {
		t.Fatalf("bucket %v rows %v", out["bucket"], rows)
	}

	// Never a socket message.
	ada.send(map[string]any{"t": "health"})
	if e := ada.next("error"); e["code"] != "bad_frame" {
		t.Fatalf("socket health: %v", e)
	}
	// No samples kept: the call says so.
	_, base2 := startTeleportServer(t)
	code, out = siteCall(t, base2, "health", map[string]any{}, "")
	mustStatus(t, code, out, 200, "off")
}
