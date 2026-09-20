package startup

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// Reporter writes the terminal output contract. Content is mandatory;
// styling is deliberately plain so it reads the same in every terminal.
type Reporter struct {
	mu  sync.Mutex
	w   io.Writer
	uni bool
}

// NewReporter writes to w. If unicode is false, ASCII marks are used.
func NewReporter(w io.Writer, unicode bool) *Reporter {
	if w == nil {
		w = io.Discard
	}
	return &Reporter{w: w, uni: unicode}
}

func (r *Reporter) ok() string {
	if r.uni {
		return "✓"
	}
	return "OK"
}

func (r *Reporter) bad() string {
	if r.uni {
		return "✗"
	}
	return "FAILED"
}

func (r *Reporter) arrow() string {
	if r.uni {
		return "→"
	}
	return "->"
}

// Line prints one line.
func (r *Reporter) Line(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, format+"\n", args...)
}

// Header prints the banner.
func (r *Reporter) Header(version string, identities int, startupURL string) {
	r.Line("TAB ROUTER %s", version)
	if startupURL == "" {
		startupURL = "(none)"
	}
	r.Line("Identities: %d   Startup URL: %s", identities, startupURL)
}

// Result prints "<label> → <what>: <detail> ✓|✗".
func (r *Reporter) Result(label, what, detail string, passed bool) {
	mark := r.ok()
	if !passed {
		mark = r.bad()
	}
	if detail != "" {
		r.Line("%s %s %s: %s %s", label, r.arrow(), what, detail, mark)
		return
	}
	r.Line("%s %s %s %s", label, r.arrow(), what, mark)
}

// Phase prints "Verifying X... ✓" or the failure variant with details.
func (r *Reporter) Phase(name string, passed bool, details []string) {
	if passed {
		if len(details) > 0 {
			r.Line("%s %s (%s)", name, r.ok(), strings.Join(details, "; "))
		} else {
			r.Line("%s %s", name, r.ok())
		}
		return
	}
	r.Line("%s %s", name, r.bad())
	for _, d := range details {
		r.Line("  %s", d)
	}
}

// Banner prints a boxed title.
func (r *Reporter) Banner(title string) {
	r.Line("================================")
	r.Line("%s", title)
	r.Line("================================")
}

// Arrow returns the arrow glyph for callers composing their own lines.
func (r *Reporter) Arrow() string { return r.arrow() }
