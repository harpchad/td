package tui

// The keymap is vim-flavored and identical in the web UI. Keys the phase does
// not implement yet are listed here anyway and answer with a line in the
// status bar rather than doing nothing, because a key that silently does
// nothing reads as a bug.
type binding struct {
	keys []string
	help string
	// hint is the label in the bottom bar. Empty keeps it off the bar.
	hint string
	// when says where an unimplemented key lands. It is a sentence rather
	// than a phase number because BUILD-SPEC.md section 16 does not schedule
	// every one of them, and a number invented here becomes a promise the
	// build order never made. Empty means the key works.
	when string
}

// bindings is the whole keymap, in the order the help screen lists it.
var bindings = []binding{
	{keys: []string{"j", "down"}, help: "move down"},
	{keys: []string{"k", "up"}, help: "move up"},
	{keys: []string{"g", "home"}, help: "jump to the top"},
	{keys: []string{"G", "end"}, help: "jump to the bottom"},
	{keys: []string{"ctrl+d"}, help: "half a page down"},
	{keys: []string{"ctrl+u"}, help: "half a page up"},
	{keys: []string{"enter"}, help: "open the detail view"},
	{keys: []string{"space"}, help: "toggle done"},
	{keys: []string{"a"}, help: "add a task", hint: "a add"},
	{keys: []string{"d"}, help: "mark done", hint: "d done"},
	{keys: []string{"x"}, help: "drop"},
	{keys: []string{"z"}, help: "fold the row under the cursor", hint: "z fold"},
	{keys: []string{"Z"}, help: "fold every parent in view"},
	// Listed here rather than with the other deferred keys so the bottom bar
	// reads in the order section 11 draws it.
	{keys: []string{"w"}, help: "waiting on someone", hint: "w wait", when: "with people, in phase 6"},
	{keys: []string{"/"}, help: "edit the filter", hint: "/ search"},
	{keys: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, help: "saved filters"},
	{keys: []string{"u"}, help: "undo", hint: "u undo"},
	{keys: []string{"r"}, help: "reload"},
	{keys: []string{"?"}, help: "this help", hint: "? keys"},
	{keys: []string{"esc"}, help: "back"},
	{keys: []string{"q", "ctrl+c"}, help: "quit"},

	// Specified in section 11, not built yet. Each says where it lands.
	//
	// Editing a task has no phase in section 16's build order: it is not in
	// phase 3's list and phase 4 is parity with phase 3. That is a gap in the
	// plan rather than something waiting its turn, so these say so instead of
	// naming a phase nobody committed to.
	{keys: []string{"e"}, help: "edit", when: "not scheduled yet"},
	{keys: []string{"p"}, help: "set priority", when: "not scheduled yet"},
	{keys: []string{"t"}, help: "tags", when: "not scheduled yet"},
	{keys: []string{"s"}, help: "snooze", when: "with reminders, in phase 5"},
	{keys: []string{"@"}, help: "people", when: "with people, in phase 6"},
	{keys: []string{"E"}, help: "edit the series", when: "with recurrence, in phase 7"},
}

// deferredKeys maps a key to where it lands, so pressing one says so instead
// of doing nothing.
var deferredKeys = func() map[string]binding {
	out := map[string]binding{}
	for _, b := range bindings {
		if b.when == "" {
			continue
		}
		for _, k := range b.keys {
			out[k] = b
		}
	}
	return out
}()

// hints are the bottom bar labels, in order. The bar is a real toolbar in
// both clients: every entry is clickable and runs the key it names.
func hints() []binding {
	out := make([]binding, 0, 8)
	for _, b := range bindings {
		if b.hint != "" {
			out = append(out, b)
		}
	}
	return out
}
