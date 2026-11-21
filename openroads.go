package main

// The open-road rule (root R11, register G1075, Austin 2026-11-21): which streets anyone can walk
// to. The game's js/open-roads.js is the same rule; testdata/open-roads.json holds cases both must
// answer the same (openroads_test.go here, the game's tests/open-roads.test.js there).
//
// Glitch locked a street with a street-building project not done for the whole world
// (locations/inc_jobs.js jobs_modify_connections 530-622: every incoming signpost and door), and
// refused teleports there, so no player stood past a locked road. The rule:
//   - a street project's street is closed unless the project is done, built from the start
//     (projJob.Built) or exempt (a subway street, a road from the starter street);
//   - the open world is every street the starter street (projTable.Start) reaches over roads
//     (projTable.Roads: signposts and doors, locked doors left out) that do not enter a closed street,
//     plus the free ways (projTable.Free: the teleporters, which no closure holds).
//
// Report #566 (G1075): the featured project was Parsin's Trail SE in Baqala, whose board street was
// built but stood in a pocket no open road joined to the world; pickFeatured had checked one
// neighbour. Now it takes only a project whose board is in the open world (projects_featured.go).
//
// The game keeps one more layer per player (a street you have stood on stays open for you, the
// game's `grandfathered`); the server never sees it, and it never makes a street open for anyone else.

// closedStreets is every street still to be built: a street project's street, not done, not built from the start, not exempt.
func (m *projects) closedStreets() map[string]bool {
	out := map[string]bool{}
	for id, job := range m.t.Jobs {
		if job.Kind != "street" || job.Built || job.Exempt {
			continue
		}
		if r := m.st.Jobs[id]; r != nil && r.Done > 0 {
			continue
		}
		out[gStreet(job.Street)] = true
	}
	return out
}

// openWorld is every street the starter street reaches with the roads as they stand, or nil when the
// table carries no roads (a table from before the rule: nothing can be said, so callers fall back).
func openWorld(t *projTable, closed map[string]bool) map[string]bool {
	start := gStreet(t.Start)
	if start == "" || len(t.Roads) == 0 {
		return nil
	}
	seen := map[string]bool{start: true}
	queue := []string{start}
	for i := 0; i < len(queue); i++ {
		at := queue[i]
		for _, to := range t.Roads[at] {
			to = gStreet(to)
			if closed[to] || seen[to] {
				continue
			}
			seen[to] = true
			queue = append(queue, to)
		}
		for _, to := range t.Free[at] {
			to = gStreet(to)
			if !seen[to] {
				seen[to] = true
				queue = append(queue, to)
			}
		}
	}
	return seen
}

// openWorldNow is the open world with this world's closed streets (nil: the table has no roads).
func (m *projects) openWorldNow() (closed, reach map[string]bool) {
	closed = m.closedStreets()
	return closed, openWorld(&m.t, closed)
}

// projectReachable: can players work on it from the open world? A street project whose board (an
// offering street) is in it, or an upgrade of a street in it. With no roads known (reach nil), every
// project passes.
func projectReachable(job projJob, reach map[string]bool) bool {
	if reach == nil {
		return true
	}
	if job.Kind == "upgrade" {
		return reach[gStreet(job.Street)]
	}
	for _, s := range job.Offer {
		if reach[gStreet(s)] {
			return true
		}
	}
	return false
}
