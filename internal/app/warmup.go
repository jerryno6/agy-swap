package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const warmupModel = "gemini-3.8-flash-low"

type WarmupQuota struct {
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetAt           string  `json:"reset_at"`
}

type WarmupResult struct {
	Account   string         `json:"account"`
	Model     string         `json:"model"`
	WireModel string         `json:"wire_model,omitempty"`
	Sent      bool           `json:"sent"`
	Verified  bool           `json:"verified"`
	Before    *WarmupQuota   `json:"before,omitempty"`
	After     *WarmupQuota   `json:"after,omitempty"`
	Snapshot  map[string]any `json:"quota_snapshot,omitempty"`
}

func (r WarmupResult) Message() string {
	if !r.Verified {
		return "Sent 'hi' to " + r.Account + "; 5h quota decrease not verified"
	}
	// One "hi" costs ~0.0002% of the 5h bucket, so whole percents hide it.
	return fmt.Sprintf("✓ 5h quota decreased for %s: %.4f%% → %.4f%%", r.Account,
		quotaPercent(r.Before.RemainingFraction, 4), quotaPercent(r.After.RemainingFraction, 4))
}

func warmupFiveHour(snapshot map[string]any) (*WarmupQuota, error) {
	_, fiveHour, _ := geminiQuotaBuckets(Account{"quota_snapshot": snapshot})
	if !fiveHour.found || fiveHour.resetAt.IsZero() || math.IsNaN(fiveHour.fraction) || math.IsInf(fiveHour.fraction, 0) {
		return nil, errors.New("Google returned no valid Gemini 5h quota")
	}
	return &WarmupQuota{RemainingFraction: fiveHour.fraction, ResetAt: isoTime(fiveHour.resetAt)}, nil
}

// Write a new vault reference so a failed accounts save cannot overwrite the
// credential still referenced on disk. Only retire the old secret after commit.
func (a *Application) saveWarmupToken(ctx context.Context, accounts *Accounts, account Account, token string) error {
	saved := captureSecretFields(account)
	oldRef := getString(account, "secret_ref")
	newRef := ""
	if a.vault != nil {
		newRef = accountSecretRef(getString(account, "email")) + ":" + rand.Text()
		if !a.vault.Set(ctx, newRef, token) {
			return errors.New("cannot save refreshed credential to account vault")
		}
		account["secret_ref"] = newRef
		delete(account, "token_data")
	} else {
		account["token_data"] = token
		delete(account, "secret_ref")
	}
	account["token_hash"] = hashToken(token)
	rememberTokenExpiry(account, token)
	if err := a.store.Save(accounts); err != nil {
		restoreSecretFields(account, saved)
		if newRef != "" {
			_ = a.vault.Delete(context.WithoutCancel(ctx), newRef)
		}
		return fmt.Errorf("save refreshed credential: %w", err)
	}
	if oldRef != "" {
		a.deleteReplacedSecrets(context.WithoutCancel(ctx), []string{oldRef})
	}
	return nil
}

func (a *Application) fetchWarmupQuota(ctx context.Context, accounts *Accounts, account Account) (map[string]any, error) {
	copyAccount := make(Account, len(account))
	for key, value := range account {
		copyAccount[key] = value
	}
	snapshot, fetchErr := a.quota.Fetch(ctx, copyAccount)
	if token := getString(copyAccount, "token_data"); token != "" && token != getString(account, "token_data") {
		if err := a.saveWarmupToken(ctx, accounts, account, token); err != nil {
			return nil, err
		}
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	if _, err := warmupFiveHour(snapshot); err != nil {
		return nil, err
	}
	previous := account["quota_snapshot"]
	account["quota_snapshot"] = snapshot
	if err := a.store.Save(accounts); err != nil {
		if previous == nil {
			delete(account, "quota_snapshot")
		} else {
			account["quota_snapshot"] = previous
		}
		return nil, fmt.Errorf("save quota: %w", err)
	}
	return snapshot, nil
}

func validateWarmupResponse(result map[string]any) error {
	if result["error"] != nil {
		return errors.New("generation returned an API error")
	}
	response := getMap(result["response"])
	if response == nil || response["error"] != nil {
		return errors.New("generation returned no valid response")
	}
	if reason := getString(getMap(response["promptFeedback"]), "blockReason"); reason != "" && reason != "BLOCK_REASON_UNSPECIFIED" {
		return errors.New("generation prompt was blocked")
	}
	for _, raw := range getSlice(response["candidates"]) {
		candidate := getMap(raw)
		if getString(candidate, "finishReason") != "STOP" {
			continue
		}
		for _, rawPart := range getSlice(getMap(candidate["content"])["parts"]) {
			part := getMap(rawPart)
			thought, _ := part["thought"].(bool)
			if !thought && strings.TrimSpace(getString(part, "text")) != "" {
				return nil
			}
		}
	}
	return errors.New("generation returned no completed text reply")
}

func (a *Application) SendGeminiWarmup(ctx context.Context, email string) (WarmupResult, error) {
	result := WarmupResult{Account: email, Model: warmupModel}
	if !a.warmupMu.TryLock() {
		return result, errors.New("warm-up is already running")
	}
	defer a.warmupMu.Unlock()
	accounts, err := a.store.Load(false)
	if err != nil {
		return result, err
	}
	account, ok := accounts.ByEmail[email]
	if !ok {
		return result, fmt.Errorf("account %s not found", email)
	}
	token, err := a.accountToken(ctx, account)
	if err != nil {
		return result, err
	}
	if !tokenMatchesEmail(token, email) {
		return result, errors.New("saved token identity does not match the account")
	}
	access, refreshed, err := a.http.accessTokenData(ctx, token)
	if err != nil {
		return result, err
	}
	if !tokenMatchesEmail(refreshed, email) {
		return result, errors.New("refreshed token identity does not match the account")
	}
	if refreshed != token {
		if err := a.saveWarmupToken(ctx, accounts, account, refreshed); err != nil {
			return result, err
		}
	}
	info, err := a.http.cloudPost(ctx, access, "loadCodeAssist", map[string]any{"metadata": map[string]any{"ideType": "ANTIGRAVITY"}})
	if err != nil {
		return result, err
	}
	project := getString(info, "cloudaicompanionProject")
	if strings.TrimSpace(project) == "" {
		return result, errors.New("Google returned no Code Assist project")
	}
	available, err := a.http.cloudPost(ctx, access, "fetchAvailableModels", map[string]any{"project": project})
	if err != nil {
		return result, fmt.Errorf("resolve warm-up model: %w", err)
	}
	models := getMap(available["models"])
	result.WireModel = warmupModel
	if getMap(models[warmupModel]) == nil {
		// The CLI exposes effort aliases; some accounts advertise only the
		// corresponding tiered model in the Code Assist API catalog.
		if getMap(models["gemini-3.8-flash-tiered"]) == nil {
			return result, errors.New("Gemini 3.8 Flash (Low) is unavailable for this account")
		}
		result.WireModel = "gemini-3.8-flash-tiered"
	}
	snapshot, err := a.fetchWarmupQuota(ctx, accounts, account)
	if err != nil {
		return result, fmt.Errorf("read baseline quota: %w", err)
	}
	result.Snapshot = snapshot
	result.Before, _ = warmupFiveHour(snapshot)
	latestToken, err := a.accountToken(ctx, account)
	if err != nil {
		return result, err
	}
	access = getString(tokenObject(decodeToken(latestToken)), "access_token")
	// Generation must never replay after redirects or the optional TLS fallback.
	single := &HTTPService{client: a.http.client, insecure: a.http.insecure, errOut: a.http.errOut, cloudAPI: a.http.cloudAPI, singleAttempt: true}
	response, err := single.cloudPost(ctx, access, "generateContent", map[string]any{
		"project": project, "model": result.WireModel,
		"userAgent": "antigravity", "requestType": "agent", "requestId": "agent-" + rand.Text(),
		"request": map[string]any{
			"sessionId":        rand.Text(),
			"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
			"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "LOW"}},
		},
	})
	if err != nil {
		return result, fmt.Errorf("generation failed; delivery is uncertain and will not be retried: %w", err)
	}
	if err := validateWarmupResponse(response); err != nil {
		return result, err
	}
	result.Sent = true
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-timer.C:
			case <-verifyCtx.Done():
				timer.Stop()
				return result, fmt.Errorf("sent 'hi', but quota verification failed: %w", verifyCtx.Err())
			}
		}
		snapshot, err := a.fetchWarmupQuota(verifyCtx, accounts, account)
		if err != nil {
			return result, fmt.Errorf("sent 'hi', but quota verification failed: %w", err)
		}
		result.Snapshot = snapshot
		result.After, _ = warmupFiveHour(snapshot)
		if result.After.RemainingFraction < result.Before.RemainingFraction {
			result.Verified = true
			return result, nil
		}
	}
	return result, nil
}
