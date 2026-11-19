package ui

import (
	"bytes"
	"image/color"
	"math"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestPalettePreferredTones(t *testing.T) {
	for _, tt := range []struct{ bg, fg, accent, warning string }{
		{"#181A1F", "#E5E5E5", "#6BB8C4", "#C6A46A"},
		{"#F5F2EB", "#000000", "#226D7A", "#826021"},
	} {
		p := Palette{Profile: colorprofile.TrueColor, Background: lipgloss.Color(tt.bg), Foreground: lipgloss.Color(tt.fg)}
		s := p.Styles()
		if rgb(s.Accent.GetForeground()) != rgb(lipgloss.Color(tt.accent)) || rgb(s.Warning.GetForeground()) != rgb(lipgloss.Color(tt.warning)) {
			t.Errorf("preferred tones changed on %s: accent %v, warning %v", tt.bg, s.Accent.GetForeground(), s.Warning.GetForeground())
		}
		wantMuted := blend(rgb(p.Background), rgb(p.Foreground), 0.6)
		if rgb(s.Muted.GetForeground()) != wantMuted {
			t.Errorf("muted tone on %s = %v, want %v", tt.bg, s.Muted.GetForeground(), wantMuted)
		}
	}
}

func TestPaletteContrastAndCapabilities(t *testing.T) {
	// Include middle greys and tinted backgrounds, where simply choosing a
	// light/dark preset or rounding RGB to 256 colours can lose contrast.
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for r := 0; r <= 255; r += 51 {
			for g := 0; g <= 255; g += 51 {
				for b := 0; b <= 255; b += 51 {
					bg := color.RGBA{uint8(r), uint8(g), uint8(b), 255}
					// Deliberately include low-contrast foregrounds: our muted
					// role still needs to reach the contrast target.
					p := Palette{Profile: profile, Background: bg, Foreground: color.RGBA{128, 128, 128, 255}}
					s := p.Styles()
					for name, style := range map[string]lipgloss.Style{"accent": s.Accent, "warning": s.Warning, "muted": s.Muted, "agent": s.AgentName, "harness": s.HarnessName, "model": s.ModelName} {
						fg := style.GetForeground()
						if ratio := contrast(rgb(fg), bg); ratio < 4.5 {
							t.Errorf("%s %s on %v: contrast %.3f", profile, name, bg, ratio)
						}
						if profile == colorprofile.ANSI256 {
							if index, ok := fg.(ansi.IndexedColor); !ok || index < 16 {
								t.Errorf("256-colour output uses an unknown or unsupported colour: %T %v", fg, fg)
							}
							if strings.Contains(style.Render("text"), "38;2;") {
								t.Error("scrollback would contain unsupported RGB escapes")
							}
						}
					}
				}
			}
		}
	}
	// Independent reference values keep the contrast test from merely
	// agreeing with an incorrect implementation of luminance.
	black, white := color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255}
	if got := contrast(black, white); math.Abs(got-21) > 0.0001 {
		t.Errorf("black/white contrast = %f", got)
	}
	if got := contrast(color.RGBA{119, 119, 119, 255}, white); math.Abs(got-4.478) > 0.001 {
		t.Errorf("grey/white contrast = %f", got)
	}
}

func TestPaletteFallbacks(t *testing.T) {
	for _, p := range []Palette{
		{Profile: colorprofile.TrueColor},
		{Profile: colorprofile.ANSI256, Foreground: lipgloss.Color("#FFFFFF")},
		{Profile: colorprofile.ANSI, Background: lipgloss.Color("#FFFFFF"), Foreground: lipgloss.Color("#000000")},
	} {
		s := p.Styles()
		if s.Muted.Render("hint") != "hint" || s.Default.Render("answer") != "answer" {
			t.Error("unknown palette imposed a foreground, background or faint text")
		}
		for _, style := range []lipgloss.Style{s.Accent, s.Warning, s.AgentName, s.HarnessName, s.ModelName} {
			if !style.GetBold() || style.Render("label") != lipgloss.NewStyle().Bold(true).Render("label") {
				t.Error("unknown colour must use default foreground with emphasis")
			}
		}
		if s.Success.GetForeground() != ansi.Green || s.Error.GetForeground() != ansi.Red {
			t.Error("outcomes must use the terminal's semantic colours")
		}
	}
	for _, profile := range []colorprofile.Profile{colorprofile.Unknown, colorprofile.NoTTY, colorprofile.ASCII} {
		s := (Palette{Profile: profile, Background: lipgloss.Color("#000000"), Foreground: lipgloss.Color("#FFFFFF")}).Styles()
		for _, style := range []lipgloss.Style{s.Default, s.Muted, s.Accent, s.Success, s.Warning, s.Error, s.AgentName, s.HarnessName, s.ModelName} {
			if style.Render("text") != "text" {
				t.Errorf("%s introduced escapes", profile)
			}
		}
	}
}

func TestResumeHintRetainsPaletteAndCopyableCommand(t *testing.T) {
	s := (Palette{Profile: colorprofile.TrueColor, Background: lipgloss.Color("#181A1F"), Foreground: lipgloss.Color("#E5E5E5")}).Styles()
	var out bytes.Buffer
	s.ResumeHint(&out, "airo chat -a assistant -r session-id")
	lines := strings.Split(out.String(), "\n")
	if lines[0] != s.Muted.Render("Resume this session with:") || lines[1] != "airo chat -a assistant -r session-id" || lines[2] != "" {
		t.Fatalf("closing hint = %q", out.String())
	}
}
