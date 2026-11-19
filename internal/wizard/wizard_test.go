package wizard

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func TestPlainQuestions(t *testing.T) {
	var out bytes.Buffer
	w := &Wizard{lines: bufio.NewReader(strings.NewReader("\npreview\nmaybe\n\nn\n")), out: &out, header: "Creating tunnel in project team"}
	if name, err := w.Name(nil); err != nil || name != "preview" {
		t.Fatalf("name = %q, error = %v", name, err)
	}
	if public, err := w.Confirm("Public?", false); err != nil || public {
		t.Fatalf("public = %v, error = %v", public, err)
	}
	if auth, err := w.Confirm("Require authentication?", true); err != nil || auth {
		t.Fatalf("auth = %v, error = %v", auth, err)
	}
	if _, err := w.Required("Instructions"); !errors.Is(err, errCanceled) {
		t.Fatalf("EOF returned %v", err)
	}
	if !strings.HasPrefix(out.String(), "Creating tunnel in project team\n\n? Name (required) › ") ||
		strings.Count(out.String(), "Creating tunnel") != 1 || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("unexpected plain prompts %q", &out)
	}
}

func TestQuestionUsesChatAccent(t *testing.T) {
	q := &question{input: textinput.New(), label: "Name (required)"}
	q.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	for _, bg := range []string{"#181A1F", "#F5F2EB"} {
		q.Update(tea.BackgroundColorMsg{Color: lipgloss.Color(bg)})
		mark := q.palette.Styles().Accent.Render("?")
		if mark == "?" || !strings.HasPrefix(q.View().Content, mark+" Name (required) › ") {
			t.Errorf("unexpected marker on %s: %q", bg, q.View().Content)
		}
	}
}
