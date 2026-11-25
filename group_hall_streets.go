package main

// Group hall streets (G1312, Austin 2026-11-25): a finished hall's own street,
// open to the owning group's members only. GameServerJS:
//
//   - quests/quest.js doJobEffects 1846-1859: the finished hall's location is
//     named after its group ("A Private Group" for a private one) and the
//     group becomes its owner (pols_setOwner, group.addPol); 1860-1863 open
//     the signpost to it (jobs_modify_connections).
//   - locations/inc_pols.js pols_canEnter 286-349: a group-owned location
//     lets in a member (this.owner.is_member(pc)); anyone else is told
//     "The location is the property of <b>…</b>, and you are not a member."
//     (players/inc_houses.js houses_familiar_private_group 497-503) and kept out.
//
// Each hall job's street (projJob.Street, the job's target_street) is the
// hall's street; the game draws it (public/js/group-hall-street.js). Who is a
// member is the site's word: the groups list it signs (channels.go's watch,
// groddle/v1/groups), so the gate never takes a client's word for it. Which
// group owns the hall is this server's (group_halls.go, r.Hall.Owned).
// A member who leaves the group sends a fresh list, and is walked out of the
// hall then (hallRecheck); a list that runs out without a fresh one keeps
// them out of the hall on their next join.
//
// Server to client:
//
//	{"t":"error","code":"hall_members","message":"…"}   the join refused (a non-member, or a hall not built)
//	{"t":"hall_out","job":"692","text":"…"}              walked out of a hall you are no longer a member of

// Words players read: inc_houses.js:500, or ours.
const (
	hallTextNotMemberA = "The location is the property of " // inc_houses.js:500 (without its <b>)
	hallTextNotMemberB = ", and you are not a member."      // inc_houses.js:500
	hallTextNotBuilt   = "This group hall isn't built yet." // ours: the source had no street before the job was done
)

// hallStreetJob is the hall job whose street this is ("" for any other street). A street id's
// first letter (G or L) is the same street.
func (m *projects) hallStreetJob(street string) string {
	if len(street) < 2 {
		return ""
	}
	for id, job := range m.t.Jobs {
		if job.Kind == "hall" && len(job.Street) > 1 && job.Street[1:] == street[1:] {
			return id
		}
	}
	return ""
}

// hallMember: the site's signed groups list (channels.go) has id in group.
func (m *projects) hallMember(id, group int64) bool {
	fn, _ := m.h.ProvidedLocked("channels.inGroup").(func(int64, int64) bool)
	return fn != nil && fn(id, group)
}

// hallRefusal is why cl may not stand in the hall of job jobID ("" when they may).
func (m *projects) hallRefusal(cl *Client, jobID string) string {
	r := m.st.Jobs[jobID]
	if r == nil || r.Hall == nil || !r.Hall.Owned {
		return hallTextNotBuilt
	}
	if m.hallMember(cl.ID(), r.Hall.Group.ID) {
		return ""
	}
	return hallTextNotMemberA + r.Hall.Group.shown() + hallTextNotMemberB
}

// hallGate is pols_canEnter for a hall's street (GateJoin).
func (m *projects) hallGate(cl *Client, street string) (bool, string, string) {
	jobID := m.hallStreetJob(street)
	if jobID == "" {
		return true, "", ""
	}
	if why := m.hallRefusal(cl, jobID); why != "" {
		return false, "hall_members", why
	}
	return true, "", ""
}

// hallRecheck: cl's groups list changed (channels.go): out of a hall they are no longer a member of.
func (m *projects) hallRecheck(cl *Client) {
	jobID := m.hallStreetJob(cl.Street())
	if jobID == "" {
		return
	}
	why := m.hallRefusal(cl, jobID)
	if why == "" {
		return
	}
	m.h.leaveLocked(cl)
	m.h.SendLocked(cl, map[string]any{"t": "hall_out", "job": jobID, "text": why})
}
