package postgres

import (
	"strconv"
	"strings"
)

// whereBuilder assembles a WHERE clause from conditions written with "?"
// placeholders, numbering them $1.. in order. Only fixed SQL fragments are
// ever added; user input always goes through args.
type whereBuilder struct {
	conds []string
	args  []any
}

// add appends a condition; a condition has at most one "?" and one argument.
func (w *whereBuilder) add(cond string, args ...any) {
	for _, a := range args {
		w.args = append(w.args, a)
		cond = strings.Replace(cond, "?", "$"+strconv.Itoa(len(w.args)), 1)
	}
	w.conds = append(w.conds, cond)
}

// arg registers a bare argument and returns its placeholder.
func (w *whereBuilder) arg(a any) string {
	w.args = append(w.args, a)
	return "$" + strconv.Itoa(len(w.args))
}

func (w *whereBuilder) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}
