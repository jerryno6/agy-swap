package app

import "time"

// fullGeminiFiveHour requires every explicit Gemini 5h bucket to be exactly
// full. Do not use display rounding or the legacy daily-window aliases here.
func fullGeminiFiveHour(snapshot map[string]any) bool {
	if hasLegacyGeminiFiveHour(snapshot) {
		return false
	}
	found := false
	for _, rawGroup := range getSlice(snapshot["groups"]) {
		group := getMap(rawGroup)
		if getString(group, "id") != selectionQuotaFamily {
			continue
		}
		for _, rawBucket := range getSlice(group["buckets"]) {
			bucket := getMap(rawBucket)
			if quotaBucketDisabled(bucket) {
				continue
			}
			if getString(bucket, "window") != "5h" {
				continue
			}
			fraction, ok := getFloat(bucket["remaining_fraction"])
			reset, err := parseUTC(getString(bucket, "reset_at"))
			if !ok || fraction != 1 || err != nil || reset.IsZero() {
				return false
			}
			found = true
		}
	}
	return found
}

func autoWarmupReady(account Account, now time.Time) bool {
	if !isSnapshotFresh(account, now) || !fullGeminiFiveHour(getMap(account["quota_snapshot"])) {
		return false
	}
	if raw, exists := account["warmup_not_before"]; exists {
		value, ok := raw.(string)
		notBefore, err := parseUTC(value)
		observed, observedErr := parseUTC(getString(getMap(account["quota_snapshot"]), "observed_at"))
		// Fail closed on corrupt guards and observations from before the guard expired.
		if !ok || err != nil || observedErr != nil || now.Before(notBefore) || observed.Before(notBefore) {
			return false
		}
	}
	return true
}

// Freeze P=1 and P=2 before applying warm-up checks: an ineligible top account
// must never be replaced by P=3. The synced set excludes cache hits and failures.
func tuiAutoWarmupTargets(state *tuiState, synced map[string]bool, now time.Time) []string {
	if state == nil || state.accounts == nil {
		return nil
	}
	ranks := tuiAutoNextRanks(state, now)
	top := [2]string{}
	for email, rank := range ranks {
		if rank >= 1 && rank <= len(top) {
			top[rank-1] = email
		}
	}
	var targets []string
	for _, email := range top {
		if email != "" && synced != nil && synced[email] && autoWarmupReady(state.accounts.ByEmail[email], now) {
			targets = append(targets, email)
		}
	}
	return targets
}
