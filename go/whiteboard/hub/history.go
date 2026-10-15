package hub

import "whiteboard/db"

const lineageMax = 300 // revisions shown in the panel

// History is what the history panel shows, built once per change and shared
// read-only by every stream on the board.
type History struct {
	Version  int
	Pos      db.Pos
	Lineage  []RevView // the checked-out revision back to the first, newest first
	Branches []BranchView
	known    map[int64]bool
}

type RevView struct {
	db.Rev
	Current bool
	Heads   []string // branches whose head is this revision
	Forks   []string // branches that leave the lineage here
}

type BranchView struct {
	db.Branch
	Current bool
	Fork    int64 // where it leaves the checked-out lineage; 0 = on it
	Ahead   int   // its revisions past the fork
}

// Detached reports a checked-out revision that no branch head is: the next
// change starts a new branch.
func (h *History) Detached() bool { return h.Pos.Branch == 0 && h.Pos.Rev != 0 }

func (h *History) has(rev int64) bool { return h != nil && h.known[rev] }

func buildHistory(version int, pos db.Pos, revs []db.Rev, branches []db.Branch) *History {
	h := &History{Version: version, Pos: pos, known: map[int64]bool{}}
	byID := map[int64]db.Rev{}
	for _, r := range revs {
		byID[r.ID] = r
		h.known[r.ID] = true
	}

	// the lineage: checked out revision back to the first
	onLine := map[int64]int{} // revision -> index in Lineage
	for r := pos.Rev; r != 0 && len(h.Lineage) < lineageMax; r = byID[r].Parent {
		rev, ok := byID[r]
		if !ok {
			break
		}
		onLine[r] = len(h.Lineage)
		h.Lineage = append(h.Lineage, RevView{Rev: rev, Current: r == pos.Rev})
	}

	for _, b := range branches {
		v := BranchView{Branch: b, Current: b.ID == pos.Branch}
		// walk from the head back until the lineage: that is the fork point
		r, ahead := b.Head, 0
		for r != 0 {
			if _, ok := onLine[r]; ok {
				break
			}
			r = byID[r].Parent
			ahead++
		}
		if i, ok := onLine[b.Head]; ok {
			h.Lineage[i].Heads = append(h.Lineage[i].Heads, b.Name)
		} else if i, ok := onLine[r]; ok {
			v.Fork, v.Ahead = r, ahead
			h.Lineage[i].Forks = append(h.Lineage[i].Forks, b.Name)
		} else if b.Head != 0 {
			v.Ahead = ahead // forks below what the panel shows, or a separate root
		}
		h.Branches = append(h.Branches, v)
	}
	return h
}
