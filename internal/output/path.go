package output

import "fmt"

// PrintPath writes a bare path line with no styling. `lore path` is a
// scripting helper meant to be consumed by `cd "$(lore path foo)"` and
// similar; ANSI codes would corrupt that.
func PrintPath(p string) {
	_, _ = fmt.Fprintln(Out, p)
}
