package store

import (
	"strconv"
	"strings"
)

// Rebind converts ? placeholders to $n for Postgres. SQLite queries pass
// through unchanged. Only ? outside of string literals is converted; our
// portable SQL never embeds literal question marks.
func Rebind(driver, query string) string {
	if driver != "postgres" {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
