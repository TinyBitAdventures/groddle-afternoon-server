# Groddle Afternoon realtime server

The Go server behind multiplayer in [Groddle Afternoon](https://github.com/TinyBitAdventures/groddle-afternoon),
a fresh take on Tiny Speck's Glitch. Play at [groddleafternoon.com](https://groddleafternoon.com).

It shares who is on each street and where they stand, chat and emotes, and the systems players
share: parties, quoins, rocks, resource nodes and trees, races, Rook attacks fought together, and
trading. Nothing here is a save: the [site](https://github.com/TinyBitAdventures/groddle-afternoon-site)
(a Minn theme and extension) keeps accounts and characters, and signs the ticket a player opens the
socket with.

## How the pieces fit

1. A signed-in player's game asks the site for a ticket (`POST /wp-json/groddle/v1/realtime`): one
   use, good for 60 seconds, signed with a secret the site and this server share.
2. The game opens a WebSocket at `https://<site>/ws`, which the web server passes to this server,
   and sends the ticket as its first frame.
3. The server checks the signature, never asks the site who anyone is, and from then on tells
   everyone on a street what the others do.

## Run it

Needs Go 1.25 or newer.

    go build -o bin/groddle-realtime .
    bin/groddle-realtime -addr 127.0.0.1:8793 -secret-file /path/to/realtime.secret

`run.sh` does both for a local site (the secret defaults to
`~/Cove/Sites/groddle.localhost/realtime.secret`; set `GRODDLE_REALTIME_SECRET_FILE` and
`GRODDLE_REALTIME_ADDR` to change them).

Options:

| Flag | Default | What it does |
|---|---|---|
| `-addr` | `127.0.0.1:8793` | Where to listen |
| `-secret-file` | | The shared secret, at least 32 bytes. `GRODDLE_REALTIME_SECRET` works instead |
| `-origins` | | Extra page origins allowed to connect (host patterns, comma separated). The socket's own host is always allowed |
| `-trade-journal` | `realtime-trades.jsonl` beside the secret | Keeps finished trades until both players' games have applied them. `off` keeps them in memory only |
| `-ledger-url` | | The Groddle site trades settle in (`POST groddle/v1/ledger/trade`, signed with the secret). Without it trades are refused |
| `-ledger-ca` | | A PEM file of extra roots for that site's certificate (a local Caddy CA) |
| `-trade-ledger` | `on` | `off` brings back the client-trusted swap, for development only |
| `-projects-file` | `realtime-projects.json` beside the secret | Keeps street projects (who put in how much, the contributions waiting for the ledger, the rewards not yet applied) across restarts. `off` keeps them in memory only |
| `-feats-file` | `realtime-feats.json` beside the secret | Keeps the shared Feat (who put in how much, who contributed to and was rewarded for each Feat, the rewards waiting for the ledger or an ack) across restarts. `off` keeps it in memory only |

The secret is the same file the site reads (`realtime.secret` beside its webroot by default). Make
one with `openssl rand -hex 32 > realtime.secret` and keep it outside any webroot.

`GET /healthz` answers `{"players":N,"streets":N}`.

### As a service

- Linux (the live host): `deploy/groddle-realtime.service` is a systemd unit (its header says how to install
  it). It restarts the server if it stops and keeps the trade journal in `/var/lib/groddle-realtime/`.
- macOS (a local Cove site): `deploy/install-local.sh [SRC]` builds SRC (default: this checkout), installs the
  binary in `~/Cove/Sites/groddle.localhost/realtime/` and loads the launchd agent
  `com.groddleafternoon.realtime`, which starts at login and restarts the server if it stops. Logs go to
  `realtime/realtime.log` there. Run it again after a change; `launchctl kickstart -k gui/$(id -u)/com.groddleafternoon.realtime`
  restarts the installed build.

### Behind the site's web server

The game connects to `/ws` on the site's own host. With Caddy:

    groddleafternoon.com {
        reverse_proxy /ws 127.0.0.1:8793
        # ... the Minn site
    }

## Trust model

The server decides everything players could fight over, from data it owns: who got a quoin, how
much is left on a rock or a tree, race scores, the Rook's health and stuns, and the swap in a trade.
It never takes an amount from a client.

Each player's own rewards (iMG, currants, items, energy) are applied by that player's game to their
own save on the site, so saves are still trusted from the client. Moving inventory onto the server
is a later step. Trades are safe against loss or duplication: both sides hold their offer in their
save while the trade runs, and the journal keeps a finished trade until both have applied it.

## Protocol

JSON text frames over the WebSocket, one object per frame, `"t"` naming the kind. The core kinds
(`auth`, `join`, `move`, `chat`, `emote`, `leave`, `ping`, and the server's `hello`, `street`,
`joined`, `left`, `moved`, `said`, `emoted`, `error`, `pong`) are listed in the header of
[`hub.go`](hub.go). One character has one live session: a second socket for the same character
closes the first with code 4001. A bad or used ticket closes with 4003.

Every shared system is a module in a file of its own, with its message kinds in its header:

| File | System |
|---|---|
| `party.go` | Parties and party chat |
| `party_spaces.go` | Party Spaces: a Party Pack opens one space for the party (spent through the site's ledger), with the shared clock, the Party ATM, party quoins and the creator's kick |
| `quoins.go` | Shared street quoins: first to touch one gets it, players nearby get a shard |
| `objects.go` | Rocks, resource nodes and trees: what one player takes is gone for everyone until it comes back |
| `rook.go` | Rook attacks fought by everyone on the street |
| `races.go` | Grab 'Em Good and the finish-line races between real players |
| `trade.go` | Trading items and currants |
| `projects.go` | Street projects everyone on a street builds together: contributions through the site's ledger, the source's reward shares, the road opening for everyone |
| `feats.go` | One Feat a day for everyone playing: contributions at the source's pace, the minimum, bonus and supermegabonus goals, ranks, rewards by place with their currants through the site's ledger |
| `buddies.go` | Who on your buddy list is online and where, "added you", and the ignore hook party and trade ask |

[`modules.go`](modules.go) is how a module registers its kinds and its connect, join, leave and
offline hooks. The rules follow Tiny Speck's released server code
([glitch-GameServerJS](https://github.com/tinyspeck/glitch-GameServerJS)); each module cites the
files and lines it follows.

## Data

`data/*.json` holds what the server needs to know about the game's world: each street's quoins,
objects, Rook-ready shrines, race courses and the trade rules. They are generated from the game
repo, so the server and the game agree. With the game checked out beside this repo:

    cd ../groddle-afternoon
    node tools/build-realtime-quoins.mjs
    node tools/build-realtime-objects.mjs
    node tools/build-realtime-rook.mjs
    node tools/build-realtime-races.mjs
    node tools/build-realtime-projects.mjs
    node tools/build-realtime-feats.mjs
    node tools/build-realtime-trade.mjs

The game's tests fail when this data is out of date.

## Tests

    go vet ./...
    go test -race ./...

One test compares tree fruit caps with the game's data, reading `../groddle-afternoon` (or
`GRODDLE_GAME_DIR`), and skips when the game isn't there. `GRODDLE_ROOK_DEV=1` turns on `rook_dev`
messages that start an attack or a wing beat on demand, for testing only, and `GRODDLE_PARTY_DEV=1`
turns on `party_space_dev`, which brings a Party Space's end near. `GRODDLE_FEATS_DEV=1` turns on `feat_dev`, which ends today's Feat soon or opens another now.

## Licence

MIT, see [LICENSE](LICENSE). Groddle Afternoon is a fan project, not affiliated with or endorsed
by Tiny Speck or Slack.
