package app

import (
	"strings"
	"testing"
	"time"
)

func overlayTestState() (*Application, *tuiState) {
	accounts := NewAccounts()
	account := quotaAccount("user@example.com", 0.85, 0.45, time.Now().Add(time.Hour))
	account["name"] = "Test User"
	accounts.Set("user@example.com", account)
	a := &Application{Version: "2.8.7", p: makePalette(true), color: true}
	return a, newTUIState(accounts, "user@example.com")
}

func assertFrameBorders(t *testing.T, lines []string, marker string) {
	t.Helper()
	found := false
	for i, line := range lines {
		plain := ansiPattern.ReplaceAllString(line, "")
		if !strings.Contains(plain, marker) {
			continue
		}
		found = true
		runes := []rune(plain)
		if first, last := runes[0], runes[len(runes)-1]; !strings.ContainsRune("│├╭╰", first) || !strings.ContainsRune("│┤╮╯", last) {
			t.Fatalf("line %d lost the frame border: %q", i, plain)
		}
	}
	if !found {
		t.Fatalf("overlay marker %q missing", marker)
	}
}

func TestTUIToastKeepsFrameBorders(t *testing.T) {
	for _, width := range []int{118, 78, 46} {
		a, state := overlayTestState()
		state.showToast("Switched to user@example.com", "success")
		lines := a.tuiLines(state, width, 22)
		assertFrameBorders(t, lines, "Switched to user@example.com")
		assertFrameBorders(t, lines, "╰")
		for i, line := range lines {
			if visibleWidth(line) != width+2 {
				t.Fatalf("width=%d line %d width=%d", width, i, visibleWidth(line))
			}
		}
	}
}

func TestTUIDialogKeepsFrameBorders(t *testing.T) {
	a, state := overlayTestState()
	state.mode = tuiHelp
	lines := a.tuiLines(state, 118, 24)
	assertFrameBorders(t, lines, "╭")
	assertFrameBorders(t, lines, "╰")
}

func TestOverlayVisibleKeepsSurroundingColumns(t *testing.T) {
	p := makePalette(true)
	base := p.Gray + "│" + p.Reset + " ab" + p.Green + "界cd" + p.Reset + " │"
	got := overlayVisible(base, "XY", 3, p)
	if plain := ansiPattern.ReplaceAllString(got, ""); plain != "│ aXY cd │" {
		t.Fatalf("plain=%q", plain)
	}
	if visibleWidth(got) != visibleWidth(base) {
		t.Fatalf("width=%d want %d", visibleWidth(got), visibleWidth(base))
	}
	if !strings.HasSuffix(got, "│") && !strings.HasSuffix(got, p.Reset) {
		t.Fatalf("suffix lost: %q", got)
	}
}

func TestTUIQuotaViewShowsSelectedEmailOnce(t *testing.T) {
	a, state := overlayTestState()
	a.p = makePalette(false)
	state.view = tuiViewQuota
	state.selectedEmail = "user@example.com"
	for _, width := range []int{118, 78} {
		joined := strings.Join(a.tuiLines(state, width, 24), "\n")
		if got := strings.Count(joined, "user@example.com"); got != 2 {
			// The overview row and the selected detail each name the account
			// once; no session is active in this fixture.
			t.Fatalf("width=%d: email appears %d times:\n%s", width, got, joined)
		}
		if !strings.Contains(joined, "Selected account") || strings.Contains(joined, "SELECTED ACCOUNT") {
			t.Fatalf("width=%d: want one selected-account label:\n%s", width, joined)
		}
	}
}

func TestTUIFrameCorners(t *testing.T) {
	app := &Application{p: makePalette(false)}
	lines := app.tuiHeaderLines("AGY SWAP", "v2.9.1", 80)
	if !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") {
		t.Errorf("Header top border = %q, want rounded corners ╭...╮", lines[0])
	}
}

func TestFormatUsageRefreshed(t *testing.T) {
	fixed := time.Date(2026, 10, 7, 14, 38, 17, 0, time.UTC)
	got := formatUsageRefreshed(fixed)
	expected := "Usage refreshed at 071026-14:38:17"
	if got != expected {
		t.Fatalf("formatUsageRefreshed() = %q, want %q", got, expected)
	}
}

func TestTUIStatusShowsUsageRefreshed(t *testing.T) {
	a, state := overlayTestState()
	fixed := time.Date(2026, 10, 7, 14, 38, 17, 0, time.UTC)
	state.message, state.messageType = formatUsageRefreshed(fixed), "success"
	lines := a.tuiStatusLines(state, 80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Usage refreshed at 071026-14:38:17") {
		t.Fatalf("status lines do not contain expected message: %s", joined)
	}
}
