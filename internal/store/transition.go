package store

import (
	"fmt"

	"github.com/harpchad/td/internal/api"
)

// transition describes one edge of the status state machine defined in
// testdata/transition_cases.json. Anything absent from this table is illegal
// and answers 409.
type transition struct {
	// needsPromotable requires a priority or a due date before the move.
	needsPromotable bool
	// needsWaitingPerson requires waiting_on to reference an existing person.
	needsWaitingPerson bool
	// setsWaitingSince stamps waiting_since with the current instant.
	setsWaitingSince bool
	// clears names task fields reset to NULL by the move.
	clears []string
}

type edge struct{ from, to string }

// transitions is the complete state machine. Do not add states.
var transitions = map[edge]transition{
	{api.StatusInbox, api.StatusTodo}:    {needsPromotable: true},
	{api.StatusInbox, api.StatusDone}:    {},
	{api.StatusInbox, api.StatusDropped}: {},

	{api.StatusTodo, api.StatusDoing}:   {},
	{api.StatusTodo, api.StatusWaiting}: {needsWaitingPerson: true, setsWaitingSince: true},
	{api.StatusTodo, api.StatusDone}:    {},
	{api.StatusTodo, api.StatusDropped}: {},

	{api.StatusDoing, api.StatusTodo}:    {},
	{api.StatusDoing, api.StatusWaiting}: {needsWaitingPerson: true, setsWaitingSince: true},
	{api.StatusDoing, api.StatusDone}:    {},
	{api.StatusDoing, api.StatusDropped}: {},

	{api.StatusWaiting, api.StatusTodo}:    {clears: []string{"waiting_on", "waiting_since"}},
	{api.StatusWaiting, api.StatusDoing}:   {clears: []string{"waiting_on", "waiting_since"}},
	{api.StatusWaiting, api.StatusDone}:    {clears: []string{"waiting_on", "waiting_since"}},
	{api.StatusWaiting, api.StatusDropped}: {clears: []string{"waiting_on", "waiting_since"}},

	{api.StatusDone, api.StatusTodo}:    {clears: []string{"completed_at"}},
	{api.StatusDone, api.StatusDropped}: {},

	{api.StatusDropped, api.StatusTodo}: {},
}

// lookupTransition returns the edge from -> to, or an api.Error carrying the
// 409 body the fixture specifies.
func lookupTransition(from, to string) (transition, *api.Error) {
	t, ok := transitions[edge{from, to}]
	if ok {
		return t, nil
	}
	return transition{}, &api.Error{
		Code:    api.ErrIllegalTransition,
		From:    from,
		To:      to,
		Message: illegalMessage(from, to),
	}
}

// illegalMessage says what to do instead, rather than restating the refusal.
func illegalMessage(from, to string) string {
	switch {
	case from == api.StatusDone:
		return "reopen to todo first"
	case to == api.StatusInbox:
		return "demotion to inbox is not a thing, drop it or edit it"
	case from == api.StatusDropped:
		return "move it back to todo first"
	case from == api.StatusInbox:
		return "promote to todo first"
	default:
		return fmt.Sprintf("no path from %s to %s", from, to)
	}
}
