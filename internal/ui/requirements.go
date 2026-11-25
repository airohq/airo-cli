package ui

import (
	"strings"

	"github.com/spf13/cobra"
)

// Requirements are the Tenant capabilities and solutions a command needs,
// in the order the feature builds on them (DESIGN.md#capabilities-and-solutions).
type Requirements struct {
	Capabilities []string
	Solutions    []string
}

// Lines are the requirements as the help shows them, one line for each kind.
func (r Requirements) Lines() string {
	var b strings.Builder
	if len(r.Capabilities) > 0 {
		b.WriteString("Required capabilities: " + strings.Join(r.Capabilities, ", ") + "\n")
	}
	if len(r.Solutions) > 0 {
		b.WriteString("Required solutions: " + strings.Join(r.Solutions, ", ") + "\n")
	}
	return b.String()
}

// Flag adds the requirements to a flag's usage, for a flag that needs more
// than its command does.
func (r Requirements) Flag(usage string) string {
	var parts []string
	if len(r.Capabilities) > 0 {
		parts = append(parts, "required capabilities: "+strings.Join(r.Capabilities, ", "))
	}
	if len(r.Solutions) > 0 {
		parts = append(parts, "required solutions: "+strings.Join(r.Solutions, ", "))
	}
	return usage + " (" + strings.Join(parts, "; ") + ")"
}

// Require puts the requirements at the top of each command's long help. A
// command without long help gets its short help below them.
func Require(r Requirements, commands ...*cobra.Command) {
	for _, c := range commands {
		long := c.Long
		if long == "" {
			long = c.Short
		}
		c.Long = r.Lines() + "\n" + long
	}
}
