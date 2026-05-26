package output

import (
	"encoding/json"
	"fmt"
)

// PrintPath emits the path of a tracked agent dir (or the central repo).
//
// Human mode: bare path line, no styling. `lore path` is a scripting
// helper meant to be consumed by `cd "$(lore path foo)"` and similar;
// ANSI codes would corrupt that.
//
// JSON mode: a documented single-line envelope so agents can parse the
// result without string-matching. Slice 21 will extend this to cover
// every data-returning command; slice 03 wires it for `lore path` as the
// only data command that exists today.
//
// Schema (stable, additive-only):
//
//	{"path": "<absolute path>"}
func PrintPath(p string) {
	if mode == ModeJSON {
		// json.NewEncoder.Encode emits a trailing newline. Fine: it makes
		// the output `tail | head` friendly without affecting parsers.
		_ = json.NewEncoder(Out).Encode(pathEnvelope{Path: p})
		return
	}
	_, _ = fmt.Fprintln(Out, p)
}

// pathEnvelope is the JSON shape `lore path --json` emits. Kept here
// (private) because slice 21's typed-result design will likely promote
// it to an exported package-level type alongside the others.
type pathEnvelope struct {
	Path string `json:"path"`
}
