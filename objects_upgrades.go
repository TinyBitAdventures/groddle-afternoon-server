package main

import (
	"sort"
	"strings"
)

// A built street upgrade's layout (player report #222/#232). Once a street's upgrade project is
// built, the game plans the street from js/street-jobs.js upgradeEcologyEntry (street-jobs-feature
// ecologyOverride): more animals, mining rocks, a shrine, so every planned spot after them moves
// (Nightshade Woods: its second Chicken stands at x 4251 instead of 2531). The server lays such a
// street out from the matching row of data/objects.json's upgrades (tools/build-realtime-objects.mjs
// upgradeLayouts) from the moment the project is built (projects.go, "projects.upgrade"), so it
// checks reach where every page draws the animal.
//
// Live objects keep their state across the switch: each is moved to its id's spot in the new layout
// (an upgrade only adds ids; one the new layout lacks is dropped).

// layoutOf is the street whose layout (data/objects.json) a street's objects stand on: its own, or
// for a Party Space's instance (PARTY_<id>, party_spaces.go) its template street's (#576). Glitch made
// each party space an instance of its template's real items, shared by everyone in it. The live
// objects stay under the instance's own street (o.streets), so two parties of one template never
// share a rock, and they go when the space ends (dropStreet).
func (o *objects) layoutOf(street string) string {
	if !partyInstance(street) {
		return street
	}
	if fn, ok := o.h.ProvidedLocked("party.spaceStreet").(func(string) string); ok {
		if t := fn(street); t != "" {
			return t
		}
	}
	return street
}

// partyInstance: the street is a Party Space's instance (PARTY_<id>).
func partyInstance(street string) bool { return strings.HasPrefix(street, partySpaceStreet) }

// upgradeOf is the option of the built street upgrade on a street ("" when none is built).
func (o *objects) upgradeOf(street string) string {
	street = o.layoutOf(street)
	if o.w.upgrades[street] == nil {
		return ""
	}
	fn, _ := o.h.ProvidedLocked("projects.upgrade").(func(string) string)
	if fn == nil {
		return ""
	}
	return fn(street)
}

// variant is the street's upgraded layout now, or nil for its own.
func (o *objects) variant(street string) *objVariant {
	if opt := o.upgradeOf(street); opt != "" {
		return o.w.upgrades[o.layoutOf(street)][opt]
	}
	return nil
}

// laidUnknown marks a street whose live objects came from the state file: the first look moves them
// to the layout that stands.
const laidUnknown = "\x00"

// spots is the street's layout now (its own, or its built upgrade's), by id. A street whose live
// objects were laid out for another layout (laidFor, absent for its own) has them moved first.
func (o *objects) spots(street string) map[string]*objSpot {
	layout := o.layoutOf(street)
	spots, key := o.w.streets[layout], ""
	if opt := o.upgradeOf(street); opt != "" && o.w.upgrades[layout][opt] != nil {
		spots, key = o.w.upgrades[layout][opt].spots, opt
	}
	if o.laidFor[street] != key {
		o.relayout(street, spots)
		if key == "" {
			delete(o.laidFor, street)
		} else {
			o.laidFor[street] = key
		}
	}
	return spots
}

// relayout moves a street's live objects to their ids' spots in a new layout.
func (o *objects) relayout(street string, spots map[string]*objSpot) {
	for id, ob := range o.streets[street] {
		spot := spots[id]
		if spot == nil || spot.kind != ob.spot.kind {
			o.dropLive(ob)
			delete(o.streets[street], id)
			continue
		}
		ob.spot = spot
	}
	if len(o.streets[street]) == 0 {
		delete(o.streets, street)
	}
}

// dropLive stops an object's timers before it leaves the street's live objects.
func (o *objects) dropLive(ob *liveObj) {
	for _, stop := range []func() bool{ob.stop, ob.resetStop, ob.rookStop, ob.poisonStop, ob.plopStop, ob.tripStop, ob.incStop} {
		if stop != nil {
			stop()
		}
	}
}

// spotOf is the spot an id names on a street now, or nil.
func (o *objects) spotOf(street, id string) *objSpot {
	return o.spots(street)[id]
}

// trunksOf is the street's tree x now (a Piggy never stops to root by one, pigpath.go).
func (o *objects) trunksOf(street string) []float64 {
	if v := o.variant(street); v != nil {
		return v.trunks
	}
	return o.w.trunks[o.layoutOf(street)]
}

// sadOn: a Piggy is sad on the street now (locationIsDepressing, for its layout's Piggies).
func (o *objects) sadOn(street string) bool {
	if v := o.variant(street); v != nil {
		return v.sad
	}
	return o.w.sad[o.layoutOf(street)]
}

// anySpot is an id's spot in any of a street's layouts (a saved object from before a restart, put
// back before the projects say which layout stands; spots moves it to the right one).
func (o *objects) anySpot(street, id string) *objSpot {
	street = o.layoutOf(street)
	if s := o.w.streets[street][id]; s != nil {
		return s
	}
	opts := make([]string, 0, len(o.w.upgrades[street]))
	for opt := range o.w.upgrades[street] {
		opts = append(opts, opt)
	}
	sort.Strings(opts)
	for _, opt := range opts {
		if s := o.w.upgrades[street][opt].spots[id]; s != nil {
			return s
		}
	}
	return nil
}

// dropStreet forgets everything a street's objects hold: a Party Space's instance when the space ends
// (party_spaces.go deleteSpace), so the next party on its template starts with fresh rocks.
func (o *objects) dropStreet(street string) {
	for _, ob := range o.streets[street] {
		o.dropLive(ob)
	}
	delete(o.streets, street)
	delete(o.laidFor, street)
	delete(o.guanoAt, street)
	if stop := o.guanoStop[street]; stop != nil {
		stop()
		delete(o.guanoStop, street)
	}
	delete(o.captures, street)
	o.saveSoon()
}
