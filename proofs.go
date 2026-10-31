package main

// One-use proofs outlive a restart (G335 slice 5). The site signs short-lived
// proofs that this server accepts once: a connect ticket (ticket.go, jti, 60
// s), a buddy add's "added you" (buddies.go, n, two minutes), a house key's
// tell (homes.go, n) and a tend claim (homes_help.go, n). Each kept the ids it
// had spent in memory, so a restart within a proof's life (a deploy) would
// have taken the same proof again. Now every spend goes through one journal,
// -proofs-file (default realtime-proofs.jsonl beside -secret-file; off:
// memory only), as persist.go's other state files do: one {"k":kind,"n":id,
// "exp":unix} line appended per spend, read back on start with the expired
// ones left out, and rewritten down to the live ones once it is
// proofsCompactAt lines long. Lines are written without an fsync, as the
// credits journal's adds are (what a process writes survives the process: a
// deploy's restart or a crash). A torn last line is skipped.

import (
	"bufio"
	"encoding/json"
	"flag"
	"log"
	"os"
	"sync"
	"testing"
	"time"
)

const proofsCompactAt = 4000

var proofsFileFlag = flag.String("proofs-file", "", "file that keeps the one-use proofs already spent (tickets, buddy adds, house keys, tend claims), so a restart refuses them again (default: realtime-proofs.jsonl beside -secret-file; off: memory only)")

// proofsFileForTest lets a test give the journal a file; under go test the flag is ignored.
var proofsFileForTest string

type proofLine struct {
	K   string `json:"k"`
	N   string `json:"n"`
	Exp int64  `json:"exp"`
}

// proofJournal is the spent proofs, by kind and id, until each expires.
type proofJournal struct {
	mu    sync.Mutex
	path  string // "": memory only
	f     *os.File
	lines int
	spent map[string]int64 // kind + "\x00" + id -> exp
	now   func() time.Time
}

var (
	theProofsOnce sync.Once
	theProofs     *proofJournal
)

// sharedProofs is the process's journal: the file under the flag, memory only under go test.
func sharedProofs() *proofJournal {
	if testing.Testing() && proofsFileForTest == "" {
		return newProofJournal("", time.Now)
	}
	theProofsOnce.Do(func() {
		theProofs = newProofJournal(statePath(proofsFileForTest, proofsFileFlag, "realtime-proofs.jsonl"), time.Now)
	})
	return theProofs
}

func newProofJournal(path string, now func() time.Time) *proofJournal {
	p := &proofJournal{path: path, spent: map[string]int64{}, now: now}
	if path == "" {
		return p
	}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		at := now().Unix()
		for sc.Scan() {
			var l proofLine
			if json.Unmarshal(sc.Bytes(), &l) != nil || l.K == "" || l.N == "" || l.Exp < at {
				continue
			}
			p.spent[l.K+"\x00"+l.N] = l.Exp
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		log.Printf("proofs %s: %v (kept in memory only)", path, err)
		p.path = ""
		return p
	}
	if err := p.rewriteLocked(); err != nil {
		log.Printf("proofs %s: %v (kept in memory only)", path, err)
		p.path = ""
	} else if len(p.spent) > 0 {
		log.Printf("proofs %s: %d one-use proofs still live after the restart", path, len(p.spent))
	}
	return p
}

// spend takes proof id of kind once: true the first time, false when it was spent before and
// has not expired by now (the caller's clock, unix seconds). exp is the proof's own expiry.
func (p *proofJournal) spend(kind, id string, exp, now int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.spent {
		if e < now {
			delete(p.spent, k)
		}
	}
	key := kind + "\x00" + id
	if _, seen := p.spent[key]; seen {
		return false
	}
	p.spent[key] = exp
	if p.f != nil {
		b, _ := json.Marshal(proofLine{K: kind, N: id, Exp: exp})
		if _, err := p.f.Write(append(b, '\n')); err != nil {
			log.Printf("proofs %s: %v", p.path, err)
		}
		p.lines++
		if p.lines >= proofsCompactAt {
			if err := p.rewriteLocked(); err != nil {
				log.Printf("proofs %s: %v", p.path, err)
			}
		}
	}
	return true
}

// rewriteLocked writes the live proofs to a fresh file and appends from there.
func (p *proofJournal) rewriteLocked() error {
	if p.f != nil {
		p.f.Close()
		p.f = nil
	}
	var buf []byte
	for key, exp := range p.spent {
		k, n, _ := cutNul(key)
		b, _ := json.Marshal(proofLine{K: k, N: n, Exp: exp})
		buf = append(append(buf, b...), '\n')
	}
	if err := writeFileSynced(p.path, buf); err != nil {
		return err
	}
	f, err := os.OpenFile(p.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	p.f, p.lines = f, len(p.spent)
	return nil
}

func cutNul(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
