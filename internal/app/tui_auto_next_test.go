package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func makeAutoNextAccount(email string, observed time.Time, buckets []map[string]any) Account {
	anyBuckets := make([]any, len(buckets))
	for i, b := range buckets {
		bucket := map[string]any{
			"id":                 firstString(getString(b, "id"), getString(b, "window")),
			"name":               firstString(getString(b, "name"), strings.ToUpper(getString(b, "window"))),
			"window":             getString(b, "window"),
			"remaining_fraction": b["remaining_fraction"],
			"reset_at":           firstString(getString(b, "reset_at"), isoTime(observed.Add(4*time.Hour))),
		}
		anyBuckets[i] = bucket
	}
	snapshot := map[string]any{
		"observed_at": isoTime(observed),
		"tier":        map[string]any{"id": "pro-tier", "name": "Pro"},
		"groups": []any{
			map[string]any{
				"id":      "gemini",
				"name":    "Gemini Models",
				"buckets": anyBuckets,
			},
		},
	}
	return Account{
		"email":          email,
		"name":           email,
		"quota_snapshot": snapshot,
	}
}

func setupAutoNextTestApp(t *testing.T, autoNext bool) (*Application, *tuiState, string, string, time.Time) {
	t.Helper()
	paths := testPaths(t)
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	tokenAlpha := tokenBlob(t, "alpha@example.com", true, "refresh-alpha", now.Add(time.Hour))
	tokenBeta := tokenBlob(t, "beta@example.com", true, "refresh-beta", now.Add(time.Hour))

	accountAlpha := makeAutoNextAccount("alpha@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	accountAlpha["token_data"] = tokenAlpha

	accountBeta := makeAutoNextAccount("beta@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.80},
		{"window": "weekly", "remaining_fraction": 0.80},
	})
	accountBeta["token_data"] = tokenBeta

	accounts := NewAccounts()
	accounts.Set("alpha@example.com", accountAlpha)
	accounts.Set("beta@example.com", accountBeta)

	store := NewStore(paths)
	if err := store.Save(accounts); err != nil {
		t.Fatalf("failed to save accounts: %v", err)
	}

	settings := defaultSettings()
	settings.UI.AutoNext = autoNext
	if err := store.SaveSettings(settings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	backend := &fakeCredentialBackend{token: tokenAlpha}
	credentials := NewCredentials(paths)
	credentials.backend = backend

	// Prepare session files with active alpha credential
	if !credentials.Apply(context.Background(), tokenAlpha, "alpha@example.com") {
		t.Fatal("failed to initialize credential session")
	}

	app := &Application{
		paths:       paths,
		store:       store,
		credentials: credentials,
		p:           makePalette(false),
	}

	state := newTUIState(accounts, tokenAlpha)
	state.settings = settings
	state.settingsLoaded = true
	state.active = "alpha@example.com"
	state.current = tokenAlpha
	state.selectedEmail = "alpha@example.com"

	return app, state, tokenAlpha, tokenBeta, now
}

func TestAutoNextInFlightToggle(t *testing.T) {
	// Scenario A: Started ON, toggled OFF in-flight -> must NOT apply
	t.Run("ON to OFF in-flight skips apply", func(t *testing.T) {
		app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)
		state.settings.UI.AutoNext = false // Toggled OFF in-flight

		event := tuiAccountsEvent{
			accounts:     state.accounts,
			quotaErrors:  map[string]string{},
			revision:     1,
			sessionToken: tokenAlpha,
			secureToken:  tokenAlpha,
			oauthToken:   tokenAlpha,
		}

		res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
		if res != tokenAlpha {
			t.Fatalf("expected session %s, got %s", tokenAlpha, res)
		}
		if state.active != "alpha@example.com" {
			t.Fatalf("active account changed to %s", state.active)
		}
		if state.toast != "" {
			t.Fatalf("unexpected toast: %s", state.toast)
		}
	})

	// Scenario B: Started OFF, toggled ON in-flight -> ON at completion, must apply
	t.Run("OFF to ON in-flight applies switch", func(t *testing.T) {
		app, state, tokenAlpha, tokenBeta, now := setupAutoNextTestApp(t, false)
		state.settings.UI.AutoNext = true // Toggled ON in-flight

		event := tuiAccountsEvent{
			accounts:     state.accounts,
			quotaErrors:  map[string]string{},
			revision:     1,
			sessionToken: tokenAlpha,
			secureToken:  tokenAlpha,
			oauthToken:   tokenAlpha,
		}

		res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
		if res != tokenBeta {
			t.Fatalf("expected session %s, got %s", tokenBeta, res)
		}
		if state.active != "beta@example.com" {
			t.Fatalf("expected active beta@example.com, got %s", state.active)
		}
		if state.selectedEmail != "beta@example.com" {
			t.Fatalf("expected selection beta@example.com, got %s", state.selectedEmail)
		}
		if state.toast != "Auto-switched to beta@example.com" || state.toastType != "success" {
			t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
		}
	})
}

func TestAutoNextSessionChangeInFlight(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	// User switched session manually while refresh was in-flight
	manualToken := tokenBlob(t, "manual@example.com", true, "refresh-manual", now.Add(time.Hour))
	current := manualToken
	state.current = manualToken

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha, // Captured at start of refresh
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, current, 1, now)
	if res != manualToken {
		t.Fatalf("session changed unexpectedly: got %s, want %s", res, manualToken)
	}
	if state.toast != "" {
		t.Fatalf("unexpected toast: %s", state.toast)
	}
}

func TestAutoNextExternalSessionChangeUnderLock(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	// External switch occurred behind our back
	externalToken := tokenBlob(t, "external@example.com", true, "refresh-ext", now.Add(time.Hour))
	app.credentials.backend.(*fakeCredentialBackend).token = externalToken
	_ = atomicWrite(app.paths.OAuthToken, []byte(externalToken), 0o600)

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("expected return of current %s, got %s", tokenAlpha, res)
	}
	// Backend credential must remain externalToken (not overwritten)
	if app.credentials.backend.(*fakeCredentialBackend).token != externalToken {
		t.Fatalf("external credential was overwritten!")
	}
}

func TestAutoNextStaleRevision(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	// Current revision moved to 2
	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 2, now)
	if res != tokenAlpha {
		t.Fatalf("stale revision triggered switch: got %s, want %s", res, tokenAlpha)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active changed: %s", state.active)
	}
}

func TestAutoNextOneAttemptPerRefresh(t *testing.T) {
	app, state, tokenAlpha, tokenBeta, now := setupAutoNextTestApp(t, true)

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	// First call succeeds and switches
	res1 := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res1 != tokenBeta {
		t.Fatalf("first call failed to switch: got %s, want %s", res1, tokenBeta)
	}

	// Second call with same state (now beta is active with high quota) does nothing
	res2 := app.handleAutoNext(context.Background(), state, event, res1, 1, now)
	if res2 != tokenBeta {
		t.Fatalf("second call mutated state: got %s, want %s", res2, tokenBeta)
	}
}

func TestAutoNextBusyAndNonBrowseSkips(t *testing.T) {
	modes := []struct {
		name string
		set  func(s *tuiState)
	}{
		{"form mode", func(s *tuiState) { s.mode = tuiForm; s.form = &tuiFormState{} }},
		{"search mode", func(s *tuiState) { s.mode = tuiSearch }},
		{"help mode", func(s *tuiState) { s.mode = tuiHelp }},
		{"palette mode", func(s *tuiState) { s.mode = tuiPalette }},
		{"confirm delete", func(s *tuiState) { s.mode = tuiConfirmDelete }},
		{"confirm action", func(s *tuiState) { s.mode = tuiConfirmAction }},
		{"busy with job", func(s *tuiState) { s.mode = tuiBrowse; s.job = &tuiJobState{Done: false} }},
	}

	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)
			tc.set(state)

			event := tuiAccountsEvent{
				accounts:     state.accounts,
				quotaErrors:  map[string]string{},
				revision:     1,
				sessionToken: tokenAlpha,
				secureToken:  tokenAlpha,
				oauthToken:   tokenAlpha,
			}

			res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
			if res != tokenAlpha {
				t.Fatalf("%s: expected %s, got %s", tc.name, tokenAlpha, res)
			}
			if state.active != "alpha@example.com" {
				t.Fatalf("%s: active changed to %s", tc.name, state.active)
			}
		})
	}
}

func TestAutoNextApplyFailurePreservesSession(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)
	// Injected failure on Set
	app.credentials.backend.(*fakeCredentialBackend).failSet = true

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("expected preserved session %s, got %s", tokenAlpha, res)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active changed to %s", state.active)
	}
	if state.toast != "Auto-switch failed" || state.toastType != "error" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}

	// Verify history was NOT recorded
	if data, err := os.ReadFile(app.paths.History); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		t.Fatalf("history was written on failure: %s", string(data))
	}
}

func TestAutoNextDemoDisabled(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)
	app.demo = true

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("demo mode executed switch: got %s, want %s", res, tokenAlpha)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active changed in demo mode: %s", state.active)
	}
}

func TestAutoNextNoEligibleCandidate(t *testing.T) {
	app, state, tokenAlpha, tokenBeta, now := setupAutoNextTestApp(t, true)

	// Make beta also low quota (< 0.15)
	betaAcc := makeAutoNextAccount("beta@example.com", now.Add(-30*time.Second), []map[string]any{
		{"window": "5h", "remaining_fraction": 0.05},
	})
	betaAcc["token_data"] = tokenBeta
	state.accounts.Set("beta@example.com", betaAcc)

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("expected retained session %s, got %s", tokenAlpha, res)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active changed: %s", state.active)
	}
	if state.toast != "No eligible auto-next account" || state.toastType != "info" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}
}

func TestAutoNextStoreOrStorageError(t *testing.T) {
	for _, key := range []string{"store", "storage"} {
		t.Run(key+" error skips auto-apply", func(t *testing.T) {
			app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

			event := tuiAccountsEvent{
				accounts:     state.accounts,
				quotaErrors:  map[string]string{key: "I/O failure"},
				revision:     1,
				sessionToken: tokenAlpha,
				secureToken:  tokenAlpha,
				oauthToken:   tokenAlpha,
			}

			res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
			if res != tokenAlpha {
				t.Fatalf("store error allowed switch: got %s, want %s", res, tokenAlpha)
			}
			if state.active != "alpha@example.com" {
				t.Fatalf("active changed: %s", state.active)
			}
		})
	}
}

func TestAutoNextActiveAccountRefreshError(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	// Alpha failed to refresh
	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{"alpha@example.com": "HTTP 401 unauthorized"},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("active error allowed switch: got %s, want %s", res, tokenAlpha)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active changed: %s", state.active)
	}
}

func TestAutoNextStaleSnapshotRejected(t *testing.T) {
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	// Alpha observation is 5 minutes old (> 2m)
	alphaAcc := makeAutoNextAccount("alpha@example.com", now.Add(-5*time.Minute), []map[string]any{
		{"window": "5h", "remaining_fraction": 0.05},
	})
	alphaAcc["token_data"] = tokenAlpha
	state.accounts.Set("alpha@example.com", alphaAcc)

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("stale snapshot allowed switch: got %s, want %s", res, tokenAlpha)
	}
}

func TestAutoNextToggleSetting(t *testing.T) {
	app, state, _, _, _ := setupAutoNextTestApp(t, false)

	// Initial is OFF
	if state.autoNextEnabled() {
		t.Fatal("expected initially false")
	}

	// Toggle ON
	if !app.toggleAutoNext(state) {
		t.Fatal("toggleAutoNext failed")
	}
	if !state.autoNextEnabled() {
		t.Fatal("expected autoNext to be true")
	}
	if state.toast != "Auto-next enabled" || state.toastType != "success" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}

	// Verify persisted on disk
	data, err := os.ReadFile(app.paths.Settings)
	if err != nil {
		t.Fatal(err)
	}
	var saved AppSettings
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.UI.AutoNext {
		t.Fatal("persisted setting is not true")
	}

	// Toggle OFF
	if !app.toggleAutoNext(state) {
		t.Fatal("toggleAutoNext second time failed")
	}
	if state.autoNextEnabled() {
		t.Fatal("expected autoNext to be false")
	}
	if state.toast != "Auto-next disabled" || state.toastType != "info" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}

	// When busy with a job, toggle should be rejected
	state.job = &tuiJobState{Done: false}
	if app.toggleAutoNext(state) {
		t.Fatal("toggle should have failed when busy")
	}
	if state.autoNextEnabled() {
		t.Fatal("setting changed while busy")
	}
}

func TestAutoNextBadgeTopBorderRow0(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	app, state, _, _, _ := setupAutoNextTestApp(t, false)
	app.color = true
	app.p = makePalette(true)

	// Test OFF state
	linesOff := app.tuiLines(state, 78, 24)
	if len(linesOff) != 24 {
		t.Fatalf("expected 24 lines, got %d", len(linesOff))
	}
	plainOff := ansiPattern.ReplaceAllString(linesOff[0], "")
	if !strings.HasPrefix(plainOff, "╭") || !strings.HasSuffix(plainOff, "╮") {
		t.Fatalf("top border corners missing in OFF: %q", plainOff)
	}
	if !strings.Contains(plainOff, "AUTO-NEXT: OFF [N]") {
		t.Fatalf("expected AUTO-NEXT: OFF [N] in line 0, got %q", plainOff)
	}
	if !strings.Contains(linesOff[0], app.p.Gray) {
		t.Fatalf("expected gray color in OFF line 0")
	}
	if strings.Contains(linesOff[0], app.p.Green) {
		t.Fatalf("did not expect green color in OFF line 0")
	}
	if vw := visibleWidth(linesOff[0]); vw != 80 {
		t.Fatalf("expected visible width 80, got %d", vw)
	}

	// Test ON state
	state.settings.UI.AutoNext = true
	linesOn := app.tuiLines(state, 78, 24)
	if len(linesOn) != 24 {
		t.Fatalf("expected 24 lines, got %d", len(linesOn))
	}
	plainOn := ansiPattern.ReplaceAllString(linesOn[0], "")
	if !strings.HasPrefix(plainOn, "╭") || !strings.HasSuffix(plainOn, "╮") {
		t.Fatalf("top border corners missing in ON: %q", plainOn)
	}
	if !strings.Contains(plainOn, "AUTO-NEXT: ON [N]") {
		t.Fatalf("expected AUTO-NEXT: ON [N] in line 0, got %q", plainOn)
	}
	if !strings.Contains(linesOn[0], app.p.Green) {
		t.Fatalf("expected green color in ON line 0")
	}
	if vw := visibleWidth(linesOn[0]); vw != 80 {
		t.Fatalf("expected visible width 80, got %d", vw)
	}
}

func TestAutoNextBadgeAllViews(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	views := []struct {
		name string
		view tuiView
	}{
		{"dashboard", tuiViewDashboard},
		{"quota", tuiViewQuota},
		{"profiles", tuiViewProfiles},
		{"history", tuiViewHistory},
		{"settings", tuiViewSettings},
		{"doctor", tuiViewDoctor},
		{"backup", tuiViewBackup},
	}

	for _, v := range views {
		for _, autoNext := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_autonext_%v", v.name, autoNext), func(t *testing.T) {
				t.Setenv("NO_COLOR", "")
				app, state, _, _, _ := setupAutoNextTestApp(t, autoNext)
				app.color = true
				app.p = makePalette(true)
				state.view = v.view

				lines := app.tuiLines(state, 78, 24)
				if len(lines) != 24 {
					t.Fatalf("view %s: expected 24 rows, got %d", v.name, len(lines))
				}
				row0 := lines[0]
				plain := ansiPattern.ReplaceAllString(row0, "")
				if !strings.HasPrefix(plain, "╭") || !strings.HasSuffix(plain, "╮") {
					t.Fatalf("view %s: top corners corrupted: %q", v.name, plain)
				}
				expectedBadge := "AUTO-NEXT: OFF [N]"
				if autoNext {
					expectedBadge = "AUTO-NEXT: ON [N]"
				}
				if !strings.Contains(plain, expectedBadge) {
					t.Fatalf("view %s (autoNext=%v): badge %q missing in row 0: %q", v.name, autoNext, expectedBadge, plain)
				}
				if autoNext && !strings.Contains(row0, app.p.Green) {
					t.Fatalf("view %s: expected green badge when ON", v.name)
				}
				if !autoNext && strings.Contains(row0, app.p.Green) {
					t.Fatalf("view %s: did not expect green badge when OFF", v.name)
				}
				if vw := visibleWidth(row0); vw != 80 {
					t.Fatalf("view %s: expected visible width 80, got %d", v.name, vw)
				}
			})
		}
	}
}

func TestAutoNextBadgeAllOverlays(t *testing.T) {
	overlays := []struct {
		name  string
		apply func(s *tuiState)
	}{
		{"help", func(s *tuiState) { s.mode = tuiHelp }},
		{"confirm_delete", func(s *tuiState) { s.mode = tuiConfirmDelete; s.confirmEmail = "beta@example.com" }},
		{"confirm_action", func(s *tuiState) { s.mode = tuiConfirmAction; s.confirmTitle = "Confirm Action" }},
		{"palette", func(s *tuiState) { s.mode = tuiPalette; s.beginPalette() }},
		{"form", func(s *tuiState) {
			s.mode = tuiForm
			s.form = &tuiFormState{
				Title:  "SETTINGS",
				Fields: []tuiFormField{{Key: "test", Label: "Field", Value: "val"}},
			}
		}},
		{"toast", func(s *tuiState) {
			s.showToast("Operation successful", "success")
			s.toastUntil = time.Now().Add(time.Minute)
		}},
	}

	for _, ov := range overlays {
		for _, autoNext := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_autonext_%v", ov.name, autoNext), func(t *testing.T) {
				t.Setenv("NO_COLOR", "")
				app, state, _, _, _ := setupAutoNextTestApp(t, autoNext)
				app.color = true
				app.p = makePalette(true)
				ov.apply(state)

				lines := app.tuiLines(state, 78, 24)
				if len(lines) != 24 {
					t.Fatalf("overlay %s: expected 24 rows, got %d", ov.name, len(lines))
				}
				row0 := lines[0]
				plain := ansiPattern.ReplaceAllString(row0, "")
				if !strings.HasPrefix(plain, "╭") || !strings.HasSuffix(plain, "╮") {
					t.Fatalf("overlay %s: top corners corrupted: %q", ov.name, plain)
				}
				expectedBadge := "AUTO-NEXT: OFF [N]"
				if autoNext {
					expectedBadge = "AUTO-NEXT: ON [N]"
				}
				if !strings.Contains(plain, expectedBadge) {
					t.Fatalf("overlay %s: badge %q missing in row 0: %q", ov.name, expectedBadge, plain)
				}
				if vw := visibleWidth(row0); vw != 80 {
					t.Fatalf("overlay %s: expected visible width 80, got %d", ov.name, vw)
				}
			})
		}
	}
}

func TestAutoNextBadgeCompactDimensions(t *testing.T) {
	sizes := []struct {
		name        string
		width       int // inner width
		height      int
		targetWidth int // frame width = inner width + 2
	}{
		{"28x12_min", 26, 12, 28},
		{"32x14", 30, 14, 32},
		{"48x16", 46, 16, 48},
		{"64x18", 62, 18, 64},
		{"80x24", 78, 24, 80},
		{"120x30", 118, 30, 120},
	}

	for _, sz := range sizes {
		for _, autoNext := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_autonext_%v", sz.name, autoNext), func(t *testing.T) {
				t.Setenv("NO_COLOR", "")
				app, state, _, _, _ := setupAutoNextTestApp(t, autoNext)
				app.color = true
				app.p = makePalette(true)

				lines := app.tuiLines(state, sz.width, sz.height)
				if len(lines) != sz.height {
					t.Fatalf("size %s: expected %d rows, got %d", sz.name, sz.height, len(lines))
				}
				row0 := lines[0]
				plain := ansiPattern.ReplaceAllString(row0, "")
				if !strings.HasPrefix(plain, "╭") || !strings.HasSuffix(plain, "╮") {
					t.Fatalf("size %s: corners corrupted: %q", sz.name, plain)
				}
				expectedBadge := "AUTO-NEXT: OFF [N]"
				if autoNext {
					expectedBadge = "AUTO-NEXT: ON [N]"
				}
				if !strings.Contains(plain, expectedBadge) {
					t.Fatalf("size %s: badge %q missing in row 0: %q", sz.name, expectedBadge, plain)
				}
				if vw := visibleWidth(row0); vw != sz.targetWidth {
					t.Fatalf("size %s: expected visible width %d, got %d", sz.name, sz.targetWidth, vw)
				}
				// Verify all lines meet visible width contract
				for i, l := range lines {
					if vw := visibleWidth(l); vw != sz.targetWidth {
						t.Fatalf("size %s line %d: expected width %d, got %d", sz.name, i, sz.targetWidth, vw)
					}
				}
			})
		}
	}
}

func TestAutoNextHelpAndPalette(t *testing.T) {
	app, state, _, _, _ := setupAutoNextTestApp(t, false)

	// 1. Verify help lines contain Shift+N and strict OR thresholds
	helpLines := app.tuiHelpLines(78)
	foundHelp := false
	for _, line := range helpLines {
		plain := ansiPattern.ReplaceAllString(line, "")
		if strings.Contains(plain, "N (Shift+N)") && strings.Contains(plain, "Toggle auto-next") &&
			strings.Contains(plain, "5h < 15%") && strings.Contains(plain, "weekly < 8%") {
			foundHelp = true
			break
		}
	}
	if !foundHelp {
		t.Fatalf("tuiHelpLines missing N (Shift+N) auto-next guide with thresholds:\n%s", strings.Join(helpLines, "\n"))
	}

	// 2. Verify tuiActions includes toggle-auto-next with shortcut N
	actions := tuiActions(state)
	var autoNextAction *tuiAction
	for i := range actions {
		if actions[i].ID == "toggle-auto-next" {
			autoNextAction = &actions[i]
			break
		}
	}
	if autoNextAction == nil {
		t.Fatal("tuiActions missing toggle-auto-next action")
	}
	if autoNextAction.Shortcut != "N" {
		t.Fatalf("expected shortcut 'N', got %q", autoNextAction.Shortcut)
	}

	// 3. Verify paletteActions includes toggle-auto-next
	state.beginPalette()
	paletteItems := state.paletteActions()
	foundPalette := false
	for _, item := range paletteItems {
		if item.ID == "toggle-auto-next" {
			foundPalette = true
			break
		}
	}
	if !foundPalette {
		t.Fatal("paletteActions does not include toggle-auto-next")
	}

	// 4. Verify settings view display contains auto next
	state.settings.UI.AutoNext = false
	settingsRowsOff := app.tuiSettingsViewRows(state, 78, 20)
	joinedOff := strings.Join(settingsRowsOff, "\n")
	if !strings.Contains(joinedOff, "auto next: off") {
		t.Fatalf("settings view missing 'auto next: off':\n%s", joinedOff)
	}

	state.settings.UI.AutoNext = true
	settingsRowsOn := app.tuiSettingsViewRows(state, 78, 20)
	joinedOn := strings.Join(settingsRowsOn, "\n")
	if !strings.Contains(joinedOn, "auto next: on") {
		t.Fatalf("settings view missing 'auto next: on':\n%s", joinedOn)
	}
}

func TestAutoNextSettingsFormToggle(t *testing.T) {
	app, state, _, _, _ := setupAutoNextTestApp(t, false)

	// Open settings form
	app.beginTUIForm(state, "settings")
	if state.form == nil || state.form.Kind != "settings" {
		t.Fatal("expected settings form to be open")
	}

	// Find ui.auto_next field
	var autoNextField *tuiFormField
	for i := range state.form.Fields {
		if state.form.Fields[i].Key == "ui.auto_next" {
			autoNextField = &state.form.Fields[i]
			break
		}
	}
	if autoNextField == nil {
		t.Fatal("ui.auto_next field not found in settings form")
	}
	if autoNextField.Value != "off" {
		t.Fatalf("expected initial value 'off', got %q", autoNextField.Value)
	}

	// Toggle to "on"
	autoNextField.Value = "on"
	msg, err := app.applyTUIForm(context.Background(), state)
	if err != nil {
		t.Fatalf("applyTUIForm failed: %v", err)
	}
	if msg != "Settings saved" {
		t.Fatalf("unexpected message: %q", msg)
	}

	// Verify in-memory state updated
	if !state.settings.UI.AutoNext {
		t.Fatal("state.settings.UI.AutoNext was not updated to true")
	}

	// Verify disk persistence
	loaded, err := app.store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UI.AutoNext {
		t.Fatal("persisted UI.AutoNext on disk is not true")
	}

	// Toggle back to "off"
	app.beginTUIForm(state, "settings")
	for i := range state.form.Fields {
		if state.form.Fields[i].Key == "ui.auto_next" {
			state.form.Fields[i].Value = "off"
			break
		}
	}
	if _, err := app.applyTUIForm(context.Background(), state); err != nil {
		t.Fatalf("applyTUIForm off failed: %v", err)
	}
	if state.settings.UI.AutoNext {
		t.Fatal("state.settings.UI.AutoNext was not updated to false")
	}
	loaded2, _ := app.store.LoadSettings()
	if loaded2.UI.AutoNext {
		t.Fatal("persisted UI.AutoNext on disk is not false")
	}
}

func TestAutoNextActiveEmailCaseInsensitive(t *testing.T) {
	app, state, tokenAlpha, tokenBeta, now := setupAutoNextTestApp(t, true)

	// Set active account with uppercase casing
	state.active = "ALPHA@EXAMPLE.COM"

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  tokenAlpha,
		oauthToken:   tokenAlpha,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenBeta {
		t.Fatalf("expected switch to %s, got %s", tokenBeta, res)
	}
	if !strings.EqualFold(state.active, "beta@example.com") {
		t.Fatalf("expected beta active, got %s", state.active)
	}
}

func TestAutoNextCandidateMultiGroupExclusion(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	// Candidate beta has 2 groups: gemini is healthy, but third_party has weekly < 0.08
	accA := helperMakeAccount("a@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	accB := helperMakeMultiGroupAccount("b@example.com", obs, []map[string]any{
		{
			"id": "gemini",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.80},
				map[string]any{"window": "weekly", "remaining_fraction": 0.80},
			},
		},
		{
			"id": "third_party",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.80},
				map[string]any{"window": "weekly", "remaining_fraction": 0.05},
			},
		},
	})

	accounts := NewAccounts()
	accounts.Set("a@example.com", accA)
	accounts.Set("b@example.com", accB)

	settings := defaultSettings()
	cand, ok := SelectAutoNextCandidate(accounts, "a@example.com", settings, nil, now)
	if ok || cand != nil {
		t.Fatalf("expected candidate B to be excluded due to low weekly in second group, got %v", cand)
	}
}

func TestAutoNextMutatedSecureBackendUnchangedOAuthMirrorSkips(t *testing.T) {
	// Case 1: Unchanged valid JSON OAuth mirror + mutated secure backend -> handleAutoNext must skip auto-switch and preserve session.
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	startOAuth := app.credentials.OAuthToken()
	startSecure := app.credentials.Secure(context.Background())
	if startOAuth == "" || startSecure == "" {
		t.Fatalf("setup failed: startOAuth=%q startSecure=%q", startOAuth, startSecure)
	}

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  startSecure,
		oauthToken:   startOAuth,
	}

	// Mutate secure backend only (e.g. external process updated OS keyring)
	mutatedSecure := tokenBlob(t, "mutated@example.com", true, "refresh-mutated", now.Add(time.Hour))
	app.credentials.backend.(*fakeCredentialBackend).token = mutatedSecure

	// Verify that credentials.Current() still returns tokenAlpha because valid JSON mirror takes precedence
	if current := app.credentials.Current(context.Background()); current != tokenAlpha {
		t.Fatalf("expected Current() to prioritize valid OAuth mirror %q, got %q", tokenAlpha, current)
	}

	// handleAutoNext must detect that secure backend changed and skip auto-switch
	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("expected preserved session %s, got %s", tokenAlpha, res)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active account changed to %s", state.active)
	}
	// Verify backend credential was NOT overwritten by auto-switch
	if app.credentials.backend.(*fakeCredentialBackend).token != mutatedSecure {
		t.Fatalf("mutated secure backend credential was overwritten by auto-switch!")
	}
	if state.toast != "" {
		t.Fatalf("unexpected toast on skip: %q", state.toast)
	}
}

func TestAutoNextMutatedOAuthMirrorUnchangedSecureBackendSkips(t *testing.T) {
	// Case 2: OAuth-only changes (mutated OAuth mirror while secure backend unchanged) -> handleAutoNext must skip auto-switch.
	app, state, tokenAlpha, _, now := setupAutoNextTestApp(t, true)

	startOAuth := app.credentials.OAuthToken()
	startSecure := app.credentials.Secure(context.Background())
	if startOAuth == "" || startSecure == "" {
		t.Fatalf("setup failed: startOAuth=%q startSecure=%q", startOAuth, startSecure)
	}

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  startSecure,
		oauthToken:   startOAuth,
	}

	// Mutate OAuth mirror file only (secure backend remains unchanged as tokenAlpha)
	otherToken := tokenBlob(t, "other@example.com", true, "refresh-other", now.Add(time.Hour))
	decoded := decodeToken(otherToken)
	if err := atomicWriteJSON(app.paths.OAuthToken, decoded); err != nil {
		t.Fatalf("failed to mutate OAuth mirror: %v", err)
	}

	// Verify OAuth mirror has changed while backend has not
	nowOAuth := app.credentials.OAuthToken()
	if nowOAuth == startOAuth {
		t.Fatalf("OAuth mirror did not change")
	}
	if app.credentials.Secure(context.Background()) != startSecure {
		t.Fatalf("secure backend was modified unexpectedly")
	}

	// handleAutoNext must detect that OAuth mirror changed and skip auto-switch
	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenAlpha {
		t.Fatalf("expected preserved session %s, got %s", tokenAlpha, res)
	}
	if state.active != "alpha@example.com" {
		t.Fatalf("active account changed to %s", state.active)
	}
	// Verify backend was not changed by auto-switch
	if app.credentials.backend.(*fakeCredentialBackend).token != tokenAlpha {
		t.Fatalf("secure backend was altered!")
	}
	if state.toast != "" {
		t.Fatalf("unexpected toast on skip: %q", state.toast)
	}
}

func TestAutoNextConsistentSessionAppliesSwitch(t *testing.T) {
	// Case 3: No-op consistent session (neither changed) -> auto-switch proceeds successfully.
	app, state, tokenAlpha, tokenBeta, now := setupAutoNextTestApp(t, true)

	startOAuth := app.credentials.OAuthToken()
	startSecure := app.credentials.Secure(context.Background())
	if startOAuth == "" || startSecure == "" {
		t.Fatalf("setup failed: startOAuth=%q startSecure=%q", startOAuth, startSecure)
	}

	event := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: tokenAlpha,
		secureToken:  startSecure,
		oauthToken:   startOAuth,
	}

	res := app.handleAutoNext(context.Background(), state, event, tokenAlpha, 1, now)
	if res != tokenBeta {
		t.Fatalf("expected switch to %s, got %s", tokenBeta, res)
	}
	if state.active != "beta@example.com" {
		t.Fatalf("expected active beta@example.com, got %s", state.active)
	}
	if state.selectedEmail != "beta@example.com" {
		t.Fatalf("expected selected beta@example.com, got %s", state.selectedEmail)
	}
	if state.toast != "Auto-switched to beta@example.com" || state.toastType != "success" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}
	// Backend credential should now be tokenBeta
	if app.credentials.Secure(context.Background()) != tokenBeta {
		t.Fatalf("expected backend token %s, got %s", tokenBeta, app.credentials.Secure(context.Background()))
	}
}

type mockAutoNextPreparingBackend struct {
	token   string
	prepare func(string) (string, error)
}

func (m *mockAutoNextPreparingBackend) Get(context.Context) string { return m.token }
func (m *mockAutoNextPreparingBackend) Set(_ context.Context, value string) bool {
	m.token = value
	return true
}
func (m *mockAutoNextPreparingBackend) Delete(context.Context) bool {
	m.token = ""
	return true
}
func (m *mockAutoNextPreparingBackend) PrepareSession(value string) (string, error) {
	if m.prepare != nil {
		return m.prepare(value)
	}
	return value, nil
}

func newMockBackend(token string) *fakeCredentialBackend {
	return &fakeCredentialBackend{token: token}
}

func rawCompactTestToken(t *testing.T, email, refresh string, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := map[string]any{"iss": "https://accounts.google.com", "email_verified": true, "email": email}
	claimData, _ := json.Marshal(claims)
	claim := base64.RawURLEncoding.EncodeToString(claimData)
	inner := map[string]any{
		"access_token":  "access-" + email,
		"refresh_token": refresh,
		"id_token":      header + "." + claim + ".signature",
	}
	if !expiry.IsZero() {
		inner["expiry"] = isoTime(expiry)
	}
	payload, err := json.Marshal(map[string]any{"token": inner})
	if err != nil {
		t.Fatalf("failed to marshal compact token: %v", err)
	}
	return string(payload)
}

func setupThreeAccountAutoNextTestApp(
	t *testing.T,
	backend CredentialBackend,
	tokenAlpha, tokenBeta, tokenGamma string,
) (*Application, *tuiState, time.Time) {
	t.Helper()
	paths := testPaths(t)
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	accountAlpha := makeAutoNextAccount("alpha@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	accountAlpha["token_data"] = tokenAlpha

	accountBeta := makeAutoNextAccount("beta@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.80},
		{"window": "weekly", "remaining_fraction": 0.80},
	})
	accountBeta["token_data"] = tokenBeta

	accountGamma := makeAutoNextAccount("gamma@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.90},
		{"window": "weekly", "remaining_fraction": 0.90},
	})
	accountGamma["token_data"] = tokenGamma

	accounts := NewAccounts()
	accounts.Set("alpha@example.com", accountAlpha)
	accounts.Set("beta@example.com", accountBeta)
	accounts.Set("gamma@example.com", accountGamma)

	store := NewStore(paths)
	if err := store.Save(accounts); err != nil {
		t.Fatalf("failed to save accounts: %v", err)
	}

	settings := defaultSettings()
	settings.UI.AutoNext = true
	if err := store.SaveSettings(settings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	credentials := NewCredentials(paths)
	credentials.backend = backend

	if !credentials.Apply(context.Background(), tokenAlpha, "alpha@example.com") {
		t.Fatal("failed to initialize credential session")
	}

	app := &Application{
		paths:       paths,
		store:       store,
		credentials: credentials,
		p:           makePalette(false),
	}

	initialSession := credentials.Current(context.Background())
	state := newTUIState(accounts, initialSession)
	state.settings = settings
	state.settingsLoaded = true
	state.active = "alpha@example.com"
	state.current = initialSession
	state.selectedEmail = "alpha@example.com"

	return app, state, now
}

func TestAutoNextNormalizingBackendSessionFollowUpRefresh(t *testing.T) {
	// Tests backend preparer/normalizer where the original account token blob
	// differs from the persisted/read credentials.Current(ctx).
	// Follow-up second independent refresh must NOT stall and must successfully
	// rotate to the next eligible candidate (Gamma).
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tokenAlpha := tokenBlob(t, "alpha@example.com", true, "refresh-alpha", now.Add(time.Hour))
	tokenBeta := tokenBlob(t, "beta@example.com", true, "refresh-beta", now.Add(time.Hour))
	tokenGamma := tokenBlob(t, "gamma@example.com", true, "refresh-gamma", now.Add(time.Hour))

	backend := &mockAutoNextPreparingBackend{
		token: tokenAlpha,
		prepare: func(value string) (string, error) {
			decoded := decodeToken(value)
			if decoded == nil {
				return value, nil
			}
			raw, _ := json.Marshal(decoded)
			return "prepared-session:" + string(raw), nil
		},
	}

	app, state, testTime := setupThreeAccountAutoNextTestApp(t, backend, tokenAlpha, tokenBeta, tokenGamma)

	// Step 1: Initial state has alpha active (low quota 5h < 15%)
	current := app.credentials.Current(context.Background())
	startSecure := app.credentials.Secure(context.Background())
	startOAuth := app.credentials.OAuthToken()

	event1 := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: current,
		secureToken:  startSecure,
		oauthToken:   startOAuth,
	}

	res1 := app.handleAutoNext(context.Background(), state, event1, current, 1, testTime)
	authoritativeCurrent1 := app.credentials.Current(context.Background())

	// Crucial check: res1 must match authoritative credentials.Current(ctx),
	// NOT the raw candidate account token blob if it differs.
	if res1 != authoritativeCurrent1 {
		t.Fatalf("expected res1 to match credentials.Current %q, got %q", authoritativeCurrent1, res1)
	}
	if state.current != authoritativeCurrent1 {
		t.Fatalf("state.current = %q, want %q", state.current, authoritativeCurrent1)
	}
	if state.active != "beta@example.com" {
		t.Fatalf("expected active beta@example.com, got %s", state.active)
	}
	if state.toast != "Auto-switched to beta@example.com" || state.toastType != "success" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}

	// Step 2: Follow-up second independent refresh.
	// Now advance time, and Beta's quota has dropped below threshold (5h = 0.10).
	// Gamma is healthy (5h = 0.90, weekly = 0.90).
	time2 := testTime.Add(30 * time.Second)
	obs2 := time2.Add(-5 * time.Second)

	betaAccountLow := makeAutoNextAccount("beta@example.com", obs2, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	betaAccountLow["token_data"] = tokenBeta

	gammaAccountHigh := makeAutoNextAccount("gamma@example.com", obs2, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.90},
		{"window": "weekly", "remaining_fraction": 0.90},
	})
	gammaAccountHigh["token_data"] = tokenGamma

	freshAccounts := NewAccounts()
	freshAccounts.Set("alpha@example.com", state.accounts.ByEmail["alpha@example.com"])
	freshAccounts.Set("beta@example.com", betaAccountLow)
	freshAccounts.Set("gamma@example.com", gammaAccountHigh)

	state.setAccounts(freshAccounts)

	current2 := res1
	secureToken2 := app.credentials.Secure(context.Background())
	oauthToken2 := app.credentials.OAuthToken()

	event2 := tuiAccountsEvent{
		accounts:     freshAccounts,
		quotaErrors:  map[string]string{},
		revision:     2,
		sessionToken: current2,
		secureToken:  secureToken2,
		oauthToken:   oauthToken2,
	}

	// This second auto-next must NOT permanently stall.
	// Session identity guards must pass because current was properly normalized!
	res2 := app.handleAutoNext(context.Background(), state, event2, current2, 2, time2)
	authoritativeCurrent2 := app.credentials.Current(context.Background())

	if res2 != authoritativeCurrent2 {
		t.Fatalf("expected second auto-next to switch to gamma (%q), got %q", authoritativeCurrent2, res2)
	}
	if state.active != "gamma@example.com" {
		t.Fatalf("expected active gamma@example.com, got %s (second refresh stalled!)", state.active)
	}
	if state.toast != "Auto-switched to gamma@example.com" || state.toastType != "success" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}
}

func TestAutoNextCompactAccountTokenVersusPersistedOAuthFile(t *testing.T) {
	// Candidate accounts store compact raw JSON tokens (e.g. {"token":{...}} without go-keyring-base64: prefix).
	// Upon apply, OAuth files persist and credentials.Current(ctx) yields "go-keyring-base64:<base64-encoded-oauth-file>".
	// The original account token blob strictly differs from credentials.Current(ctx).
	// Verifies that handleAutoNext assigns the authoritative persisted representation and
	// that a follow-up refresh successfully rotates to the next candidate without stalling.
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tokenAlpha := tokenBlob(t, "alpha@example.com", true, "refresh-alpha", now.Add(time.Hour))
	rawCompactBeta := rawCompactTestToken(t, "beta@example.com", "refresh-beta", now.Add(time.Hour))
	rawCompactGamma := rawCompactTestToken(t, "gamma@example.com", "refresh-gamma", now.Add(time.Hour))

	backend := newMockBackend(tokenAlpha)
	app, state, testTime := setupThreeAccountAutoNextTestApp(t, backend, tokenAlpha, rawCompactBeta, rawCompactGamma)

	current := app.credentials.Current(context.Background())
	event1 := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     1,
		sessionToken: current,
		secureToken:  app.credentials.Secure(context.Background()),
		oauthToken:   app.credentials.OAuthToken(),
	}

	res1 := app.handleAutoNext(context.Background(), state, event1, current, 1, testTime)
	authoritativeCurrent1 := app.credentials.Current(context.Background())

	// Verify that the original raw compact token blob differs from authoritative Current
	if rawCompactBeta == authoritativeCurrent1 {
		t.Fatalf("rawCompactBeta should differ from authoritativeCurrent1")
	}
	// Post-apply session must be the authoritative persisted Current
	if res1 != authoritativeCurrent1 {
		t.Fatalf("expected res1 %q, got %q", authoritativeCurrent1, res1)
	}
	if state.current != authoritativeCurrent1 {
		t.Fatalf("expected state.current %q, got %q", authoritativeCurrent1, state.current)
	}
	if state.active != "beta@example.com" {
		t.Fatalf("expected active beta@example.com, got %s", state.active)
	}

	// Verify the regression condition: if un-normalized token were passed as sessionToken,
	// handleAutoNext identity guard would reject it and preserve input:
	eventStalled := tuiAccountsEvent{
		accounts:     state.accounts,
		quotaErrors:  map[string]string{},
		revision:     2,
		sessionToken: rawCompactBeta, // un-normalized token
		secureToken:  app.credentials.Secure(context.Background()),
		oauthToken:   app.credentials.OAuthToken(),
	}
	stalledRes := app.handleAutoNext(context.Background(), state, eventStalled, rawCompactBeta, 2, testTime)
	if stalledRes != rawCompactBeta {
		t.Fatalf("expected mismatch guard to reject un-normalized token and preserve input")
	}

	// Follow-up second refresh with authoritative current1:
	time2 := testTime.Add(30 * time.Second)
	obs2 := time2.Add(-5 * time.Second)

	betaAccountLow := makeAutoNextAccount("beta@example.com", obs2, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	betaAccountLow["token_data"] = rawCompactBeta

	gammaAccountHigh := makeAutoNextAccount("gamma@example.com", obs2, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.90},
		{"window": "weekly", "remaining_fraction": 0.90},
	})
	gammaAccountHigh["token_data"] = rawCompactGamma

	freshAccounts := NewAccounts()
	freshAccounts.Set("alpha@example.com", state.accounts.ByEmail["alpha@example.com"])
	freshAccounts.Set("beta@example.com", betaAccountLow)
	freshAccounts.Set("gamma@example.com", gammaAccountHigh)

	state.setAccounts(freshAccounts)

	current2 := res1
	event2 := tuiAccountsEvent{
		accounts:     freshAccounts,
		quotaErrors:  map[string]string{},
		revision:     3,
		sessionToken: current2,
		secureToken:  app.credentials.Secure(context.Background()),
		oauthToken:   app.credentials.OAuthToken(),
	}

	res2 := app.handleAutoNext(context.Background(), state, event2, current2, 3, time2)
	authoritativeCurrent2 := app.credentials.Current(context.Background())

	if res2 != authoritativeCurrent2 {
		t.Fatalf("expected second auto-next to switch to gamma (%q), got %q", authoritativeCurrent2, res2)
	}
	if state.active != "gamma@example.com" {
		t.Fatalf("expected active gamma@example.com, got %s", state.active)
	}
	if state.toast != "Auto-switched to gamma@example.com" || state.toastType != "success" {
		t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
	}
}

func TestAutoNextCompactWrapperVersusPersistedPrettyJSONCrossPlatform(t *testing.T) {
	// Covers compact wrapper versus persisted pretty JSON cross-platform (LF and CRLF).
	// When OAuth mirror contains formatted/indented JSON, credentials.Current(ctx) reflects
	// the persisted file representation. Auto-next must adopt this authoritative representation
	// so subsequent refreshes do not stall.
	for _, tc := range []struct {
		name     string
		lineFeed string
	}{
		{name: "LF formatted OAuth mirror", lineFeed: "\n"},
		{name: "CRLF formatted OAuth mirror", lineFeed: "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			tokenAlpha := tokenBlob(t, "alpha@example.com", true, "refresh-alpha", now.Add(time.Hour))
			compactBeta := rawCompactTestToken(t, "beta@example.com", "refresh-beta", now.Add(time.Hour))
			compactGamma := rawCompactTestToken(t, "gamma@example.com", "refresh-gamma", now.Add(time.Hour))

			backend := newMockBackend(tokenAlpha)
			app, state, testTime := setupThreeAccountAutoNextTestApp(t, backend, tokenAlpha, compactBeta, compactGamma)

			current := app.credentials.Current(context.Background())
			event1 := tuiAccountsEvent{
				accounts:     state.accounts,
				quotaErrors:  map[string]string{},
				revision:     1,
				sessionToken: current,
				secureToken:  app.credentials.Secure(context.Background()),
				oauthToken:   app.credentials.OAuthToken(),
			}

			// First auto-switch to Beta
			res1 := app.handleAutoNext(context.Background(), state, event1, current, 1, testTime)
			if res1 != app.credentials.Current(context.Background()) {
				t.Fatalf("expected res1 %q, got %q", app.credentials.Current(context.Background()), res1)
			}
			if state.active != "beta@example.com" {
				t.Fatalf("expected active beta@example.com, got %s", state.active)
			}

			// Simulate external formatting of OAuth mirror (e.g. tool or editor formatted with indentation and specific line breaks)
			decodedBeta := decodeToken(compactBeta)
			prettyBytes, err := json.MarshalIndent(decodedBeta, "", "  ")
			if err != nil {
				t.Fatalf("failed to marshal pretty json: %v", err)
			}
			if tc.lineFeed == "\r\n" {
				prettyBytes = []byte(strings.ReplaceAll(string(prettyBytes), "\n", "\r\n"))
			}
			if err := atomicWrite(app.paths.OAuthToken, prettyBytes, 0o600); err != nil {
				t.Fatalf("failed to write formatted OAuth mirror: %v", err)
			}

			// Now authoritative Current reflects the pretty formatted file
			authoritativeCurrent := app.credentials.Current(context.Background())
			if authoritativeCurrent == compactBeta {
				t.Fatalf("authoritativeCurrent should differ from compactBeta")
			}

			// With state.current updated to the authoritative Current (simulating tui loop refresh / performSwitch sync):
			state.current = authoritativeCurrent

			// Second refresh where Beta is low quota and Gamma is eligible:
			time2 := testTime.Add(30 * time.Second)
			obs2 := time2.Add(-5 * time.Second)

			betaAccountLow := makeAutoNextAccount("beta@example.com", obs2, []map[string]any{
				{"window": "5h", "remaining_fraction": 0.10},
				{"window": "weekly", "remaining_fraction": 0.50},
			})
			betaAccountLow["token_data"] = compactBeta

			gammaAccountHigh := makeAutoNextAccount("gamma@example.com", obs2, []map[string]any{
				{"window": "5h", "remaining_fraction": 0.90},
				{"window": "weekly", "remaining_fraction": 0.90},
			})
			gammaAccountHigh["token_data"] = compactGamma

			freshAccounts := NewAccounts()
			freshAccounts.Set("alpha@example.com", state.accounts.ByEmail["alpha@example.com"])
			freshAccounts.Set("beta@example.com", betaAccountLow)
			freshAccounts.Set("gamma@example.com", gammaAccountHigh)

			state.setAccounts(freshAccounts)

			event2 := tuiAccountsEvent{
				accounts:     freshAccounts,
				quotaErrors:  map[string]string{},
				revision:     2,
				sessionToken: authoritativeCurrent,
				secureToken:  app.credentials.Secure(context.Background()),
				oauthToken:   app.credentials.OAuthToken(),
			}

			res2 := app.handleAutoNext(context.Background(), state, event2, authoritativeCurrent, 2, time2)
			authoritativeCurrent2 := app.credentials.Current(context.Background())

			if res2 != authoritativeCurrent2 {
				t.Fatalf("expected switch to gamma (%q), got %q", authoritativeCurrent2, res2)
			}
			if state.active != "gamma@example.com" {
				t.Fatalf("expected active gamma@example.com, got %s", state.active)
			}
			if state.toast != "Auto-switched to gamma@example.com" || state.toastType != "success" {
				t.Fatalf("toast = %q (%s)", state.toast, state.toastType)
			}
		})
	}
}
