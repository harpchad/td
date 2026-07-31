package query

import "github.com/harpchad/td/internal/api"

// Row is one line of a rendered list: a task plus how far to indent it.
type Row struct {
	Task  api.Task
	Depth int
}

// Arrange turns sorted tasks into display order.
//
// Sort order and display order are not the same thing. The comparator orders
// parents; a subtask is then lifted out of its sorted position and drawn
// directly under its parent. In the home view fixture task 113 sorts seventh
// and displays fourth.
//
// A subtask whose parent is not in the result set stays where the comparator
// put it, at depth zero, which is what makes a filter that matches a child
// directly still show it.
func Arrange(tasks []api.Task) []Row {
	present := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		present[t.ID] = true
	}

	children := map[string][]api.Task{}
	for _, t := range tasks {
		if t.ParentID != nil && present[*t.ParentID] {
			children[*t.ParentID] = append(children[*t.ParentID], t)
		}
	}

	out := make([]Row, 0, len(tasks))
	for _, t := range tasks {
		if t.ParentID != nil && present[*t.ParentID] {
			continue
		}
		out = append(out, Row{Task: t})
		for _, child := range children[t.ID] {
			out = append(out, Row{Task: child, Depth: 1})
		}
	}
	return out
}
