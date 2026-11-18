package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownListLayout(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
		width              int
	}{
		{"wrapped", "1. ordered item contains many words for wrapping", "1. ordered item\n   contains many\n   words for\n   wrapping", 18},
		{"start and digits", "9. first\n10. second item wraps here", "9. first\n10. second item\n    wraps here", 16},
		{"nested", "1. outer\n   - inner item that also needs wrapping\n2. last", "1. outer\n    • inner item\n      that also\n      needs wrapping\n2. last", 20},
		{"loose", "1. first\n\n   Another paragraph.\n\n2. second", "1. first\n\n   Another paragraph.\n\n2. second", 40},
		{"tasks", "- [x] done\n- [ ] next", "[x] done\n[ ] next", 40},
		{"quote", "> 1. first item wraps here\n> 2. second", "│ 1. first item\n│    wraps here\n│ 2. second", 18},
		{"code", "```text\n1. literal\n  2. indent\n```", "  1. literal\n    2. indent", 40},
		{"surrounding text", "Before.\n\n1. First\n2. Second\n\nAfter.", "Before.\n\n1. First\n2. Second\n\nAfter.", 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Markdown(tc.width, Palette{})
			if err != nil {
				t.Fatal(err)
			}
			out, err := r.Render(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			plain := ansi.Strip(out)
			lines := strings.Split(plain, "\n")
			for i := range lines {
				lines[i] = strings.TrimRight(lines[i], " ")
			}
			plain = strings.Trim(strings.Join(lines, "\n"), "\n")
			if plain != tc.want {
				t.Errorf("got\n%s\nwant\n%s", plain, tc.want)
			}
		})
	}
}

func TestMarkdownNumberedMarkerColour(t *testing.T) {
	p := Palette{Profile: colorprofile.TrueColor, Background: lipgloss.Color("#181A1F")}
	r, err := Markdown(40, p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render("9. Plain with `code`\n10. [Docs][ref]\n\n[ref]: https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	plain, colours := renderedColours(t, out)
	for _, word := range []string{"9.", "10.", "Docs", "https://example.com"} {
		at := strings.Index(plain, word)
		if at < 0 {
			t.Fatalf("lost %q in %q", word, plain)
		}
		for _, c := range colours[at : at+len(word)] {
			if c == nil || rgb(c) != rgb(r.colours.link) {
				t.Errorf("%q colour = %v", word, c)
			}
		}
	}
	if colours[strings.Index(plain, "Plain")] != nil {
		t.Fatal("marker colour leaked into item text")
	}
}
