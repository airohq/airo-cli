package picker

import (
	"bytes"
	"image/color"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/airohq/airo-cli/internal/ui"
)

func TestInlineChoice(t *testing.T) {
	m := &menu{label: "Choose a model", names: []string{"alpha", "beta", "gamma"}, current: 1, visible: 2, filter: textinput.New()}
	m.resetMatches()
	m.Init()
	view := m.View()
	if view.AltScreen || !strings.Contains(view.Content, "› beta (current)") {
		t.Fatal(view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !strings.Contains(m.View().Content, "› gamma") {
		t.Fatal(m.View())
	}
	m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.done || !strings.Contains(m.View().Content, "No matches") {
		t.Fatal(m.View())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.Update(tea.KeyPressMsg{Code: 'A', Text: "ALP"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view = m.View()
	if !m.done || m.answer != "alpha" || view.AltScreen || view.Content != "? Choose a model › alpha" {
		t.Fatal(view)
	}
}

func TestKeyboardGuideBelowChoices(t *testing.T) {
	palette := ui.Palette{Profile: colorprofile.TrueColor, Foreground: color.White, Background: color.Black}
	m := &menu{names: []string{"claude", "kagent"}, current: -1, visible: 1, filter: textinput.New()}
	m.Update(tea.ColorProfileMsg{Profile: palette.Profile})
	m.Update(tea.ForegroundColorMsg{Color: palette.Foreground})
	m.Update(tea.BackgroundColorMsg{Color: palette.Background})
	guide := palette.Styles().Muted.Render("↑/↓ move · type to filter · Enter choose · Esc cancel")
	for _, tt := range []struct{ filter, lastRow string }{
		{"", "  1–1 of 2"},
		{"cla", palette.Styles().Accent.Render("›") + " claude"},
		{"missing", "  No matches"},
	} {
		m.filter.SetValue(tt.filter)
		m.resetMatches()
		if got := m.View().Content; !strings.HasSuffix(got, "\n"+tt.lastRow+"\n"+guide) {
			t.Errorf("filter %q: unexpected footer in %q", tt.filter, got)
		}
	}
}

func TestPlainChoice(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"\n", "beta"}, {"3\n1\n", "alpha"}, {"q\n", ""}, {"", ""},
	} {
		var out bytes.Buffer
		got, err := One(t.Context(), strings.NewReader(tt.input), &out, "context", []string{"alpha", "beta"}, 1)
		if err != nil || got != tt.want || !strings.Contains(out.String(), "2. beta (current)") || strings.Contains(out.String(), "\x1b") {
			t.Fatalf("input %q returned %q, %v; output %q", tt.input, got, err, out.String())
		}
	}
}
