package picker

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
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
