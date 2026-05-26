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
// result without string-matching. Slice 21 will extend this pattern to
// every data-returning command; slice 03 wires it for `lore path` as the
// only data command that exists today.
//
// Schema (stable, additive-only): {"path": "<absolute path>"}.
func PrintPath(p string) {
	if mode == ModeJSON {
		// json.NewEncoder.Encode emits a trailing newline. Fine: it makes
		// the output `tail | head` friendly without affecting parsers.
		//
		// Encode's error is ignored: PathEnvelope has a single string
		// field, so the only realistic failure is a writer error on Out,
		// which the human-mode Fprintln below also ignores. A richer
		// future envelope with map[string]any fields could surface real
		// marshalling errors and would want to bubble them through
		// PrintError; revisit then.
		_ = json.NewEncoder(Out).Encode(PathEnvelope{Path: p})
		return
	}
	_, _ = fmt.Fprintln(Out, p)
}

// PathEnvelope is the JSON shape `lore path --json` emits. Exported so
// downstream parsers (tests, agent harnesses) can decode into a typed
// struct rather than re-spelling the schema. Additive-only: new fields
// may be added but existing fields keep their name and type.
type PathEnvelope struct {
	Path string `json:"path"`
}
