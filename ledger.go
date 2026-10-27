package main

// The site's ledger, seen from here (G335): the Groddle site (Minn) holds each
// multiplayer character's currants and tradeable things, and a trade both
// players accepted only happens once the site has moved both ledgers
// (groddle-afternoon-site src/LedgerService.php, src/Ledger.php). trade.go
// asks; this file is the asking.
//
//	POST <site>/wp-json/groddle/v1/ledger/trade       {"trade":"…","ids":[a,b],"gave":[Goods,Goods]}
//	     200 {"status":"settled"} | 409 {"status":"refused","short":{"id":a,"c":"apple"}} | 200 {"status":"void"}
//	POST <site>/wp-json/groddle/v1/ledger/trade/void  {"trade":"…","ids":[a,b]}
//	     200 {"status":"void"} | {"status":"settled","ids":[a,b],"gave":[Goods,Goods]}
//
// Both are idempotent per trade id: the site records a trade settled or void
// exactly once, so asking again (a lost answer, a restart) is always safe.
//
// The service identity: every call carries
//
//	X-Groddle-Service: <unix seconds>.<16 hex nonce>.<base64url HMAC-SHA256>
//
// over "groddle-service/1\n" METHOD "\n" route "\n" seconds "\n" nonce "\n"
// hex(sha256(body)) with the realtime secret, the way the site signs tickets
// the other way (RealtimeTicket::sign) but framed so the two can never be
// mistaken for each other (src/ServiceCall.php checks it, 60 s window).

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ledgerRouteTrade = "/groddle/v1/ledger/trade"
	ledgerRouteVoid  = "/groddle/v1/ledger/trade/void"
	ledgerPrefix     = "groddle-service/1"
	ledgerTimeout    = 5 * time.Second
	ledgerTries      = 3
)

var (
	ledgerURLFlag   = flag.String("ledger-url", "", "the Groddle site's origin (https://groddle.localhost): trades settle in its ledger")
	ledgerCAFlag    = flag.String("ledger-ca", "", "a PEM file of extra roots to trust for -ledger-url (Cove's local Caddy root)")
	tradeLedgerFlag = flag.String("trade-ledger", "on", "on: trades settle in the site's ledger (needs -ledger-url); off: the old client-trusted swap, for development only")
)

// ledgerShort names what a giver's ledger lacked.
type ledgerShort struct {
	ID       int64  `json:"id"`
	C        string `json:"c"`
	Currants bool   `json:"currants"`
}

// ledgerAnswer is the site's word on a trade. Status "" means no answer came.
type ledgerAnswer struct {
	Status string       `json:"status"` // settled | refused | void
	Short  *ledgerShort `json:"short,omitempty"`
	IDs    []int64      `json:"ids,omitempty"`
	Gave   []tradeGoods `json:"gave,omitempty"`
}

// ledgerSite is the site's ledger. Calls block; trade.go makes them off the hub lock.
type ledgerSite interface {
	settle(r *tradeRecord) ledgerAnswer
	void(r *tradeRecord) ledgerAnswer
}

// signService is the X-Groddle-Service value for one call.
func signService(secret []byte, method, route string, at int64, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	msg := ledgerPrefix + "\n" + strings.ToUpper(method) + "\n" + route + "\n" + strconv.FormatInt(at, 10) + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	return strconv.FormatInt(at, 10) + "." + nonce + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type httpLedger struct {
	base   string
	secret []byte
	client *http.Client
	now    func() time.Time
	pause  func(time.Duration)
}

// newLedgerFromFlags: the site's ledger the flags name; missing when trades
// must settle there but no -ledger-url was given (trades are then refused).
func newLedgerFromFlags() (site ledgerSite, missing bool) {
	if *tradeLedgerFlag == "off" {
		return nil, false
	}
	base := strings.TrimRight(strings.TrimSpace(*ledgerURLFlag), "/")
	if base == "" {
		log.Printf("trade ledger: no -ledger-url, so trading is off (-trade-ledger off brings back the client-trusted swap for development)")
		return nil, true
	}
	path := ""
	if f := flag.Lookup("secret-file"); f != nil {
		path = expandHome(f.Value.String())
	}
	secret, err := readSecret(path)
	if err != nil {
		log.Printf("trade ledger: %v (trading is off)", err)
		return nil, true
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if ca := strings.TrimSpace(*ledgerCAFlag); ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(expandHome(ca))
		if err != nil || !pool.AppendCertsFromPEM(pem) {
			log.Printf("trade ledger: -ledger-ca %s: %v (trading is off)", ca, errOr(err, "no certificates in it"))
			return nil, true
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	log.Printf("trade ledger: trades settle at %s", base)
	return &httpLedger{base: base, secret: secret, client: &http.Client{Timeout: ledgerTimeout, Transport: tr}, now: time.Now, pause: time.Sleep}, false
}

func errOr(err error, text string) error {
	if err != nil {
		return err
	}
	return errors.New(text)
}

func (l *httpLedger) settle(r *tradeRecord) ledgerAnswer {
	return l.call(ledgerRouteTrade, map[string]any{"trade": r.Trade, "ids": r.IDs, "gave": [2]any{goodsJSON(r.Gave[0]), goodsJSON(r.Gave[1])}})
}

func (l *httpLedger) void(r *tradeRecord) ledgerAnswer {
	return l.call(ledgerRouteVoid, map[string]any{"trade": r.Trade, "ids": r.IDs})
}

// call posts one signed call, trying again (same body) while no answer comes.
func (l *httpLedger) call(route string, payload any) ledgerAnswer {
	body, _ := json.Marshal(payload)
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		a, err := l.once(route, body)
		if err == nil {
			return a
		}
		log.Printf("trade ledger: %s: %v", route, err)
	}
	return ledgerAnswer{}
}

func (l *httpLedger) once(route string, body []byte) (ledgerAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+route, bytes.NewReader(body))
	if err != nil {
		return ledgerAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, route, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return ledgerAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a ledgerAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil {
		switch a.Status {
		case "settled", "refused", "void":
			return a, nil
		}
	}
	return ledgerAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}
