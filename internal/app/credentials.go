package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

type CredentialBackend interface {
	Get(context.Context) string
	Set(context.Context, string) bool
	Delete(context.Context) bool
}

// sessionPreparer lets a platform adapt a validated account token to the exact
// representation its active-session store accepts before transaction checks.
// Implementations require a successful update to that authoritative store;
// callers must not report success based only on the mirrored OAuth files.
type sessionPreparer interface {
	PrepareSession(string) (string, error)
}

type authoritativeSecureStore interface {
	AuthoritativeSecure() bool
}

type osCredentialBackend struct{}

func (osCredentialBackend) Get(ctx context.Context) string { return platformCredentialGet(ctx) }
func (osCredentialBackend) Set(ctx context.Context, token string) bool {
	return platformCredentialSet(ctx, token)
}
func (osCredentialBackend) Delete(ctx context.Context) bool { return platformCredentialDelete(ctx) }
func (osCredentialBackend) AuthoritativeSecure() bool {
	return runtime.GOOS == "darwin"
}

type Credentials struct {
	paths   Paths
	backend CredentialBackend
}

func NewCredentials(paths Paths) *Credentials {
	return &Credentials{paths: paths, backend: osCredentialBackend{}}
}

func (c *Credentials) Secure(ctx context.Context) string {
	if c == nil || c.backend == nil {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.backend.Get(ctx)
}

func (c *Credentials) OAuthToken() string {
	if c == nil {
		return ""
	}
	return c.readOAuthToken()
}

// secureIsSource reports whether the OS secure store is what agy actually
// authenticates from (Windows Credential Manager, macOS keychain). There the
// OAuth files are only a mirror that agy-swap writes; a running agy process can
// overwrite the secure store without touching them.
func (c *Credentials) secureIsSource() bool {
	if c == nil || c.backend == nil {
		return false
	}
	if _, ok := c.backend.(sessionPreparer); ok {
		return true
	}
	if auth, ok := c.backend.(authoritativeSecureStore); ok && auth.AuthoritativeSecure() {
		return true
	}
	return false
}

// Current returns the session agy will use. When the secure store is
// authoritative it wins and the OAuth file is only a fallback for an empty
// store. Elsewhere the file is preferred, as before.
func (c *Credentials) Current(ctx context.Context) string {
	if c.secureIsSource() {
		if token := c.Secure(ctx); token != "" {
			return token
		}
		return c.OAuthToken()
	}
	if token := c.OAuthToken(); token != "" {
		return token
	}
	return c.Secure(ctx)
}

// SessionDrift describes the identity in the secure store (what agy uses)
// against the identity in the agy-swap mirror files.
type SessionDrift struct {
	SecureEmail string
	FileEmail   string
	Drift       bool
}

// SessionDrift reports whether the secure store and the OAuth file disagree.
// Drift is only reported when the secure store is authoritative and both
// identities are known; email hints are read from either token format.
func (c *Credentials) SessionDrift(ctx context.Context) SessionDrift {
	if c == nil {
		return SessionDrift{}
	}
	result := SessionDrift{
		SecureEmail: extractEmailHint(c.Secure(ctx)),
		FileEmail:   extractEmailHint(c.OAuthToken()),
	}
	if result.FileEmail == "" {
		result.FileEmail = c.StoredActiveEmail()
	}
	result.Drift = c.secureIsSource() && result.SecureEmail != "" && result.FileEmail != "" && !strings.EqualFold(result.SecureEmail, result.FileEmail)
	return result
}

// Message is the user-facing description of a drifted session.
func (d SessionDrift) Message() string {
	return fmt.Sprintf("session drift: agy will use %s but the agy-swap session file says %s (a running agy process likely refreshed its token into the keyring)", d.SecureEmail, d.FileEmail)
}

// verifyActive confirms that the secure store holds the account just applied.
// It is a no-op where the secure store is not authoritative.
func (c *Credentials) verifyActive(ctx context.Context, tokenData, email string) bool {
	if !c.secureIsSource() {
		return true
	}
	secure := c.Secure(ctx)
	if secure == "" {
		return false
	}
	if hint := extractEmailHint(secure); hint != "" {
		return strings.EqualFold(hint, email)
	}
	if preparer, ok := c.backend.(sessionPreparer); ok {
		if prepared, err := preparer.PrepareSession(tokenData); err == nil {
			return secure == prepared
		}
	}
	return secure == tokenData
}

// StoredActiveEmail returns the last identity written by the Antigravity
// credential flow. It is a local hint used to paint the active badge before a
// remote userinfo lookup completes; callers still verify it against accounts.
func (c *Credentials) StoredActiveEmail() string {
	data, err := os.ReadFile(c.paths.GoogleAccounts)
	if err != nil {
		return ""
	}
	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}
	return normalizeEmail(getString(parsed, "active"))
}

func (c *Credentials) Set(ctx context.Context, token string) bool {
	return c.backend.Set(ctx, token)
}
func (c *Credentials) Delete(ctx context.Context) bool { return c.backend.Delete(ctx) }

func (c *Credentials) readOAuthToken() string {
	if c == nil {
		return ""
	}
	data, err := os.ReadFile(c.paths.OAuthToken)
	if err != nil {
		return ""
	}
	var parsed any
	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}
	return "go-keyring-base64:" + base64.StdEncoding.EncodeToString(data)
}

func (c *Credentials) writeOAuthFiles(tokenData, email string) bool {
	decoded := decodeToken(tokenData)
	inner := tokenObject(decoded)
	if inner == nil || getString(inner, "access_token") == "" {
		return false
	}
	paths := []string{c.paths.OAuthToken, c.paths.OAuthCredentials}
	if email != "" {
		paths = append(paths, c.paths.GoogleAccounts)
	}
	snapshot, err := snapshotFiles(paths...)
	if err != nil {
		return false
	}
	creds := map[string]any{"access_token": getString(inner, "access_token"), "refresh_token": getString(inner, "refresh_token"), "scope": firstString(inner["scope"], "https://www.googleapis.com/auth/cloud-platform openid https://www.googleapis.com/auth/userinfo.email"), "token_type": firstString(inner["token_type"], "Bearer"), "id_token": getString(inner, "id_token")}
	if expiry, ok := inner["expiry_date"]; ok {
		creds["expiry_date"] = expiry
	} else {
		creds["expiry_date"] = 0
	}
	if err := atomicWriteJSON(c.paths.OAuthToken, decoded); err != nil {
		_ = restoreFiles(snapshot)
		return false
	}
	if err := atomicWriteJSON(c.paths.OAuthCredentials, creds); err != nil {
		_ = restoreFiles(snapshot)
		return false
	}
	if email != "" {
		ga := map[string]any{"active": email, "old": []any{}}
		if data, readErr := os.ReadFile(c.paths.GoogleAccounts); readErr == nil {
			var existing map[string]any
			if json.Unmarshal(data, &existing) == nil {
				oldActive := getString(existing, "active")
				var oldList []any
				if raw, ok := existing["old"].([]any); ok {
					oldList = raw
				}
				if oldActive != "" && oldActive != email && !containsString(oldList, oldActive) {
					oldList = append([]any{oldActive}, oldList...)
				}
				filtered := make([]any, 0, len(oldList))
				for _, item := range oldList {
					if value, ok := item.(string); ok && value != email {
						filtered = append(filtered, value)
					}
				}
				ga = map[string]any{"active": email, "old": filtered}
			}
		}
		if err := atomicWriteJSON(c.paths.GoogleAccounts, ga); err != nil {
			_ = restoreFiles(snapshot)
			return false
		}
	}
	return true
}

func containsString(values []any, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// applyUnlocked applies the account and then reads the secure store back to
// confirm agy will really use it; a mismatch is reported as failure.
func (c *Credentials) applyUnlocked(ctx context.Context, tokenData, email string) bool {
	if !c.applyUnlockedRaw(ctx, tokenData, email) {
		return false
	}
	return c.verifyActive(ctx, tokenData, normalizeEmail(email))
}

func (c *Credentials) applyUnlockedRaw(ctx context.Context, tokenData, email string) bool {
	if decodeToken(tokenData) == nil {
		return false
	}
	email = normalizeEmail(email)
	if email == "" {
		return false
	}
	if !tokenMatchesEmail(tokenData, email) {
		return false
	}
	prepared := tokenData
	requireSecure := false
	if preparer, ok := c.backend.(sessionPreparer); ok {
		requireSecure = true
		var err error
		prepared, err = preparer.PrepareSession(tokenData)
		if err != nil {
			return false
		}
	}
	if auth, ok := c.backend.(authoritativeSecureStore); ok && auth.AuthoritativeSecure() {
		requireSecure = true
	}
	previous := c.Secure(ctx)
	if previous == prepared {
		return c.writeOAuthFiles(tokenData, email)
	}
	updated := c.Set(ctx, prepared)
	if !updated {
		current := c.Secure(ctx)
		switch {
		case current == prepared:
			updated = true
		case previous != "":
			if current != previous {
				_ = c.Set(ctx, previous)
			}
			return false
		case current != "":
			return false
		case requireSecure:
			return false
		}
	}
	if c.writeOAuthFiles(tokenData, email) {
		return true
	}
	if updated {
		if previous != "" {
			_ = c.Set(ctx, previous)
		} else {
			_ = c.Delete(ctx)
		}
	}
	return false
}

func tokenMatchesEmail(tokenData, email string) bool {
	claimed := extractEmailHint(tokenData)
	return claimed == "" || claimed == normalizeEmail(email)
}

func (c *Credentials) Apply(ctx context.Context, tokenData, email string) bool {
	lock, err := acquireFileLock(c.paths.SessionLock)
	if err != nil {
		return false
	}
	defer func() { _ = lock.Close() }()
	return c.applyUnlocked(ctx, tokenData, email)
}

func (c *Credentials) deleteOAuthFiles() bool {
	ok := true
	for _, path := range []string{c.paths.OAuthToken, c.paths.OAuthCredentials, c.paths.GoogleAccounts} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			ok = false
		}
	}
	return ok
}

func (c *Credentials) clearUnlocked(ctx context.Context) bool {
	_ = c.Delete(ctx)
	_ = c.deleteOAuthFiles()
	return c.Secure(ctx) == "" && c.OAuthToken() == ""
}
func (c *Credentials) Clear(ctx context.Context) bool {
	lock, err := acquireFileLock(c.paths.SessionLock)
	if err != nil {
		return false
	}
	defer func() { _ = lock.Close() }()
	return c.clearUnlocked(ctx)
}

// agyProcessNote warns that already-running agy processes keep their previous
// account and may overwrite the keyring when they refresh their token. It
// returns "" when nothing applies (always on non-Windows platforms).
func agyProcessNote() string {
	n := runningAgyProcesses()
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d running agy process(es) keep their previous account until restarted and may overwrite the keyring when they refresh their token.", n)
}
