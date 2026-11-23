package ui

import (
	"bytes"
	"testing"
)

func TestLinked(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{"https://skills.sh/a/b/c", "\x1b]8;;https://skills.sh/a/b/c\x07\x1b[4mpdf\x1b[24m\x1b]8;;\x07"},
		{"", "pdf"},
		{"https://x\x1b]0;evil", "pdf"},
		{"https://x y", "pdf"},
	} {
		if got := Linked(tc.address, "pdf"); got != tc.want {
			t.Errorf("Linked(%q) = %q, want %q", tc.address, got, tc.want)
		}
	}
	// A buffer is no terminal: piped output stays plain.
	if got := Link(&bytes.Buffer{}, "https://skills.sh", "pdf"); got != "pdf" {
		t.Errorf("Link to a pipe = %q, want the text alone", got)
	}
}

func TestTableMeasuresWhatACellShows(t *testing.T) {
	var out bytes.Buffer
	err := Table(&out, [][]string{
		{"NAME", "SOURCE"},
		{Linked("https://skills.sh/a/b/pdf", "pdf"), "a/b@pdf"},
		{"frontend", "a/b@frontend"},
	})
	want := "NAME       SOURCE\n" +
		Linked("https://skills.sh/a/b/pdf", "pdf") + "        a/b@pdf\n" +
		"frontend   a/b@frontend\n"
	if err != nil || out.String() != want {
		t.Errorf("Table = %q, %v, want %q", out.String(), err, want)
	}
}
