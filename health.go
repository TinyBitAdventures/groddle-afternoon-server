package main

// Server health over time (Austin 2026-11-11: "review the server health over
// time ... see the count of active players on the game at that same moment ...
// capture at which point the server becomes under high load and upgrade
// proactively ... lightweight so we can keep all historical info"). A sampler
// writes one line a minute, on the minute, with who is playing and how hard
// the machine and this process are working; the console reads it through the
// signed health call (admin.go's console, teleport.go's signing).
//
// Where: one file a UTC month, <dir>/2026-11.jsonl, the directory -health-dir
// (default health/ beside -secret-file; off: no samples), mode 0700, the files
// 0600. Kept forever: a line is about 200 bytes, so a month is about 9 MB.
// Each line is one JSON object; a value this machine cannot read is left out
// (macOS has no /proc: no machine CPU, load, memory or network there):
//
//	{"t":1762819200,"p":12,"w":3,"s":7,"c":23.4,"st":0.2,"pc":11.8,"l1":0.41,"l5":0.38,"l15":0.3,"n":2,
//	 "mt":3915,"mu":1210,"rss":88,"hp":31.2,"g":161,"du":9.4,"df":40.6,"ni":3145728,"no":5242880,"mi":2400,"mo":9100,"lag":4.1}
//
//	t    the minute (unix seconds, UTC, on the minute)
//	p    players in the game now (connected and signed in; one socket per character)
//	w    the website's /chat/ sockets now (web.go; a player may have several tabs)
//	s    streets with at least one player
//	c    the whole machine's CPU busy % since the last sample (/proc/stat; iowait counts as idle)
//	st   of that, % stolen by the hypervisor (a shared droplet's neighbours)
//	pc   this process's CPU as a % of the whole machine (getrusage)
//	l1 l5 l15  load average (/proc/loadavg); n the CPU count, so load per core is l1/n
//	mt mu  memory total and used (total less MemAvailable), MB (/proc/meminfo)
//	rss  this process's resident memory, MB (/proc/self/status); hp the Go heap in use, MB; g goroutines
//	du df  disk used and free (to this user) on the data directory's filesystem, GB
//	ni no  network bytes in and out a minute, every interface but lo (/proc/net/dev)
//	mi mo  socket frames read and written a minute (main.go's read loop and writer)
//	lag  the slowest frame this minute, ms: from Hub.Handle's start, waiting for the hub's lock, until it let go
//
// Rates (c, st, pc, ni, no, mi, mo) are over the time since the last sample,
// scaled to a minute; the first sample after a start covers the part minute
// since the start.
//
// The console reads it, POST /ws?call=health:
//
//	health {"since":<unix s>,"until":<unix s>,"points":1000}   every field optional: since 0 is the first
//	       sample kept, until 0 is now; points at most healthPointsMax (default healthPointsDefault)
//	       200 {"status":"ok","bucket":600,"since":…,"until":…,"first":<the oldest t kept, 0 none>,
//	            "machine":{"host","os","cpus","mem","disk","version","started","uptime","boot"},
//	            "now":{a line, the newest}|null,"fields":["t","p",…],"rows":[[…],…]}
//	       200 {"status":"off"}   no samples kept
//
// The samples in [since, until) are put in buckets of bucket seconds (60 at
// least; the smallest of a fixed set of steps that keeps the rows at most
// points), each row one bucket that has samples, oldest first, in the order
// fields names: t the bucket's start, then per bucket the most (p, w, s, cx,
// l1x, g, lag, n, mt), the mean (pa, c, st, pc, l1, l5, l15, mu, rss, hp, ni,
// no, mi, mo) or the newest (du, df); a value no sample in the bucket had is
// null. machine is now: cpus, mem (MB) and disk (GB) as this machine has them,
// started and uptime (s) this process's, boot the machine's uptime (s).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	healthEvery         = time.Minute
	healthPointsDefault = 1000
	healthPointsMax     = 1500
)

var (
	healthDirFlag = flag.String("health-dir", "", "directory for the server health samples, one file a UTC month, kept forever (default: health beside -secret-file; off: none)")

	// Under go test the flag is ignored: a test sets the directory (none: no samples) and starts no sampler unless it asks.
	healthDirForTest string
	healthRunForTest bool
	healthProcRoot   = "/proc"

	healthStarted = time.Now()

	// Counters the socket code bumps (main.go, hub.go); the sampler reads them each minute.
	healthFramesIn  atomic.Int64
	healthFramesOut atomic.Int64
	healthLagMax    atomic.Int64 // microseconds, the slowest Hub.Handle since the last sample

	healthFileRe = regexp.MustCompile(`^(\d{4})-(\d{2})\.jsonl$`)

	// The bucket sizes a query picks from (seconds), then whole days.
	healthSteps = []int64{60, 120, 180, 300, 600, 900, 1200, 1800, 3600, 7200, 10800, 14400, 21600, 28800, 43200, 86400}
)

func init() {
	modules = append(modules, newHealthModule)
	serviceHandlers["health"] = adminHealth
}

// healthNoteLag keeps the slowest frame handling since the last sample (deferred at Hub.Handle's start).
func healthNoteLag(start time.Time) {
	d := time.Since(start).Microseconds()
	for {
		was := healthLagMax.Load()
		if d <= was || healthLagMax.CompareAndSwap(was, d) {
			return
		}
	}
}

// healthSample is one line of the log.
type healthSample struct {
	T   int64    `json:"t"`
	P   int      `json:"p"`
	W   int      `json:"w"`
	S   int      `json:"s"`
	C   *float64 `json:"c,omitempty"`
	St  *float64 `json:"st,omitempty"`
	Pc  *float64 `json:"pc,omitempty"`
	L1  *float64 `json:"l1,omitempty"`
	L5  *float64 `json:"l5,omitempty"`
	L15 *float64 `json:"l15,omitempty"`
	N   int      `json:"n"`
	Mt  *float64 `json:"mt,omitempty"`
	Mu  *float64 `json:"mu,omitempty"`
	Rss *float64 `json:"rss,omitempty"`
	Hp  *float64 `json:"hp,omitempty"`
	G   int      `json:"g"`
	Du  *float64 `json:"du,omitempty"`
	Df  *float64 `json:"df,omitempty"`
	Ni  *float64 `json:"ni,omitempty"`
	No  *float64 `json:"no,omitempty"`
	Mi  *float64 `json:"mi,omitempty"`
	Mo  *float64 `json:"mo,omitempty"`
	Lag *float64 `json:"lag,omitempty"`
}

// roundTo is x to d decimals, as a value a line may carry.
func roundTo(x float64, d int) *float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return nil
	}
	p := math.Pow10(d)
	v := math.Round(x*p) / p
	return &v
}

// —— Reading the machine ——

// cpuTimes is /proc/stat's first line: every jiffy, the idle ones (idle + iowait) and the stolen ones.
type cpuTimes struct{ total, idle, steal uint64 }

func parseProcStat(s string) (cpuTimes, bool) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		for i := 0; i < 8 && i+1 < len(f); i++ {
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			v[i] = n
		}
		// user nice system idle iowait irq softirq steal (guest is already in user).
		var t cpuTimes
		for _, n := range v {
			t.total += n
		}
		t.idle, t.steal = v[3]+v[4], v[7]
		return t, t.total > 0
	}
	return cpuTimes{}, false
}

func parseLoadavg(s string) (l1, l5, l15 float64, ok bool) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	var err [3]error
	l1, err[0] = strconv.ParseFloat(f[0], 64)
	l5, err[1] = strconv.ParseFloat(f[1], 64)
	l15, err[2] = strconv.ParseFloat(f[2], 64)
	return l1, l5, l15, err[0] == nil && err[1] == nil && err[2] == nil
}

// parseKB reads "Name:   1234 kB" lines (/proc/meminfo, /proc/self/status) into kB by name.
func parseKB(s string, names ...string) map[string]uint64 {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || !want[name] {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		if n, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			out[name] = n
		}
	}
	return out
}

// parseNetDev sums bytes received and sent over every interface but loopback.
func parseNetDev(s string) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(s, "\n") {
		name, rest, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !found || name == "lo" || strings.Contains(name, "|") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx, tx, ok = rx+r, tx+t, true
	}
	return rx, tx, ok
}

// healthProbe reads the machine and keeps the counters of the last sample, for the rates.
type healthProbe struct {
	proc string
	dir  string // the disk is this directory's filesystem

	at        time.Time
	cpu       cpuTimes
	cpuOK     bool
	rx, tx    uint64
	netOK     bool
	procCPU   time.Duration
	framesIn  int64
	framesOut int64
}

func (p *healthProbe) read(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(p.proc, name))
	return string(b), err == nil
}

func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// baseline takes the counters the first sample's rates start from.
func (p *healthProbe) baseline(now time.Time) {
	p.at = now
	if s, ok := p.read("stat"); ok {
		p.cpu, p.cpuOK = parseProcStat(s)
	}
	if s, ok := p.read("net/dev"); ok {
		p.rx, p.tx, p.netOK = parseNetDev(s)
	}
	p.procCPU = processCPU()
	p.framesIn, p.framesOut = healthFramesIn.Load(), healthFramesOut.Load()
	healthLagMax.Store(0)
}

// sample reads everything but the hub's counts (the caller fills those) and moves the baseline on.
func (p *healthProbe) sample(now time.Time) healthSample {
	smp := healthSample{T: now.Round(time.Minute).Unix(), N: runtime.NumCPU()}
	elapsed := now.Sub(p.at).Seconds()
	perMinute := func(d float64) *float64 {
		if elapsed < 1 || d < 0 {
			return nil
		}
		return roundTo(d*60/elapsed, 0)
	}
	if s, ok := p.read("stat"); ok {
		if t, ok := parseProcStat(s); ok {
			if p.cpuOK && t.total > p.cpu.total {
				dt := float64(t.total - p.cpu.total)
				idle := float64(t.idle) - float64(p.cpu.idle)
				smp.C = roundTo(math.Max(0, math.Min(100, (dt-idle)/dt*100)), 1)
				smp.St = roundTo(math.Max(0, (float64(t.steal)-float64(p.cpu.steal))/dt*100), 1)
			}
			p.cpu, p.cpuOK = t, true
		}
	}
	if cpu := processCPU(); cpu > 0 && elapsed >= 1 {
		smp.Pc = roundTo(math.Max(0, (cpu-p.procCPU).Seconds()/elapsed/float64(smp.N)*100), 1)
		p.procCPU = cpu
	}
	if s, ok := p.read("loadavg"); ok {
		if l1, l5, l15, ok := parseLoadavg(s); ok {
			smp.L1, smp.L5, smp.L15 = roundTo(l1, 2), roundTo(l5, 2), roundTo(l15, 2)
		}
	}
	if s, ok := p.read("meminfo"); ok {
		m := parseKB(s, "MemTotal", "MemAvailable")
		if total, ok := m["MemTotal"]; ok && total > 0 {
			smp.Mt = roundTo(float64(total)/1024, 0)
			if avail, ok := m["MemAvailable"]; ok && avail <= total {
				smp.Mu = roundTo(float64(total-avail)/1024, 0)
			}
		}
	}
	if s, ok := p.read("self/status"); ok {
		if rss, ok := parseKB(s, "VmRSS")["VmRSS"]; ok {
			smp.Rss = roundTo(float64(rss)/1024, 1)
		}
	}
	ms := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/sched/goroutines:goroutines"}}
	metrics.Read(ms)
	if ms[0].Value.Kind() == metrics.KindUint64 {
		smp.Hp = roundTo(float64(ms[0].Value.Uint64())/(1<<20), 1)
	}
	if ms[1].Value.Kind() == metrics.KindUint64 {
		smp.G = int(ms[1].Value.Uint64())
	}
	if total, free, used, ok := diskOf(p.dir); ok && total > 0 {
		smp.Du, smp.Df = roundTo(float64(used)/(1<<30), 1), roundTo(float64(free)/(1<<30), 1)
	}
	if s, ok := p.read("net/dev"); ok {
		if rx, tx, ok := parseNetDev(s); ok {
			if p.netOK && rx >= p.rx && tx >= p.tx {
				smp.Ni, smp.No = perMinute(float64(rx-p.rx)), perMinute(float64(tx-p.tx))
			}
			p.rx, p.tx, p.netOK = rx, tx, true
		}
	}
	in, out := healthFramesIn.Load(), healthFramesOut.Load()
	smp.Mi, smp.Mo = perMinute(float64(in-p.framesIn)), perMinute(float64(out-p.framesOut))
	p.framesIn, p.framesOut = in, out
	if lag := healthLagMax.Swap(0); lag > 0 {
		smp.Lag = roundTo(float64(lag)/1000, 1)
	}
	p.at = now
	return smp
}

// diskOf is the size, the space free to this user and the space used on dir's filesystem, in bytes.
func diskOf(dir string) (total, free, used uint64, ok bool) {
	if dir == "" {
		return 0, 0, 0, false
	}
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, 0, 0, false
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, (uint64(st.Blocks) - uint64(st.Bfree)) * bs, true
}

// —— The sampler ——

type healthLog struct {
	h     *Hub
	dir   string
	probe *healthProbe
	stop  chan struct{}

	mu   sync.Mutex
	last *healthSample
}

type healthModule struct{ l *healthLog }

func newHealthModule(h *Hub) Module {
	dir := statePath(healthDirForTest, healthDirFlag, "health")
	if dir == "" {
		return &healthModule{}
	}
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		err = os.Chmod(dir, 0o700)
	}
	if err != nil {
		log.Printf("health %s: %v (no health samples are kept)", dir, err)
		return &healthModule{}
	}
	l := &healthLog{h: h, dir: dir, probe: &healthProbe{proc: healthProcRoot, dir: dir}, stop: make(chan struct{})}
	l.probe.baseline(time.Now())
	if !testing.Testing() || healthRunForTest {
		go l.run()
	}
	return &healthModule{l: l}
}

func (m *healthModule) Register(r *Registry) {
	if m.l == nil {
		return
	}
	r.Provide("health.self", m.l)
	r.OnShutdown(func() {
		select {
		case <-m.l.stop:
		default:
			close(m.l.stop)
		}
	})
}

// healthOf is the hub's health log (nil when none is kept).
func (h *Hub) healthOf() *healthLog {
	l, _ := h.provided["health.self"].(*healthLog)
	return l
}

// run takes a sample on every minute until shutdown.
func (l *healthLog) run() {
	for {
		now := time.Now()
		t := time.NewTimer(now.Truncate(healthEvery).Add(healthEvery).Sub(now))
		select {
		case <-l.stop:
			t.Stop()
			return
		case <-t.C:
		}
		l.take(time.Now())
	}
}

// take reads one sample now and appends it to its month's file.
func (l *healthLog) take(now time.Time) healthSample {
	l.h.mu.Lock()
	players := 0
	for _, cl := range l.h.clients {
		if !cl.closed {
			players++
		}
	}
	streets, web := len(l.h.streets), 0
	for _, tabs := range l.h.webs {
		web += len(tabs)
	}
	l.h.mu.Unlock()

	smp := l.probe.sample(now)
	smp.P, smp.W, smp.S = players, web, streets
	l.mu.Lock()
	l.last = &smp
	l.mu.Unlock()
	if err := l.append(smp); err != nil {
		log.Printf("health: %v", err)
	}
	return smp
}

func (l *healthLog) append(smp healthSample) error {
	b, err := json.Marshal(smp)
	if err != nil {
		return err
	}
	path := filepath.Join(l.dir, time.Unix(smp.T, 0).UTC().Format("2006-01")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// months are the month files, oldest first, as their first second (UTC) and path.
func (l *healthLog) months() []healthMonth {
	entries, _ := os.ReadDir(l.dir)
	var out []healthMonth
	for _, e := range entries {
		m := healthFileRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if mo < 1 || mo > 12 {
			continue
		}
		out = append(out, healthMonth{start: time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC), path: filepath.Join(l.dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

type healthMonth struct {
	start time.Time
	path  string
}

// lineTime is a line's t without decoding the rest (every line starts {"t":<digits>).
func lineTime(b []byte) (int64, bool) {
	rest, ok := bytes.CutPrefix(b, []byte(`{"t":`))
	if !ok {
		return 0, false
	}
	n := 0
	for n < len(rest) && n < 12 && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	t, err := strconv.ParseInt(string(rest[:n]), 10, 64)
	return t, err == nil && n > 0
}

// scan calls fn with each sample in [since, until), oldest first (a line that does not parse is skipped).
func (l *healthLog) scan(since, until int64, fn func(*healthSample)) {
	for _, m := range l.months() {
		if m.start.AddDate(0, 1, 0).Unix() <= since || m.start.Unix() >= until {
			continue
		}
		f, err := os.Open(m.path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 256<<10), 1<<20) // big reads: a year is about 100 MB of lines
		for sc.Scan() {
			b := sc.Bytes()
			if t, ok := lineTime(b); !ok || t < since || t >= until {
				continue
			}
			if smp, ok := parseHealthLine(b); ok {
				fn(&smp)
			}
		}
		f.Close()
	}
}

// parseHealthLine reads a line as the sampler writes it (flat, numbers only) about ten
// times faster than encoding/json, which a year of lines would otherwise take seconds over;
// anything else goes through encoding/json.
func parseHealthLine(b []byte) (healthSample, bool) {
	var s healthSample
	i, n := 0, len(b)
	if n < 2 || b[0] != '{' {
		return s, false
	}
	i = 1
	for i < n && b[i] != '}' {
		if b[i] != '"' {
			return slowHealthLine(b)
		}
		k := i + 1
		for k < n && b[k] != '"' {
			k++
		}
		if k+1 >= n || b[k+1] != ':' {
			return slowHealthLine(b)
		}
		key := b[i+1 : k]
		v := k + 2
		e := v
		for e < n && b[e] != ',' && b[e] != '}' {
			e++
		}
		f, err := strconv.ParseFloat(string(b[v:e]), 64)
		if err != nil {
			return slowHealthLine(b)
		}
		switch string(key) {
		case "t":
			s.T = int64(f)
		case "p":
			s.P = int(f)
		case "w":
			s.W = int(f)
		case "s":
			s.S = int(f)
		case "n":
			s.N = int(f)
		case "g":
			s.G = int(f)
		default:
			if dst := s.field(string(key)); dst != nil {
				*dst = &f
			}
		}
		i = e
		if i < n && b[i] == ',' {
			i++
		}
	}
	return s, i < n
}

func slowHealthLine(b []byte) (healthSample, bool) {
	var s healthSample
	return s, json.Unmarshal(b, &s) == nil
}

// field is a sample's optional value by its key.
func (s *healthSample) field(key string) **float64 {
	switch key {
	case "c":
		return &s.C
	case "st":
		return &s.St
	case "pc":
		return &s.Pc
	case "l1":
		return &s.L1
	case "l5":
		return &s.L5
	case "l15":
		return &s.L15
	case "mt":
		return &s.Mt
	case "mu":
		return &s.Mu
	case "rss":
		return &s.Rss
	case "hp":
		return &s.Hp
	case "du":
		return &s.Du
	case "df":
		return &s.Df
	case "ni":
		return &s.Ni
	case "no":
		return &s.No
	case "mi":
		return &s.Mi
	case "mo":
		return &s.Mo
	case "lag":
		return &s.Lag
	}
	return nil
}

// —— The query ——

// healthCol is one column of a query's rows: a value of a sample, and how a bucket keeps it.
type healthCol struct {
	name string
	how  byte // 'x' the most, 'a' the mean, 'l' the newest
	dp   int
	get  func(*healthSample) (float64, bool)
}

func opt(v *float64) (float64, bool) {
	if v == nil {
		return 0, false
	}
	return *v, true
}

func healthInt(v int) *float64 { f := float64(v); return &f }

var healthCols = []healthCol{
	{"p", 'x', 0, func(s *healthSample) (float64, bool) { return float64(s.P), true }},
	{"pa", 'a', 1, func(s *healthSample) (float64, bool) { return float64(s.P), true }},
	{"w", 'x', 0, func(s *healthSample) (float64, bool) { return float64(s.W), true }},
	{"s", 'x', 0, func(s *healthSample) (float64, bool) { return float64(s.S), true }},
	{"c", 'a', 1, func(s *healthSample) (float64, bool) { return opt(s.C) }},
	{"cx", 'x', 1, func(s *healthSample) (float64, bool) { return opt(s.C) }},
	{"st", 'a', 1, func(s *healthSample) (float64, bool) { return opt(s.St) }},
	{"pc", 'a', 1, func(s *healthSample) (float64, bool) { return opt(s.Pc) }},
	{"l1", 'a', 2, func(s *healthSample) (float64, bool) { return opt(s.L1) }},
	{"l1x", 'x', 2, func(s *healthSample) (float64, bool) { return opt(s.L1) }},
	{"l5", 'a', 2, func(s *healthSample) (float64, bool) { return opt(s.L5) }},
	{"l15", 'a', 2, func(s *healthSample) (float64, bool) { return opt(s.L15) }},
	{"n", 'x', 0, func(s *healthSample) (float64, bool) { return float64(s.N), true }},
	{"mt", 'x', 0, func(s *healthSample) (float64, bool) { return opt(s.Mt) }},
	{"mu", 'a', 0, func(s *healthSample) (float64, bool) { return opt(s.Mu) }},
	{"rss", 'a', 1, func(s *healthSample) (float64, bool) { return opt(s.Rss) }},
	{"hp", 'a', 1, func(s *healthSample) (float64, bool) { return opt(s.Hp) }},
	{"g", 'x', 0, func(s *healthSample) (float64, bool) { return float64(s.G), true }},
	{"du", 'l', 1, func(s *healthSample) (float64, bool) { return opt(s.Du) }},
	{"df", 'l', 1, func(s *healthSample) (float64, bool) { return opt(s.Df) }},
	{"ni", 'a', 0, func(s *healthSample) (float64, bool) { return opt(s.Ni) }},
	{"no", 'a', 0, func(s *healthSample) (float64, bool) { return opt(s.No) }},
	{"mi", 'a', 0, func(s *healthSample) (float64, bool) { return opt(s.Mi) }},
	{"mo", 'a', 0, func(s *healthSample) (float64, bool) { return opt(s.Mo) }},
	{"lag", 'x', 1, func(s *healthSample) (float64, bool) { return opt(s.Lag) }},
}

// healthBucket is the smallest step that keeps a span in at most points buckets.
func healthBucket(span int64, points int) int64 {
	if points < 1 {
		points = 1
	}
	need := (span + int64(points) - 1) / int64(points)
	for _, s := range healthSteps {
		if s >= need {
			return s
		}
	}
	return (need + 86399) / 86400 * 86400
}

// healthRows buckets samples into rows of [t, healthCols…], oldest first. Samples may come in any
// order (a clock stepped back, lines appended out of order): each bucket keeps its own sums.
type healthRows struct {
	bucket  int64
	buckets map[int64]*healthAcc
	rows    [][]*float64
}

type healthAcc struct {
	n   []int
	acc []float64
	at  []int64 // for 'l' (the newest): the t its value came from
}

func newHealthRows(bucket int64) *healthRows {
	return &healthRows{bucket: bucket, buckets: map[int64]*healthAcc{}}
}

func (r *healthRows) add(s *healthSample) {
	start := s.T - ((s.T%r.bucket)+r.bucket)%r.bucket
	b := r.buckets[start]
	if b == nil {
		b = &healthAcc{n: make([]int, len(healthCols)), acc: make([]float64, len(healthCols)), at: make([]int64, len(healthCols))}
		r.buckets[start] = b
	}
	for i, c := range healthCols {
		v, ok := c.get(s)
		if !ok {
			continue
		}
		switch {
		case b.n[i] == 0:
			b.acc[i], b.at[i] = v, s.T
		case c.how == 'l':
			if s.T >= b.at[i] {
				b.acc[i], b.at[i] = v, s.T
			}
		case c.how == 'x':
			b.acc[i] = math.Max(b.acc[i], v)
		default:
			b.acc[i] += v
		}
		b.n[i]++
	}
}

// flush makes the rows, oldest first.
func (r *healthRows) flush() {
	starts := make([]int64, 0, len(r.buckets))
	for t := range r.buckets {
		starts = append(starts, t)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	r.rows = make([][]*float64, 0, len(starts))
	for _, t := range starts {
		b := r.buckets[t]
		row := make([]*float64, 0, len(healthCols)+1)
		row = append(row, healthInt(int(t)))
		for i, c := range healthCols {
			switch {
			case b.n[i] == 0:
				row = append(row, nil)
			case c.how == 'a':
				row = append(row, roundTo(b.acc[i]/float64(b.n[i]), c.dp))
			default:
				row = append(row, roundTo(b.acc[i], c.dp))
			}
		}
		r.rows = append(r.rows, row)
	}
}

func healthFields() []string {
	out := []string{"t"}
	for _, c := range healthCols {
		out = append(out, c.name)
	}
	return out
}

// healthMachine is what this machine is now (the query's machine).
func (l *healthLog) machine(now time.Time) map[string]any {
	host, _ := os.Hostname()
	m := map[string]any{"host": host, "os": runtime.GOOS + "/" + runtime.GOARCH, "cpus": runtime.NumCPU(), "version": version,
		"started": healthStarted.Unix(), "uptime": int64(now.Sub(healthStarted).Seconds()), "mem": nil, "disk": nil, "boot": nil}
	if s, ok := l.probe.read("meminfo"); ok {
		if total, ok := parseKB(s, "MemTotal")["MemTotal"]; ok {
			m["mem"] = int64(total / 1024)
		}
	}
	if total, _, _, ok := diskOf(l.dir); ok {
		m["disk"] = roundTo(float64(total)/(1<<30), 1)
	}
	if s, ok := l.probe.read("uptime"); ok {
		if f := strings.Fields(s); len(f) > 0 {
			if up, err := strconv.ParseFloat(f[0], 64); err == nil {
				m["boot"] = int64(up)
			}
		}
	}
	return m
}

func adminHealth(h *Hub, body []byte) (int, any) {
	var c struct {
		Since  int64 `json:"since"`
		Until  int64 `json:"until"`
		Points int   `json:"points"`
	}
	if json.Unmarshal(body, &c) != nil || c.Since < 0 || c.Until < 0 || c.Points < 0 || c.Points > healthPointsMax || (c.Until > 0 && c.Until <= c.Since) {
		return http.StatusBadRequest, adminBad
	}
	l := h.healthOf()
	if l == nil {
		return http.StatusOK, statusOf("off")
	}
	now := time.Now()
	if c.Until == 0 {
		c.Until = now.Unix() + 1
	}
	if c.Points == 0 {
		c.Points = healthPointsDefault
	}
	// The bucket needs the span actually kept: since 0 (or before the first sample) starts at the first.
	since, first := c.Since, l.first()
	if first > since {
		since = first
	}
	bucket := healthBucket(max(c.Until-since, 1), c.Points)
	rows := newHealthRows(bucket)
	l.scan(c.Since, c.Until, rows.add)
	rows.flush()
	l.mu.Lock()
	last := l.last
	l.mu.Unlock()
	return http.StatusOK, map[string]any{"status": "ok", "bucket": bucket, "since": c.Since, "until": c.Until, "first": first,
		"machine": l.machine(now), "now": last, "fields": healthFields(), "rows": rows.rows}
}

// first is the oldest t kept (the first line of the oldest month), 0 for none.
func (l *healthLog) first() int64 {
	for _, m := range l.months() {
		f, err := os.Open(m.path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if t, ok := lineTime(sc.Bytes()); ok {
				f.Close()
				return t
			}
		}
		f.Close()
	}
	return 0
}
