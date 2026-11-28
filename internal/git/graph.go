package git

// Segment is a line of the graph within a row: from the lane From at the
// row's top to the lane To at its bottom (Full), from the lane From at the
// top to the commit (Top), or from the commit to the lane To at the bottom
// (Bottom). Color is the lane whose color the line takes.
type Segment struct {
	From, To int
	Kind     SegmentKind
	Color    int
}

// SegmentKind is which part of a row a Segment crosses.
type SegmentKind uint8

const (
	SegmentFull SegmentKind = iota
	SegmentTop
	SegmentBottom
)

// GraphRow is the graph of one commit of the log: the lane of its dot and
// the lines through its row.
type GraphRow struct {
	Lane     int
	Color    int
	Segments []Segment
	// Width is the number of lanes the row uses.
	Width int
}

// Graph lays out the lanes of commits, which are in the order git log
// --topo-order or --date-order gives them, children before parents.
func Graph(commits []Commit) []GraphRow {
	type lane struct {
		hash  string
		color int
	}
	var lanes []lane // the commit each lane waits for, "" for a free lane
	nextColor := 0
	free := func() int {
		for i, l := range lanes {
			if l.hash == "" {
				return i
			}
		}
		lanes = append(lanes, lane{})
		return len(lanes) - 1
	}
	rows := make([]GraphRow, len(commits))
	for i, c := range commits {
		col := -1
		for j, l := range lanes {
			if l.hash == c.Hash {
				col = j
				break
			}
		}
		if col < 0 {
			// A commit no child shown waits for: a branch's tip.
			col = free()
			lanes[col] = lane{hash: c.Hash, color: nextColor}
			nextColor++
		}
		row := GraphRow{Lane: col, Color: lanes[col].color}
		// The lines coming in from the top: the children waiting for c,
		// which end here, and the lanes going past.
		for j, l := range lanes {
			switch {
			case l.hash == c.Hash:
				if j != col || i > 0 && hasChildAbove(rows, i, col) {
					row.Segments = append(row.Segments, Segment{From: j, To: col, Kind: SegmentTop, Color: l.color})
				}
				if j != col {
					lanes[j] = lane{}
				}
			case l.hash != "":
				row.Segments = append(row.Segments, Segment{From: j, To: j, Kind: SegmentFull, Color: l.color})
			}
		}
		// The lines going out at the bottom, to the parents.
		color := lanes[col].color
		if len(c.Parents) == 0 {
			lanes[col] = lane{}
		}
		for p, parent := range c.Parents {
			to := -1
			for j, l := range lanes {
				if l.hash == parent {
					to = j
					break
				}
			}
			lc := color
			switch {
			case p == 0:
				// The lane goes on to the first parent, even when another
				// lane waits for it too: the parent takes the leftmost lane
				// waiting for it, and the others join it there.
				to = col
				lanes[col] = lane{hash: parent, color: color}
			case to < 0:
				to = free()
				lc = nextColor
				nextColor++
				lanes[to] = lane{hash: parent, color: lc}
			default:
				lc = lanes[to].color
			}
			row.Segments = append(row.Segments, Segment{From: col, To: to, Kind: SegmentBottom, Color: lc})
		}
		for len(lanes) > 0 && lanes[len(lanes)-1].hash == "" {
			lanes = lanes[:len(lanes)-1]
		}
		w := col + 1
		for _, s := range row.Segments {
			w = max(w, s.From+1, s.To+1)
		}
		row.Width = w
		rows[i] = row
	}
	return rows
}

// FirstParentGraph lays out the lanes of commits as Graph does, except that
// a merge commit continues only toward its first parent: git log
// --first-parent still reports every parent of a commit, so lines toward the
// merged-in side branches would end nowhere — the side commits are not in
// the walk. Truncating each parents list to its first entry before Graph
// reads it keeps every commit's own Parents untouched while the layout sees
// exactly the chain the walk followed.
func FirstParentGraph(commits []Commit) []GraphRow {
	slim := make([]Commit, len(commits))
	for i, c := range commits {
		if len(c.Parents) > 1 {
			c.Parents = c.Parents[:1]
		}
		slim[i] = c
	}
	return Graph(slim)
}

// hasChildAbove reports whether the row above left a line at the bottom of
// lane col, which the commit at row i continues.
func hasChildAbove(rows []GraphRow, i, col int) bool {
	for _, s := range rows[i-1].Segments {
		if s.Kind != SegmentTop && s.To == col {
			return true
		}
	}
	return false
}
