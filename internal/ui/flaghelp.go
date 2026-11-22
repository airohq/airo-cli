package ui

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const flagSection = "airo-help-section"

// GroupFlags adds a help section. Sections appear in registration order.
func GroupFlags(cmd *cobra.Command, heading string, names ...string) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[flagSection] += heading + "\n"
	for _, name := range names {
		if err := cmd.Flags().SetAnnotation(name, flagSection, []string{heading}); err != nil {
			panic(err) // A section names a flag the command did not register.
		}
	}
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
	for title := range strings.SplitSeq(strings.TrimSuffix(order, "\n"), "\n") {
		if section := groups[title]; section != nil {
			sections = append(sections, section)
		}
	}
	return sections
}
