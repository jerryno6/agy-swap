package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (a *Application) applyTUIForm(ctx context.Context, state *tuiState) (string, error) {
	if state == nil || state.form == nil {
		return "", errors.New("no form is active")
	}
	form := state.form
	switch form.Kind {
	case "profile-create", "profile-edit":
		name := cleanText(formField(form, "name"))
		if err := validateAliasName(name); err != nil {
			return "", err
		}
		accounts, err := a.store.Load(false)
		if err != nil {
			return "", err
		}
		settings, err := a.loadSettings()
		if err != nil {
			return "", err
		}
		email, err := resolveConfiguredTarget(formField(form, "account"), accounts, settings)
		if err != nil || email == "" {
			if err == nil {
				err = errors.New("account not found")
			}
			return "", err
		}
		profile := settings.Profiles[name]
		profile.Account = email
		profile.Family = cleanText(formField(form, "family"))
		profile.Policy = firstString(profile.Policy, settings.Policy.Name)
		if threshold := strings.TrimSpace(formField(form, "threshold")); threshold != "" {
			value, parseErr := strconv.Atoi(threshold)
			if parseErr != nil || value < 0 || value > 100 {
				return "", errors.New("notify threshold must be between 0 and 100")
			}
			profile.NotifyThreshold = value
		}
		settings.Profiles[name] = profile
		if err := a.store.SaveSettings(settings); err != nil {
			return "", err
		}
		return "Saved profile " + name, nil

	case "tags":
		email, _, ok := state.selectedAccount()
		if !ok {
			return "", errors.New("select an account first")
		}
		settings, err := a.loadSettings()
		if err != nil {
			return "", err
		}
		parts := strings.Split(formField(form, "tags"), ",")
		settings.Tags[email] = uniqueClean(parts)
		if len(settings.Tags[email]) == 0 {
			delete(settings.Tags, email)
		}
		if err := a.store.SaveSettings(settings); err != nil {
			return "", err
		}
		return "Updated tags for " + email, nil

	case "settings":
		settings, err := a.loadSettings()
		if err != nil {
			return "", err
		}
		for _, field := range form.Fields {
			value := field.Value
			if field.Value == "on" || field.Value == "off" {
				value = strconv.FormatBool(formBool(form, field.Key))
			}
			if err := setConfigValue(&settings, field.Key, value); err != nil {
				return "", fmt.Errorf("%s: %w", field.Label, err)
			}
		}
		if err := a.store.SaveSettings(settings); err != nil {
			return "", err
		}
		state.settings = settings
		state.settingsLoaded = true
		return "Settings saved", nil

	case "alias":
		name, target := cleanText(formField(form, "name")), cleanText(formField(form, "target"))
		if err := validateAliasName(name); err != nil {
			return "", err
		}
		if target == "" {
			return "", errors.New("target is required")
		}
		settings, err := a.loadSettings()
		if err != nil {
			return "", err
		}
		settings.Aliases[name] = target
		if err := a.store.SaveSettings(settings); err != nil {
			return "", err
		}
		return "Saved alias " + name, nil

	case "binding":
		path := cleanBindingPath(formField(form, "path"))
		profile := cleanText(formField(form, "profile"))
		mode := firstString(cleanText(formField(form, "mode")), "prompt")
		if path == "" || profile == "" {
			return "", errors.New("path and profile are required")
		}
		if !oneOf(mode, "prompt", "recommend", "auto", "disabled") {
			return "", errors.New("mode must be prompt, recommend, auto, or disabled")
		}
		settings, err := a.loadSettings()
		if err != nil {
			return "", err
		}
		if _, ok := settings.Profiles[profile]; !ok {
			return "", fmt.Errorf("unknown profile %q", profile)
		}
		replaced := false
		for index := range settings.Bindings {
			if settings.Bindings[index].Path == path {
				settings.Bindings[index] = Binding{Path: path, Profile: profile, Mode: mode}
				replaced = true
			}
		}
		if !replaced {
			settings.Bindings = append(settings.Bindings, Binding{Path: path, Profile: profile, Mode: mode})
		}
		if err := a.store.SaveSettings(settings); err != nil {
			return "", err
		}
		return "Saved project binding", nil

	case "target":
		name, command := cleanText(formField(form, "name")), strings.TrimSpace(formField(form, "command"))
		if err := validateAliasName(name); err != nil {
			return "", err
		}
		if command == "" || strings.ContainsAny(command, "\t\r\n") {
			return "", errors.New("target command must be one executable path or name")
		}
		settings, err := a.loadSettings()
		if err != nil {
			return "", err
		}
		settings.Targets[name] = TargetConfig{Command: command, Enabled: true}
		if err := a.store.SaveSettings(settings); err != nil {
			return "", err
		}
		return "Saved target " + name, nil
	default:
		return "", fmt.Errorf("unsupported TUI form %q", form.Kind)
	}
}

func (a *Application) tuiDoctorSnapshot(ctx context.Context, refresh bool) ([]doctorCheck, bool) {
	checks := make([]doctorCheck, 0, 12)
	add := func(name, status, message string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Message: message})
	}
	if err := ensurePrivateDir(a.paths.ConfigDir); err != nil {
		add("config_dir", "error", err.Error())
	} else {
		add("config_dir", "ok", a.paths.ConfigDir)
	}
	if settings, err := a.loadSettings(); err != nil {
		add("config", "error", err.Error())
	} else {
		add("config", "ok", fmt.Sprintf("schema %d", settings.Schema))
	}
	accounts, err := a.store.Load(false)
	if err != nil {
		add("accounts", "error", err.Error())
	} else {
		add("accounts", "ok", fmt.Sprintf("%d account(s)", accounts.Len()))
		plaintext, missing := 0, 0
		for _, email := range accounts.Order {
			account := accounts.ByEmail[email]
			if getString(account, "token_data") != "" {
				plaintext++
			}
			if _, tokenErr := a.accountToken(ctx, account); tokenErr != nil {
				missing++
			}
		}
		if plaintext > 0 {
			add("vault_migration", "warning", fmt.Sprintf("%d account(s) still use legacy token_data", plaintext))
		}
		if missing > 0 {
			add("vault_entries", "error", fmt.Sprintf("%d account secret(s) cannot be read", missing))
		}
	}
	current := a.credentials.Current(ctx)
	switch {
	case current == "":
		add("active_session", "warning", "no active Antigravity credential detected")
	case decodeToken(current) == nil:
		add("active_session", "error", "active credential is not a recognized OAuth token")
	default:
		add("active_session", "ok", "OAuth credential detected")
	}
	if runtime.GOOS == "windows" {
		add("platform", "ok", runtime.GOOS+"/"+runtime.GOARCH+" uses Credential Manager")
	} else {
		add("platform", "ok", runtime.GOOS+"/"+runtime.GOARCH)
	}
	if _, err := os.Stat(a.paths.History); err == nil {
		add("history", "ok", a.paths.History)
	} else {
		add("history", "ok", "not created yet")
	}
	if refresh {
		if release, releaseErr := a.releaseAssetCheck(ctx); releaseErr != nil {
			add("update_asset", "warning", releaseErr.Error())
		} else if available, _ := release["available"].(bool); available {
			add("update_asset", "ok", fmt.Sprintf("v%v asset %v is available", release["latest"], release["asset"]))
		} else {
			add("update_asset", "error", fmt.Sprintf("v%v is missing asset %v", release["latest"], release["asset"]))
		}
	}
	healthy := true
	for _, check := range checks {
		if check.Status == "error" {
			healthy = false
			break
		}
	}
	return checks, healthy
}

func (a *Application) tuiExportBackup(ctx context.Context, path, passphrase string, includeSecrets bool) (string, error) {
	path = firstString(strings.TrimSpace(path), "agy-swap-backup.json")
	plaintext, err := a.backupDocument(ctx, includeSecrets)
	if err != nil {
		return "", err
	}
	output := plaintext
	if includeSecrets {
		envelope, encryptErr := encryptBackup(passphrase, plaintext)
		if encryptErr != nil {
			return "", encryptErr
		}
		output, err = json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return "", err
		}
	}
	if err := atomicWrite(path, append(output, '\n'), 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("Backup written to %s", path), nil
}

func (a *Application) tuiImportBackup(ctx context.Context, path, passphrase string, merge bool) (string, error) {
	count, migrated, err := a.importBackup(ctx, strings.TrimSpace(path), passphrase, merge)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Imported %d account(s); migrated %d secret(s)", count, migrated), nil
}

func (a *Application) tuiVerifyBackup(path, passphrase string) (string, error) {
	if _, err := readBackup(strings.TrimSpace(path), passphrase); err != nil {
		return "", err
	}
	return "Backup is valid", nil
}

// toggleAutoNext toggles the auto_next setting via store.UpdateSettings,
// updating in-memory state only upon successful persistence.
func (a *Application) toggleAutoNext(state *tuiState) bool {
	if a == nil || a.store == nil || state == nil {
		return false
	}
	if state.job != nil && !state.job.Done {
		return false
	}
	updated, err := a.store.UpdateSettings(func(s *AppSettings) error {
		s.UI.AutoNext = !s.UI.AutoNext
		return nil
	})
	if err != nil {
		state.showToast("Could not update auto-next: "+err.Error(), "error")
		return false
	}
	state.settings = updated
	state.settingsLoaded = true
	if updated.UI.AutoNext {
		state.showToast("Auto-next enabled", "success")
	} else {
		state.showToast("Auto-next disabled", "info")
	}
	return true
}

// handleAutoNext evaluates whether the active account has dropped below quota thresholds
// and safely executes a session switch to the best eligible candidate under SessionLock.
func (a *Application) handleAutoNext(
	ctx context.Context,
	state *tuiState,
	event tuiAccountsEvent,
	current string,
	refreshRevision uint64,
	now time.Time,
) string {
	if a == nil || a.store == nil || a.credentials == nil || state == nil || event.accounts == nil {
		return current
	}
	// Disabled in live demo mode
	if a.demo {
		return current
	}
	// Stale revision: slower refresh must never trigger auto-apply
	if event.revision != refreshRevision {
		return current
	}
	// AutoNext must be ON at completion
	if !state.settings.UI.AutoNext {
		return current
	}
	// Skip and reconsider next refresh when busy or not in normal browse mode
	if state.mode != tuiBrowse || (state.job != nil && !state.job.Done) || state.form != nil {
		return current
	}
	// Session identity changed since refresh started: protect manual/external switch
	if event.sessionToken == "" || event.sessionToken != current {
		return current
	}
	// Do not act on cached old failures, store/storage error
	if hasQuotaError(event.quotaErrors, "store") || hasQuotaError(event.quotaErrors, "storage") {
		return current
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	// Active account identity
	activeEmail := state.active
	if activeEmail == "" {
		activeEmail = a.activeHint(event.accounts, current)
	}
	if activeEmail == "" && a.credentials != nil {
		activeEmail = a.credentials.StoredActiveEmail()
	}
	if activeEmail == "" {
		return current
	}
	activeAccount := findAccountCaseInsensitive(event.accounts, activeEmail)
	if activeAccount == nil {
		return current
	}

	// Fresh successful active response required: no refresh error on active account
	if hasQuotaError(event.quotaErrors, activeEmail) {
		return current
	}

	// Fresh snapshot required
	if !isSnapshotFresh(activeAccount, now) {
		return current
	}

	// Active account quota must be below threshold (5h < 15% OR weekly < 8%)
	if !ShouldAutoNext(activeAccount, now) {
		return current
	}

	// Select best eligible candidate (excludes active, cooldowns, errors, stale, below-threshold)
	candidateAccount, ok := SelectAutoNextCandidate(event.accounts, activeEmail, state.settings, event.quotaErrors, now)
	if !ok {
		// No eligible candidate retains current with concise info
		state.showToast("No eligible auto-next account", "info")
		return current
	}
	candidateEmail := getString(candidateAccount, "email")
	if candidateEmail == "" {
		state.showToast("No eligible auto-next account", "info")
		return current
	}

	// Short native transaction under SessionLock
	lock, err := acquireFileLock(a.paths.SessionLock)
	if err != nil {
		state.showToast("Auto-switch failed: "+err.Error(), "error")
		return current
	}
	defer func() { _ = lock.Close() }()

	// Recheck exact identity under SessionLock before applying, protecting concurrent/external switches
	nowSession := a.credentials.Current(ctx)
	if nowSession != event.sessionToken || nowSession != current {
		return current
	}
	if a.credentials.Secure(ctx) != event.secureToken {
		return current
	}
	if a.credentials.OAuthToken() != event.oauthToken {
		return current
	}

	token, tokenErr := a.accountToken(ctx, candidateAccount)
	if tokenErr != nil {
		state.showToast("Auto-switch failed: "+tokenErr.Error(), "error")
		return current
	}

	// Reuse applyUnlocked rollback behavior
	if !a.credentials.applyUnlocked(ctx, token, candidateEmail) {
		state.showToast("Auto-switch failed", "error")
		return current
	}

	// Record switch in history once
	a.recordSwitch(candidateEmail)

	// Update current, active, state.current, selection consistently on success
	current = a.credentials.Current(ctx)
	if current == "" {
		current = token
	}
	state.current = current
	state.active = candidateEmail
	state.selectedEmail = candidateEmail
	state.resolvingToken = ""
	state.clampSelection()
	state.showToast("Auto-switched to "+candidateEmail, "success")
	state.beginAnimation("success", 360*time.Millisecond)

	return current
}

func findAccountCaseInsensitive(accounts *Accounts, email string) Account {
	if accounts == nil || email == "" {
		return nil
	}
	if acc, ok := accounts.ByEmail[email]; ok {
		return acc
	}
	for k, acc := range accounts.ByEmail {
		if strings.EqualFold(k, email) {
			return acc
		}
	}
	return nil
}
