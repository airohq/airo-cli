package main

import "testing"

// Every noun takes its plural (DESIGN.md). A noun is a command with verbs
// below it.
func TestNounsTakeThePlural(t *testing.T) {
	root := newRoot()
	nouns := 0
	for _, c := range root.Commands() {
		if !c.HasSubCommands() {
			continue
		}
		nouns++
		plural := c.Name() + "s"
		if !c.HasAlias(plural) {
			t.Errorf("airo %s has no alias %s", c.Name(), plural)
			continue
		}
		got, _, err := root.Find([]string{plural, "list"})
		if err != nil {
			t.Errorf("airo %s list: %v", plural, err)
		} else if got.Parent() != c || got.Name() != "list" {
			t.Errorf("airo %s list ran %q, want the list of %s", plural, got.CommandPath(), c.Name())
		}
	}
	if nouns == 0 {
		t.Fatal("found no noun to check")
	}
}
