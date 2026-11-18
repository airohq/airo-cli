package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRedirectedStreamsStayPlain(t *testing.T) {
	var out bytes.Buffer
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if IsTerminal(file) || IsTerminal(&out) || Interactive(file, &out) || Styled(file) || Chat(file, &out, &out) {
		t.Fatal("a character device or a buffer was treated as an interactive terminal")
	}
	styles := NewStyles(&out)
	for _, style := range []string{styles.Default.Render("text"), styles.Muted.Render("text"), styles.Accent.Render("text"), styles.Success.Render("✓ done"), styles.Warning.Render("! warning"), styles.Error.Render("✗ error")} {
		if strings.Contains(style, "\x1b") {
			t.Fatalf("plain style contains escapes: %q", style)
		}
	}
}

func TestTerminalCapabilities(t *testing.T) {
	// Also run this test under a PTY to exercise positive detection. CI with
	// no controlling terminal still checks redirected streams above.
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no controlling terminal")
	}
	defer tty.Close()
	if !IsTerminal(tty) {
		t.Skip("no usable controlling terminal")
	}
	previous, set := os.LookupEnv("NO_COLOR")
	t.Cleanup(func() {
		if set {
			os.Setenv("NO_COLOR", previous)
		} else {
			os.Unsetenv("NO_COLOR")
		}
	})
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "xterm-256color")
	if !Chat(tty, tty, tty) {
		t.Fatal("terminal chat was disabled")
	}
	var pipe bytes.Buffer
	if Chat(tty, &pipe, tty) || Chat(tty, tty, &pipe) || Interactive(&pipe, tty) {
		t.Fatal("redirected input or output enabled chat")
	}
	t.Setenv("NO_COLOR", "")
	if Styled(tty) || Chat(tty, tty, tty) {
		t.Fatal("empty NO_COLOR must disable styling and redraws")
	}
	if !Interactive(tty, tty) {
		t.Fatal("NO_COLOR must still allow plain prompts")
	}
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "dumb")
	if Styled(tty) || Chat(tty, tty, tty) {
		t.Fatal("TERM=dumb must disable styling and redraws")
	}
}

func TestMarkdownFitsAndPreservesText(t *testing.T) {
	r, err := Markdown(40)
	if err != nil {
		t.Fatal(err)
	}
	text := "# Answer\n\nSome **strong words** and a [link](https://example.com).\n\n```go\nfmt.Println(\"hello\")\n```\n\n" + strings.Repeat("long", 30)
	got, err := r.Render(text)
	if err != nil {
		t.Fatal(err)
	}
	got = Wrap(got, 40)
	plain := ansi.Strip(got)
	for _, want := range []string{"Answer", "strong words", "https://example.com", `fmt.Println("hello")`} {
		if !strings.Contains(plain, want) {
			t.Errorf("markdown %q lacks %q", plain, want)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Errorf("line exceeds width: %q", line)
		}
	}
	if strings.Contains(got, "38;2;") || strings.Contains(got, "48;2;") || strings.Contains(got, "38;5;") {
		t.Fatalf("markdown uses colours beyond the terminal palette: %q", got)
	}
}

func TestTerminalTextRemovesControls(t *testing.T) {
	got := Text("safe\x1b[2J\x1b]52;c;secret\a\x1b[31m red\x1b[0m\r\x00\nnext\tword")
	if got != "safe red\nnext\tword" {
		t.Fatalf("sanitized text = %q", got)
	}
}
