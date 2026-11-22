package ui

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const flagSection = "airo-help-section"

// FlagGroup lists the flags that share a help heading.
type FlagGroup struct {
	Heading string
	Names   []string
}

// SetFlagGroups shows the selected groups in order, hiding previously shown
// groups. Flags outside any group stay under Basic Flags.
func SetFlagGroups(cmd *cobra.Command, groups ...FlagGroup) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if len(f.Annotations[flagSection]) != 0 {
			f.Hidden = true
		}
	})
	var headings []string
	for _, group := range groups {
		headings = append(headings, group.Heading)
		for _, name := range group.Names {
			if err := cmd.Flags().SetAnnotation(name, flagSection, []string{group.Heading}); err != nil {
				panic(err) // A section names a flag the command did not register.
			}
			cmd.Flags().Lookup(name).Hidden = false
		}
	}
	cmd.Annotations[flagSection] = strings.Join(headings, "\n")
}

// FlagSections returns sections for commands that opt into grouped help.
// Ungrouped flags, including Cobra's generated --help, then go under Basic Flags.
func FlagSections(cmd *cobra.Command, flags *pflag.FlagSet) []*pflag.FlagSet {
	order := cmd.Annotations[flagSection]
	if order == "" {
		return nil
	}
	basic := pflag.NewFlagSet("Basic Flags", pflag.ContinueOnError)
	groups := make(map[string]*pflag.FlagSet)
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		section := basic
		if titles := f.Annotations[flagSection]; len(titles) != 0 {
			title := titles[0]
			if groups[title] == nil {
				groups[title] = pflag.NewFlagSet(title, pflag.ContinueOnError)
			}
			section = groups[title]
		}
		section.AddFlag(f)
	})
	if len(groups) == 0 {
		return nil
	}
	var sections []*pflag.FlagSet
	if basic.HasFlags() {
		sections = append(sections, basic)
	}
	for title := range strings.SplitSeq(order, "\n") {
		if section := groups[title]; section != nil {
			sections = append(sections, section)
		}
	}
	return sections
}
