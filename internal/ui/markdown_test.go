package ui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const markdownSample = "# Heading\n\nProse with **strong** text, *emphasis* and `file.go`.\n\n" +
	"## Section\n\n### Details\n\n- Entry\n\n[Docs](https://example.com)\n\n> Quoted text.\n\n" +
	"```go\npackage main\n\n// Explain the code.\nconst answer = 42\nfunc greet() string { return \"hello\" }\n```\n\n" +
	"```diff\n-old value\n+new value\n```\n\nAfter the code.\n"

func TestMarkdownUsesAdaptivePalette(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for _, bg := range []string{"#181A1F", "#F5F2EB", "#787878"} {
			t.Run(fmt.Sprintf("%s/%s", profile, bg), func(t *testing.T) {
				t.Parallel()
				p := Palette{Profile: profile, Background: lipgloss.Color(bg), Foreground: lipgloss.Color("#B0B0B0")}
				r, err := Markdown(76, p)
				if err != nil {
					t.Fatal(err)
				}
				// Catppuccin's markup scopes and token colours stay distinct;
				// none of these should acquire the surrounding UI's accent.
				red, peach, yellow := "#F38BA8", "#FAB387", "#F9E2AF"
				green, blue, pink, purple, comment := "#A6E3A1", "#89B4FA", "#F5C2E7", "#CBA6F7", "#6C7086"
				codeText := "#CDD6F4"
				if bg != "#181A1F" {
					red, peach, yellow = "#D20F39", "#FE640B", "#DF8E1D"
					green, blue, pink, purple, comment = "#40A02B", "#1E66F5", "#EA76CB", "#8839EF", "#9CA0B0"
					codeText = "#4C4F69"
				}
				want := map[string]string{
					"Heading": red, "Section": peach, "Details": yellow, "file.go": green,
					"Docs": blue, "https://example.com": blue, "strong": red, "emphasis": red,
					"Quoted text.": pink, "Explain the code.": comment,
					"func": red, "greet": blue, "hello": green, "return": purple, "42": peach, "main": codeText,
					"old value": red, "new value": green,
					"Prose": "", "Entry": "", "After the code.": "",
				}
				// Interleaved renders must not reuse another renderer's Chroma
				// palette, including while light and dark themes run in parallel.
				for range 3 {
					got, err := r.Render(markdownSample)
					if err != nil {
						t.Fatal(err)
					}
					plain, colours := renderedColours(t, got)
					for word, hex := range want {
						var expected color.Color
						if hex != "" {
							expected = p.readable(rgb(lipgloss.Color(hex)), rgb(p.Background))
						}
						at := strings.Index(plain, word)
						if at < 0 {
							t.Fatalf("rendered Markdown lost %q: %q", word, plain)
						}
						for _, actual := range colours[at : at+len(word)] {
							if (actual == nil) != (expected == nil) || (actual != nil && expected != nil && rgb(actual) != rgb(expected)) {
								t.Fatalf("%q colour = %v, want %v", word, actual, expected)
							}
						}
					}
					for _, actual := range colours {
						if actual == nil {
							continue
						}
						if contrast(rgb(actual), rgb(p.Background)) < 4.5 {
							t.Fatalf("Markdown colour %v lost contrast on %s", actual, bg)
						}
						if profile == colorprofile.ANSI256 {
							if index, ok := actual.(ansi.IndexedColor); !ok || index < 16 {
								t.Fatalf("256-colour output used %T %v", actual, actual)
							}
						}
					}
				}
			})
		}
	}
}

func TestMarkdownFallbacksPreserveContent(t *testing.T) {
	var want string
	for _, p := range []Palette{
		{}, {Profile: colorprofile.TrueColor},
		{Profile: colorprofile.ANSI, Background: lipgloss.Color("#FFFFFF")},
		{Profile: colorprofile.TrueColor, Background: lipgloss.Color("#181A1F"), Foreground: lipgloss.Color("#E5E5E5")},
	} {
		r, err := Markdown(76, p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Render(markdownSample)
		if err != nil {
			t.Fatal(err)
		}
		plain, colours := renderedColours(t, got)
		if strings.Contains(plain, "│ │ ") || !strings.Contains(plain, "│ Quoted text.") {
			t.Fatalf("a single blockquote needs one margin marker: %q", plain)
		}
		lines := strings.Split(plain, "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " ")
		}
		plain = strings.Join(lines, "\n")
		if want == "" {
			want = plain
		} else if plain != want {
			t.Errorf("colours changed the text or spacing:\ngot  %q\nwant %q", plain, want)
		}
		if p.Background == nil || p.Profile < colorprofile.ANSI256 {
			for _, c := range colours {
				if c != nil {
					t.Fatal("unknown or limited colours forced a Markdown foreground")
				}
			}
		}
	}
}

func TestMarkdownInlineCodeDelimiters(t *testing.T) {
	r, err := Markdown(76, Palette{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Render("Use `file.go`, and `` a`b `` contains a literal backtick.")
	if err != nil {
		t.Fatal(err)
	}
	if plain := strings.TrimSpace(ansi.Strip(got)); plain != "Use file.go, and a`b contains a literal backtick." {
		t.Fatalf("inline code delimiters or content changed: %q", plain)
	}
}

// Decode actual SGR output instead of asserting a particular ordering of
// escape codes. Each visible byte records the foreground a terminal would use.
func renderedColours(t *testing.T, text string) (string, []color.Color) {
	t.Helper()
	parser := ansi.NewParser()
	var state byte
	var foreground color.Color
	var plain strings.Builder
	var colours []color.Color
	for len(text) > 0 {
		seq, width, n, next := ansi.DecodeSequence(text, state, parser)
		text, state = text[n:], next
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			params := parser.Params()
			if len(params) == 0 {
				foreground = nil
			}
			for i := 0; i < len(params); i++ {
				code := params[i].Param(0)
				switch {
				case code == 0 || code == 39:
					foreground = nil
				case code == 38:
					i += max(1, ansi.ReadStyleColor(params[i:], &foreground)) - 1
				case code >= 30 && code <= 37:
					foreground = ansi.BasicColor(code - 30)
				case code >= 90 && code <= 97:
					foreground = ansi.BasicColor(code - 90 + 8)
				case code == 48 || (code >= 40 && code <= 47) || (code >= 100 && code <= 107):
					t.Fatal("Markdown imposed its own background")
				}
			}
		} else if width > 0 || seq == "\n" || seq == "\t" {
			plain.WriteString(seq)
			for range len(seq) {
				colours = append(colours, foreground)
			}
		}
	}
	return plain.String(), colours
}
