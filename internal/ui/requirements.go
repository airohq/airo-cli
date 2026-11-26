package ui

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// Explain turns the cluster's refusal of a feature into an error that names
// the requirements and keeps the cluster's message. It explains a Forbidden
// only: other errors come back as they are. The cluster decides, so this
// never reads which capabilities a tenant has
// (DESIGN.md#capabilities-and-solutions).
func (r Requirements) Explain(project, feature string, err error) error {
	if err == nil || !apierrors.IsForbidden(err) {
		return err
	}
	return r.Refused(project, feature, err)
}

// Refused is the error for a refusal the command recognised itself, such as
// a condition reason of a resource.
func (r Requirements) Refused(project, feature string, err error) error {
	return fmt.Errorf("project %s does not allow %s: ask a tenant administrator to enable %s: %w", project, feature, r.names(), err)
}

// names reads "the agents capability", or "the tunnels and local-agents
// capabilities and the adlc solution".
func (r Requirements) names() string {
	var parts []string
	if n := len(r.Capabilities); n == 1 {
		parts = append(parts, r.Capabilities[0]+" capability")
	} else if n > 1 {
		parts = append(parts, and(r.Capabilities)+" capabilities")
	}
	if n := len(r.Solutions); n == 1 {
		parts = append(parts, r.Solutions[0]+" solution")
	} else if n > 1 {
		parts = append(parts, and(r.Solutions)+" solutions")
	}
	return "the " + strings.Join(parts, " and the ")
}

func and(words []string) string {
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
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
