package main

// Helpers for the state files that let what the server decides outlive a
// restart (the launchd agent restarts on every deploy). Each module that keeps
// one has a flag of its own, defaulting to a file beside -secret-file, with
// "off" for memory only, and a ForTest path (under go test the flags are
// ignored): trade.go (-trade-journal), projects.go (-projects-file),
// feats.go (-feats-file), hi.go (-hi-file), credits.go (-credits-file),
// objects.go (-objects-file). Whole-state files are written through
// writeFileSynced (projects.go: a temp file, fsync, rename), journals are
// append-only and compacted. A file that is missing starts the module empty;
// one that does not parse is moved aside (logged) and the module starts empty
// and goes on saving, so a bad file never stops the server or the next save.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// statePath is where a module keeps its state: the test's path under go test,
// else the flag's, else name beside -secret-file; "" for memory only ("off",
// or no -secret-file to sit beside).
func statePath(forTest string, flagValue *string, name string) string {
	path := forTest
	if !testing.Testing() && forTest == "" {
		if flagValue != nil {
			path = *flagValue
		}
		if path == "" {
			if f := flag.Lookup("secret-file"); f != nil && f.Value.String() != "" {
				path = filepath.Join(filepath.Dir(expandHome(f.Value.String())), name)
			}
		}
	}
	if path == "off" {
		return ""
	}
	return expandHome(path)
}

// readStateFile reads a whole-state file into v. A missing file is (false, nil):
// start empty. A file that does not parse is moved aside to <path>.bad-<unix
// seconds> and logged, and is (false, nil) too, so the module starts empty and
// its next save writes a good file. Only a file that cannot be read at all is
// an error (the caller keeps that module in memory only, as feats.go does).
func readStateFile(who, path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		aside := fmt.Sprintf("%s.bad-%d", path, time.Now().Unix())
		if rerr := os.Rename(path, aside); rerr != nil {
			return false, fmt.Errorf("%v, and it could not be moved aside: %v", err, rerr)
		}
		log.Printf("%s %s: %v (moved aside to %s; starting empty)", who, path, err, aside)
		return false, nil
	}
	return true, nil
}
