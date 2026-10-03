//go:build windows

package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSessionIDToken(email, padding string) string {
	claims, _ := json.Marshal(map[string]any{
		"iss": "https://accounts.google.com", "email": email,
		"email_verified": true, "padding": padding,
	})
	return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func windowsDuplicateIDTokenPayload(email, padding string) string {
	oldID := testSessionIDToken("old@example.com", strings.Repeat("o", 850))
	newID := testSessionIDToken(email, strings.Repeat("n", 850))
	payload, _ := json.Marshal(map[string]any{
		"auth_method": "oauth",
		"id_token":    oldID,
		"token": map[string]any{
			"access_token": "access", "refresh_token": "refresh", "scope": "openid",
			"token_type": "Bearer", "expiry_date": int64(1893456000000), "expiry": "2030-01-01T00:00:00Z",
			"id_token": newID, "extension": padding,
		},
	})
	return string(payload)
}

func TestWindowsSessionCredentialNormalizesWrappedDuplicateIDToken(t *testing.T) {
	payload := windowsDuplicateIDTokenPayload("user@example.com", "")
	newID := testSessionIDToken("user@example.com", strings.Repeat("n", 850))
	wrapped := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(payload))
	if len(wrapped) <= windowsCredentialBlobLimit {
		t.Fatal("fixture must exceed the Windows credential limit before normalization")
	}

	got, err := windowsSessionCredential(wrapped)
	if err != nil {
		t.Fatalf("normalize credential: %v", err)
	}
	if len([]byte(got)) > windowsCredentialBlobLimit {
		t.Fatalf("normalized credential is %d bytes, over the %d-byte limit", len(got), windowsCredentialBlobLimit)
	}
	var normalized map[string]any
	if err := json.Unmarshal([]byte(got), &normalized); err != nil {
		t.Fatalf("normalized payload is not JSON: %v", err)
	}
	nested := getMap(normalized["token"])
	if _, exists := nested["id_token"]; exists {
		t.Fatal("redundant nested id_token was retained")
	}
	if getString(normalized, "id_token") != newID {
		t.Fatal("the latest nested id_token was not promoted to the root")
	}
	expiryDate, expiryDateOK := numberInt(nested["expiry_date"])
	if getString(nested, "access_token") != "access" || getString(nested, "refresh_token") != "refresh" || getString(nested, "scope") != "openid" || getString(nested, "token_type") != "Bearer" || !expiryDateOK || expiryDate != 1893456000000 || getString(nested, "expiry") != "2030-01-01T00:00:00Z" || getString(normalized, "auth_method") != "oauth" {
		t.Fatal("normalization dropped a required authentication field")
	}
	if gotEmail := extractEmailHint(got); gotEmail != "user@example.com" || !tokenMatchesEmail(got, gotEmail) {
		t.Fatalf("normalized identity = %q", gotEmail)
	}
}

func TestWindowsSessionCredentialRawJSONNormalizationIsIdempotent(t *testing.T) {
	first, err := windowsSessionCredential(windowsDuplicateIDTokenPayload("user@example.com", ""))
	if err != nil {
		t.Fatalf("normalize raw JSON: %v", err)
	}
	second, err := windowsSessionCredential(first)
	if err != nil {
		t.Fatalf("normalize normalized JSON: %v", err)
	}
	if second != first {
		t.Fatal("normalizing raw JSON twice changed the credential")
	}
}

func TestWindowsSessionCredentialRejectsUnshrinkableBlob(t *testing.T) {
	id := testSessionIDToken("user@example.com", "")
	payload, err := json.Marshal(map[string]any{
		"auth_method": "oauth", "id_token": id,
		"token":              map[string]any{"access_token": "access", "refresh_token": "refresh", "scope": "openid", "token_type": "Bearer", "expiry_date": int64(1893456000000), "expiry": "2030-01-01T00:00:00Z", "id_token": id},
		"provider_extension": strings.Repeat("x", windowsCredentialBlobLimit),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := windowsSessionCredential(string(payload)); err != errWindowsCredentialTooLarge {
		t.Fatalf("oversized credential error = %v", err)
	}
}

type windowsNormalizedCredentialBackend struct {
	value          string
	prepareCalls   int
	setCalls       int
	failSet        bool
	mutateThenFail bool
	afterSet       func()
}

func TestWindowsSessionCredentialEnforcesExactByteLimit(t *testing.T) {
	base, err := json.Marshal(map[string]any{"padding": ""})
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("x", windowsCredentialBlobLimit-len(base))
	boundary, err := json.Marshal(map[string]any{"padding": padding})
	if err != nil {
		t.Fatal(err)
	}
	if len(boundary) != windowsCredentialBlobLimit {
		t.Fatalf("boundary fixture is %d bytes, want %d", len(boundary), windowsCredentialBlobLimit)
	}
	if got, err := windowsSessionCredential(string(boundary)); err != nil || len(got) != windowsCredentialBlobLimit {
		t.Fatalf("exact-limit credential: bytes=%d, err=%v", len(got), err)
	}
	overLimit, err := json.Marshal(map[string]any{"padding": padding + "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := windowsSessionCredential(string(overLimit)); err != errWindowsCredentialTooLarge {
		t.Fatalf("over-limit credential error = %v", err)
	}
}

func TestWindowsSessionCredentialRejectsInvalidJSONAndBase64(t *testing.T) {
	for _, value := range []string{"not-json", "go-keyring-base64:%%%"} {
		if _, err := windowsSessionCredential(value); err == nil {
			t.Fatalf("accepted invalid credential payload %q", value)
		}
	}
}

func (b *windowsNormalizedCredentialBackend) Get(context.Context) string { return b.value }
func (b *windowsNormalizedCredentialBackend) Set(_ context.Context, value string) bool {
	b.setCalls++
	if b.failSet && !b.mutateThenFail {
		return false
	}
	b.value = value
	if b.afterSet != nil {
		b.afterSet()
	}
	if b.failSet {
		return false
	}
	return true
}
func (b *windowsNormalizedCredentialBackend) Delete(context.Context) bool { b.value = ""; return true }
func (b *windowsNormalizedCredentialBackend) PrepareSession(value string) (string, error) {
	b.prepareCalls++
	return windowsSessionCredential(value)
}

func TestApplyPreservesPreviousCredentialWhenWindowsPayloadCannotFit(t *testing.T) {
	paths := testPaths(t)
	backend := &windowsNormalizedCredentialBackend{value: `{"existing":"session"}`}
	credentials := NewCredentials(paths)
	credentials.backend = backend
	id := testSessionIDToken("user@example.com", "")
	payload, err := json.Marshal(map[string]any{
		"auth_method": "oauth", "id_token": id,
		"token":              map[string]any{"access_token": "access", "refresh_token": "refresh", "scope": "openid", "token_type": "Bearer", "expiry_date": int64(1893456000000), "expiry": "2030-01-01T00:00:00Z", "id_token": id},
		"provider_extension": strings.Repeat("x", windowsCredentialBlobLimit),
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Apply(context.Background(), string(payload), "user@example.com") {
		t.Fatal("apply succeeded with an unshrinkable Windows credential")
	}
	if backend.value != `{"existing":"session"}` {
		t.Fatal("failed apply changed the existing session credential")
	}
}

func TestApplyRejectsUnshrinkableCredentialWithoutPreviousSession(t *testing.T) {
	paths := testPaths(t)
	backend := &windowsNormalizedCredentialBackend{}
	credentials := NewCredentials(paths)
	credentials.backend = backend
	id := testSessionIDToken("user@example.com", "")
	payload, err := json.Marshal(map[string]any{
		"auth_method": "oauth", "id_token": id,
		"token":              map[string]any{"access_token": "access", "refresh_token": "refresh", "scope": "openid", "token_type": "Bearer", "expiry_date": int64(1893456000000), "expiry": "2030-01-01T00:00:00Z", "id_token": id},
		"provider_extension": strings.Repeat("x", windowsCredentialBlobLimit),
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Apply(context.Background(), string(payload), "user@example.com") {
		t.Fatal("apply accepted an unshrinkable credential without an active session")
	}
	if backend.value != "" || backend.setCalls != 0 {
		t.Fatal("unshrinkable credential reached the session backend")
	}
	for _, path := range []string{paths.OAuthToken, paths.OAuthCredentials, paths.GoogleAccounts} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected OAuth file state at %s: %v", path, err)
		}
	}
}

func TestApplyFailsWhenWindowsSessionSetFailsWithoutPreviousSession(t *testing.T) {
	paths := testPaths(t)
	backend := &windowsNormalizedCredentialBackend{failSet: true}
	credentials := NewCredentials(paths)
	credentials.backend = backend
	if credentials.Apply(context.Background(), windowsDuplicateIDTokenPayload("user@example.com", ""), "user@example.com") {
		t.Fatal("apply succeeded although the session backend rejected the credential")
	}
	if backend.setCalls != 1 || backend.value != "" {
		t.Fatal("failed session set changed backend state")
	}
	for _, path := range []string{paths.OAuthToken, paths.OAuthCredentials, paths.GoogleAccounts} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected OAuth file state at %s: %v", path, err)
		}
	}
}

func TestApplyUsesPreparedValueForRepeatAndFailedSetRecovery(t *testing.T) {
	t.Run("repeat skips secure write", func(t *testing.T) {
		paths := testPaths(t)
		backend := &windowsNormalizedCredentialBackend{}
		credentials := NewCredentials(paths)
		credentials.backend = backend
		payload := windowsDuplicateIDTokenPayload("user@example.com", "")
		if !credentials.Apply(context.Background(), payload, "user@example.com") || !credentials.Apply(context.Background(), payload, "user@example.com") {
			t.Fatal("apply failed")
		}
		if backend.setCalls != 1 {
			t.Fatalf("secure writes = %d, want 1", backend.setCalls)
		}
	})
	t.Run("failed call after update is recognized", func(t *testing.T) {
		paths := testPaths(t)
		backend := &windowsNormalizedCredentialBackend{failSet: true, mutateThenFail: true}
		credentials := NewCredentials(paths)
		credentials.backend = backend
		if !credentials.Apply(context.Background(), windowsDuplicateIDTokenPayload("user@example.com", ""), "user@example.com") {
			t.Fatal("apply did not recognize the prepared value after a failed Set result")
		}
	})
}

func TestApplyRestoresExactPreviousCredentialWhenOAuthSnapshotFails(t *testing.T) {
	paths := testPaths(t)
	previous := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(`{"existing":"session"}`))
	backend := &windowsNormalizedCredentialBackend{value: previous}
	credentials := NewCredentials(paths)
	credentials.backend = backend
	if err := os.MkdirAll(filepath.Dir(paths.OAuthCredentials), 0o700); err != nil {
		t.Fatal(err)
	}
	backend.afterSet = func() {
		if backend.setCalls == 1 {
			if err := os.Mkdir(paths.OAuthCredentials, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if credentials.Apply(context.Background(), windowsDuplicateIDTokenPayload("user@example.com", ""), "user@example.com") {
		t.Fatal("apply succeeded after OAuth file snapshot failed")
	}
	if backend.value != previous || backend.setCalls != 2 {
		t.Fatal("failed OAuth snapshot did not restore the exact previous session bytes")
	}
	for _, path := range []string{paths.OAuthToken, paths.GoogleAccounts} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed OAuth snapshot left unexpected path %s: %v", path, err)
		}
	}
}

func TestApplyRejectsIdentityMismatchWithoutChangingWindowsSession(t *testing.T) {
	paths := testPaths(t)
	backend := &windowsNormalizedCredentialBackend{value: `{"existing":"session"}`}
	credentials := NewCredentials(paths)
	credentials.backend = backend
	if credentials.Apply(context.Background(), windowsDuplicateIDTokenPayload("other@example.com", ""), "user@example.com") {
		t.Fatal("apply accepted a token for a different account")
	}
	if backend.value != `{"existing":"session"}` {
		t.Fatal("identity mismatch changed the existing session credential")
	}
	if backend.setCalls != 0 {
		t.Fatal("identity mismatch reached the session backend")
	}
}
