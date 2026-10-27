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
| `-hi-file` | `realtime-hi.json` beside the secret | Keeps the day's hi signs, the tally and the street evasion records across restarts. `off` keeps them in memory only |

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

### On a host of its own

When the site's web server cannot pass `/ws` on (managed WordPress hosting), run this server on a
host of its own and tell the site where it is: `define( 'GRODDLE_REALTIME_URL',
'wss://play.groddleafternoon.com/ws' );` in the site's `wp-config.php`. Each ticket then names that
address and the game connects there. Allow the site's pages with `-origins groddleafternoon.com`,
keep `-ledger-url https://groddleafternoon.com`, and give both sides the same secret (the site's
`GRODDLE_REALTIME_SECRET` constant, this server's `-secret-file`). With Caddy on that host:

    play.groddleafternoon.com {
        reverse_proxy /ws 127.0.0.1:8793
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
| `party_persist.go` | Parties and Party Spaces through a restart (`-party-file`): parties, invites, kicked lists, each space's clock and spends; members back within 2 minutes rejoin their party and space; a space that expired while the server was down closes as it would have; a space that cannot come back, or a spend with no answer, is given back once through the site's `ledger/spend/refund` |
| `quoins.go` | Shared street quoins: first to touch one gets it, players nearby get a shard |
| `objects.go` | Rocks, resource nodes and trees: what one player takes is gone for everyone until it comes back; Tree Poison, its antidote and a street tree's death are shared (a dead Wood Tree's Planks credited to its poisoner; a rooked tree at health 1 dies); Dirt Pile dig crews |
| `objects_pigs.go` | Street Piggies (`pig:<i>`): one mood, hunger and twice-daily tick for everyone; pet, nibble, feed and eating a tree's fruit decided by the server |
| `rook.go` | Rook attacks fought by everyone on the street |
| `races.go` | Grab 'Em Good and the finish-line races between real players; the dead (a player on a Naraka street) cannot challenge and are left out of a starting game |
| `hoghaul.go` | The Great Hog Haul between real players on races.go's challenge: sacks, hogties, the pen and three wins decided from `data/hoghaul.json`; the winner credited through credits.go |
| `trade.go` | Trading items and currants |
| `projects.go` | Street projects everyone on a street builds together: contributions through the site's ledger, the source's reward shares, the road opening for everyone |
| `group_halls.go` | The ten group halls (G331): `project_claim` claims one for a group the site confirms you're in, paying the claim currants through the signed `ledger/hall`; the claimed phase has its minutes before `project_expired` resets it; when finished the site records the group as owner and `project_built` carries `hall.group`; pending site calls live in `-projects-file` under `halls` |
| `feats.go` | One Feat a day for everyone playing: contributions at the source's pace, the minimum, bonus and supermegabonus goals, ranks, rewards by place with their currants through the site's ledger; rewards by place: 1-13 a Large Petrified Rock (petrified_rock_large.js:8), 14-26 a Medium, 1-26 feats:top_26, 27-30 The Cigar |
| `homes.go` | Visiting real players' homes: `HOME_<id>` and `HOUSE_<id>` gated by a pass the site signs, knocking and answering, house keys given and revoked live; a revoked key-holder, and anyone without a key when the owner goes offline, is walked out |
| `homes_help.go` | The owner's Butler takes a visitor's package and message from the home street through the site's signed `ledger/butler` (`butler_leave`, `butler_parcel` to the owner); relays key-holders' tending proofs (`home_tend_tell`, `home_tended`); tells an owner a hosted party (`home_hosted`) |
| `hi.go` | Hi signs between players: the daily sign, the 400 px target, infection, the bonus, the day's tally with yesterday's winner and top spreader, street evasion records, and Sneezing and Sparkle Powder reaching the street |
| `radiance.go` | Transcendental Radiation between players: range by rank and Radius cards, the rolled time, pulses every 3 s to everyone in range, the end and the Sharing the Oneness flag, from a rank the site signs (`radiance_watch`); who is meditating on a street (the orb bubble) and how many meditate together |
| `gifts.go` | Giving a stack, a written note or a wrapped Gift Box to a player on your street (`gift_give`, `gift_given`, `gift_got`), moved through the site's signed `ledger/gift` |
| `drops.go` | Stacks dropped on a public street for everyone (`ground_drop`, `ground_pickup`; `ground_state`, `ground_added`, `ground_dropped`, `ground_taken`, `ground_gone`, `ground_back`): the dropper's 100 px guard, the first pickup wins, kept by the site's signed `ledger/drop` and `ledger/drop/take`, loaded from `ledger/drops` at start; a stack left 7 days goes home |
| `verbs.go` | What one player does to another: the player menu's emotes (Hug, High five, Kiss, Moon, Splank), Soak and Play for (`verb_do`, `verb_done`, `verb_got`: same street, online, within 800 px, not blocked, no emotes in a race); Charades (the server keeps the word, hears street chat through Registry.OnChat, credits the guesser's 250 currants as why `charades`); Epic Blowout at a home with 12 players and 11 under the influence (`GRODDLE_VERBS_DEV=1`: 3 and 3) |
| `crowns.go` | Game of Crowns between real players: a ticket challenges the street, four play in a private arena, the server decides the holder, tags, locks and the clock below the play line from reported positions, and the winner's currants go through the site's signed `ledger/crowns`; a reload is a logout (out of the game, the rest play on); `crown_pending` tells a page a spent ticket's fate; owed rewards survive a restart (`-crowns-file`) |
| `credits.go` | What the server decides a player gets (currants quoins and their shards, race wins, takes from shared rocks, trees and nodes) goes to the site's ledger as signed batches (`POST /groddle/v1/ledger/credits`, idempotent per event id); at least once while the process runs (in-memory outbox, backoff 1 s to 60 s); needs `-ledger-url` |
| `follow.go` | Following another player: the ask and the leader's OK, the follower sent where the leader went (street to street and on a teleport), what ends it (any input, a private place, offline), streets followed (Stalker), teleports with followers counted at teleport_complete (Three's a Cloud, the Licensed Teleporter counters) and the Glitch Train chain |
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
    node tools/build-realtime-crowns.mjs
    node tools/build-realtime-hoghaul.mjs

The game's tests fail when this data is out of date.

## Tests

    go vet ./...
    go test -race ./...

One test compares tree fruit caps with the game's data, reading `../groddle-afternoon` (or
`GRODDLE_GAME_DIR`), and skips when the game isn't there. `GRODDLE_ROOK_DEV=1` turns on `rook_dev`
messages that start an attack or a wing beat on demand, for testing only, and `GRODDLE_PARTY_DEV=1`
turns on `party_space_dev`, which brings a Party Space's end near. `GRODDLE_FEATS_DEV=1` turns on `feat_dev`, which ends today's Feat soon or opens another now. `GRODDLE_TREES_DEV=1` lets `obj_act` carry `ms` for a poison's clock and turns on `obj_dev`, which sets a Piggy's hunger or mood or runs its tick.

State that outlives a restart: each system keeps a file beside `-secret-file` unless its flag names one, and `off` means memory only: `-trade-journal`, `-projects-file`, `-feats-file`, `-hi-file`, `-credits-file` (credits the ledger hasn't taken), `-objects-file` (dead and poisoned trees, street Piggies, rocks, nodes and trees), `-rook-file` (rooked until revived, streets resting), `-quoins-file` (gone quoins, today's grabs) `-crowns-file` (Game of Crowns rewards and wins the ledger hasn't answered) and `-party-file` (parties and Party Spaces). A bad file is moved aside and the system starts empty. Lane and dev servers must pass `off` (or their own paths) for every state flag, or they write over the live server's files.

## Licence

MIT, see [LICENSE](LICENSE). Groddle Afternoon is a fan project, not affiliated with or endorsed
by Tiny Speck or Slack.
