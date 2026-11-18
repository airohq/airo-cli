package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownTableLayout(t *testing.T) {
	const table = "| Name | Count |\n| :--- | ---: |\n| alpha | 2 |\n| beta | 12 |"
	for _, tc := range []struct {
		name, source, want string
		width              int
	}{
		{"open columns", table, " Name     Count\n━━━━━━━  ━━━━━━━\n alpha        2\n───────  ───────\n beta        12", 40},
		{"wrap rows", "| Key | Notes |\n| --- | --- |\n| a | several short words here |\n| b | last |", " Key    Notes\n━━━━━  ━━━━━━━━━━━━━\n a      several\n        short words\n        here\n─────  ─────────────\n b      last", 20},
		{"center", "| A | B | C |\n| --- | :---: | ---: |\n| long | x | 3 |\n| a | wide | 100 |", " A        B        C\n━━━━━━  ━━━━━━  ━━━━━\n long     x        3\n──────  ──────  ─────\n a       wide    100", 40},
		{"narrow", table, "Name: alpha\nCount: 2\n\nName: beta\nCount: 12", 12},
		{"missing cell", "| A | B |\n| --- | --- |\n| first |", " A        B\n━━━━━━━  ━━━\n first", 40},
		{"header only", "| A | B |\n| --- | --- |", " A    B\n━━━  ━━━", 40},
		{"quote", "> | A | B |\n> | --- | --- |\n> | x | y |", "│  A    B\n│ ━━━  ━━━\n│  x    y", 40},
		{"list", "1. Table:\n\n   | A | B |\n   | --- | --- |\n   | x | y |", "1. Table:\n\n    A    B\n   ━━━  ━━━\n    x    y", 40},
		{"code", "```text\n| A | B |\n| --- | --- |\n| x | y |\n```", "  | A | B |\n  | --- | --- |\n  | x | y |", 40},
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
			for i, line := range lines {
				if ansi.StringWidth(line) > tc.width {
					t.Errorf("line overflows width %d: %q", tc.width, line)
				}
				lines[i] = strings.TrimRight(line, " ")
			}
			plain = strings.Trim(strings.Join(lines, "\n"), "\n")
			if plain != tc.want {
				t.Errorf("got\n%s\nwant\n%s", plain, tc.want)
			}
		})
	}
}

func TestMarkdownTablePreservesCellContent(t *testing.T) {
	const source = "Before.\n\n| Label | Value |\n| --- | --- |\n| **世界** | `a\\|b` |\n| Docs | [reference][ref] |\n\nAfter.\n\n[ref]: https://example.com/long/path"
	for _, width := range []int{80, 30, 12} {
		r, err := Markdown(width, Palette{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := r.Render(source)
		if err != nil {
			t.Fatal(err)
		}
		plain := ansi.Strip(out)
		for _, word := range []string{"Before.", "世界", "a|b", "reference", "https://example.com/long/path", "After."} {
			if !strings.Contains(strings.Join(strings.Fields(plain), ""), word) {
				t.Errorf("width %d lost %q:\n%s", width, word, plain)
			}
		}
		for _, line := range strings.Split(plain, "\n") {
			if ansi.StringWidth(line) > width {
				t.Errorf("width %d overflowed: %q", width, line)
			}
		}
	}
}
