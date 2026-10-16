package hub

import (
	"sort"

	"whiteboard/db"
)

const (
	lineageMax = 300 // revisions shown in the panel
	thumbsMax  = 40  // the newest this many carry a thumbnail
)

// History is what the history panel shows, built once per change and shared
// read-only by every stream on the board.
type History struct {
	Version  int
	Pos      db.Pos
	Lineage  []RevView // the checked-out revision back to the first, newest first
	Branches []BranchView
	Spark    []int // shape count along the lineage, oldest first (the sparkline)
	known    map[int64]bool
}

type RevView struct {
	db.Rev
	Current bool
	Ahead   bool     // past where the board is (undone, or looked back from): the line goes on
	Heads   []string // branches whose head is this revision
	Forks   []string // branches that leave the lineage here
	Delta   int      // shapes added (+) or removed (−) by this revision
	Thumb   string   // the drawing at this revision as SVG markup, "" past thumbsMax
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

	// the line: from the head of the branch the board is on (or, looked
	// back from, the branch whose line it is on), back to the first; the
	// revisions past the board's are its future, like git's log past HEAD
	top := pos.Rev
	if pos.Rev != 0 {
		order := append([]db.Branch(nil), branches...)
		sort.SliceStable(order, func(i, j int) bool {
			return order[i].ID == pos.Branch && order[j].ID != pos.Branch || order[i].Main && !order[j].Main && order[j].ID != pos.Branch
		})
	find:
		for _, b := range order {
			for r, n := b.Head, 0; r != 0 && n < lineageMax; r, n = byID[r].Parent, n+1 {
				if r == pos.Rev {
					top = b.Head
					break find
				}
			}
		}
	}
	onLine := map[int64]int{} // revision -> index in Lineage
	ahead := top != pos.Rev
	for r := top; r != 0 && len(h.Lineage) < lineageMax; r = byID[r].Parent {
		rev, ok := byID[r]
		if !ok {
			break
		}
		onLine[r] = len(h.Lineage)
		if r == pos.Rev {
			ahead = false
		}
		h.Lineage = append(h.Lineage, RevView{Rev: rev, Current: r == pos.Rev, Ahead: ahead})
	}

	for i := range h.Lineage {
		parent := 0
		if p, ok := byID[h.Lineage[i].Parent]; ok {
			parent = p.Shapes
		}
		h.Lineage[i].Delta = h.Lineage[i].Shapes - parent
	}
	for i := len(h.Lineage) - 1; i >= 0; i-- {
		h.Spark = append(h.Spark, h.Lineage[i].Shapes)
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
