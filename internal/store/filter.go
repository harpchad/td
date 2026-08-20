package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/harpchad/td/internal/query"
)

// whereClause is a SQL fragment plus its bind arguments.
type whereClause struct {
	sql  string
	args []any
}

// filterContext carries the clock a filter resolves against. Date predicates
// need today's calendar date in the configured timezone, and is:snoozed needs
// the current instant.
type filterContext struct {
	today  string
	nowUTC string
}

func newFilterContext(now time.Time, loc *time.Location) filterContext {
	return filterContext{
		today:  now.In(loc).Format(query.DateLayout),
		nowUTC: now.UTC().Format(time.RFC3339),
	}
}

// buildWhere turns a parsed filter into a SQL predicate over the alias `t`.
// A nil node matches every task.
func buildWhere(n query.Node, fc filterContext) (whereClause, error) {
	if n == nil {
		return whereClause{sql: "1"}, nil
	}
	return compile(n, fc)
}

func compile(n query.Node, fc filterContext) (whereClause, error) {
	switch v := n.(type) {
	case *query.And:
		return compileJoin(v.Nodes, " AND ", fc)
	case *query.Or:
		return compileJoin(v.Nodes, " OR ", fc)
	case *query.Not:
		inner, err := compile(v.Node, fc)
		if err != nil {
			return whereClause{}, err
		}
		return whereClause{sql: "NOT (" + inner.sql + ")", args: inner.args}, nil
	case *query.Is:
		return compileIs(v, fc)
	case *query.Tag:
		return whereClause{
			sql: `EXISTS (SELECT 1 FROM task_tag tt JOIN tag g ON g.id = tt.tag_id
			              WHERE tt.task_id = t.id AND lower(g.name) = ?)`,
			args: []any{v.Name},
		}, nil
	case *query.Person:
		return compilePerson(v), nil
	case *query.Priority:
		op, err := sqlOp(v.Op)
		if err != nil {
			return whereClause{}, err
		}
		return whereClause{
			sql:  fmt.Sprintf("(t.priority IS NOT NULL AND t.priority %s ?)", op),
			args: []any{v.Value},
		}, nil
	case *query.Date:
		return compileDate(v, fc)
	case *query.Src:
		return whereClause{sql: "lower(t.source) = ?", args: []any{v.Name}}, nil
	case *query.Has:
		return compileHas(v)
	case *query.Notify:
		return whereClause{sql: "t.notify = ?", args: []any{v.Mode}}, nil
	case *query.Grp:
		return compileGrp(v), nil
	case *query.Phrase:
		return ftsClause(`"` + escapeFTS(v.Text) + `"`), nil
	case *query.Word:
		return ftsClause(`"` + escapeFTS(v.Text) + `"*`), nil
	default:
		return whereClause{}, fmt.Errorf("cannot compile %T", n)
	}
}

func compileJoin(nodes []query.Node, sep string, fc filterContext) (whereClause, error) {
	parts := make([]string, 0, len(nodes))
	var args []any
	for _, child := range nodes {
		c, err := compile(child, fc)
		if err != nil {
			return whereClause{}, err
		}
		parts = append(parts, c.sql)
		args = append(args, c.args...)
	}
	return whereClause{sql: "(" + strings.Join(parts, sep) + ")", args: args}, nil
}

func compileIs(v *query.Is, fc filterContext) (whereClause, error) {
	switch v.Value {
	case "open":
		return whereClause{sql: "t.status NOT IN ('done','dropped')"}, nil
	case "done", "todo", "doing", "waiting", "inbox", "dropped":
		return whereClause{sql: "t.status = ?", args: []any{v.Value}}, nil
	case "orphan":
		return whereClause{
			sql: `(t.parent_id IS NOT NULL AND EXISTS (
			        SELECT 1 FROM task p WHERE p.id = t.parent_id
			        AND p.status IN ('done','dropped')))`,
		}, nil
	case "snoozed":
		return whereClause{
			sql:  "(t.snooze_until IS NOT NULL AND t.snooze_until > ?)",
			args: []any{fc.nowUTC},
		}, nil
	case "deferred":
		return whereClause{
			sql:  "(t.start_at IS NOT NULL AND td_local_date(t.start_at) > ?)",
			args: []any{fc.today},
		}, nil
	case "new":
		// The same mark the list draws in the gutter, so what you can see you
		// can also ask for: is:new lists what arrived while you were elsewhere.
		return whereClause{
			sql: `EXISTS (SELECT 1 FROM task_unseen u WHERE u.task_id = t.id)`,
		}, nil
	}
	return whereClause{}, fmt.Errorf("unknown is: value %q", v.Value)
}

// compilePerson matches a person link in any role. A bare @handle also
// matches the waiting_on link, which lives on the task row rather than in
// task_person, so "everything involving Mikah" includes what you are waiting
// on them for.
func compilePerson(v *query.Person) whereClause {
	const inRole = `EXISTS (SELECT 1 FROM task_person tp JOIN person pe ON pe.id = tp.person_id
	                        WHERE tp.task_id = t.id AND lower(pe.handle) = ?%s)`
	const waiting = `EXISTS (SELECT 1 FROM person pw WHERE pw.id = t.waiting_on AND lower(pw.handle) = ?)`

	if v.Role == nil {
		return whereClause{
			sql:  "(" + fmt.Sprintf(inRole, "") + " OR " + waiting + ")",
			args: []any{v.Handle, v.Handle},
		}
	}
	if *v.Role == "waiting" {
		return whereClause{sql: waiting, args: []any{v.Handle}}
	}
	return whereClause{
		sql:  fmt.Sprintf(inRole, " AND tp.role = ?"),
		args: []any{v.Handle, *v.Role},
	}
}

func compileDate(v *query.Date, fc filterContext) (whereClause, error) {
	col := "t.due_at"
	if v.Field == "start" {
		col = "t.start_at"
	}

	switch v.Special {
	case "none":
		return whereClause{sql: col + " IS NULL"}, nil
	case "overdue":
		return whereClause{
			sql:  fmt.Sprintf("(%s IS NOT NULL AND td_local_date(%s) < ?)", col, col),
			args: []any{fc.today},
		}, nil
	}

	op, err := sqlOp(v.Op)
	if err != nil {
		return whereClause{}, err
	}
	return whereClause{
		sql:  fmt.Sprintf("(%s IS NOT NULL AND td_local_date(%s) %s ?)", col, col, op),
		args: []any{v.Value},
	}, nil
}

func compileHas(v *query.Has) (whereClause, error) {
	switch v.What {
	case "attachment":
		return whereClause{sql: "EXISTS (SELECT 1 FROM attachment a WHERE a.task_id = t.id)"}, nil
	case "notes":
		return whereClause{sql: "t.notes <> ''"}, nil
	case "sub":
		return whereClause{sql: "EXISTS (SELECT 1 FROM task c WHERE c.parent_id = t.id)"}, nil
	}
	return whereClause{}, fmt.Errorf("unknown has: value %q", v.What)
}

// compileGrp matches a direct task-to-group link or any task involving a
// member of that group, in any role including waiting_on.
func compileGrp(v *query.Grp) whereClause {
	const sqlText = `(
	  EXISTS (SELECT 1 FROM task_group tg JOIN person_group pg ON pg.id = tg.group_id
	          WHERE tg.task_id = t.id AND lower(pg.handle) = ?)
	  OR EXISTS (SELECT 1 FROM task_person tp
	             JOIN group_member gm ON gm.person_id = tp.person_id
	             JOIN person_group pg ON pg.id = gm.group_id
	             WHERE tp.task_id = t.id AND lower(pg.handle) = ?)
	  OR EXISTS (SELECT 1 FROM group_member gm2
	             JOIN person_group pg2 ON pg2.id = gm2.group_id
	             WHERE gm2.person_id = t.waiting_on AND lower(pg2.handle) = ?)
	)`
	return whereClause{sql: sqlText, args: []any{v.Name, v.Name, v.Name}}
}

func ftsClause(match string) whereClause {
	return whereClause{
		sql:  "t.rowid IN (SELECT rowid FROM task_fts WHERE task_fts MATCH ?)",
		args: []any{match},
	}
}

// escapeFTS quotes a term for FTS5. Doubling an embedded quote is the only
// escape the MATCH syntax has, and wrapping the term in quotes stops a value
// like `AND` or `p:1` from being read as query syntax.
func escapeFTS(s string) string {
	return strings.ReplaceAll(s, `"`, `""`)
}

func sqlOp(op string) (string, error) {
	switch op {
	case "=", "<=", ">=", "<", ">":
		if op == "=" {
			return "=", nil
		}
		return op, nil
	}
	return "", fmt.Errorf("unknown comparison %q", op)
}
