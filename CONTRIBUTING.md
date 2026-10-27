# Contributing to the Groddle Afternoon realtime server

Issues and pull requests are welcome. A multiplayer bug, a shared system that behaves differently from the original, a crash: [open an issue](https://github.com/TinyBitAdventures/groddle-afternoon-server/issues), or [send the fix](https://github.com/TinyBitAdventures/groddle-afternoon-server/pulls).

This repo is the Go server behind multiplayer: presence, chat, parties and groups, and the parts of the world players share (quoins, rocks, races, Rook attacks, trades). Each shared system is a module of its own file that registers with the hub (`modules.go`). The game is [groddle-afternoon](https://github.com/TinyBitAdventures/groddle-afternoon) and the website is [groddle-afternoon-site](https://github.com/TinyBitAdventures/groddle-afternoon-site); https://groddleafternoon.com/code/ shows what is in each.

## Run it locally

Clone the three repos side by side:

    git clone https://github.com/TinyBitAdventures/groddle-afternoon
    git clone https://github.com/TinyBitAdventures/groddle-afternoon-server
    git clone https://github.com/TinyBitAdventures/groddle-afternoon-site

Test and build:

    cd groddle-afternoon-server
    go vet ./...
    go test -race ./...
    go build -o bin/groddle-realtime .

`run.sh` starts it for a local site. A full multiplayer setup also needs the site running on Minn; the readme has the details.

## House rules

- **Commit subjects use Emoji-Log**: `📦 NEW:`, `👌 IMPROVE:`, `🐛 FIX:`, `📖 DOC:` or `🤖 TEST:`, then the change in plain words. The body, if any, is short `- ` bullets with the facts that matter.
- **Every change comes with a test**, and `go test -race ./...` stays green.
- **The world data in `data/` is written by the game repo's builders** (`tools/build-realtime-*.mjs`), so both sides read the same numbers. Change the builder there and rerun it, rather than editing the JSON here.
- **Solo play does not change for multiplayer work.** The server adds to the game; a player on their own never needs it.
- **Rules cite their source**: the GameServerJS file and line a number came from, in a code comment.
- **A new shared system is a module**: its own file, registered in an `init` func, using the hub's `*Locked` helpers and `h.After` for timers.

## Licences

The code is MIT (see `LICENSE`). The game data it serves comes from Tiny Speck's CC0 release of Glitch. The Glitch name and logo are Tiny Speck's and are not used.
