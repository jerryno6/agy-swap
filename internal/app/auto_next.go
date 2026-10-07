package app

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// AutoNext5hThreshold is the strict upper bound for the 5-hour rolling quota window (20%).
	AutoNext5hThreshold = 0.20

	// AutoNextWeeklyThreshold is the strict upper bound for the weekly quota window (15%).
	AutoNextWeeklyThreshold = 0.15

	// AutoNextMaxQuotaAge is the maximum allowed age of a quota snapshot before it is considered stale.
	AutoNextMaxQuotaAge = 2 * time.Minute
)

// AutoNextThresholdResult contains detailed results of quota threshold evaluation.
type AutoNextThresholdResult struct {
	Has5h     bool
	Min5h     float64
	HasWeekly bool
	MinWeekly float64
	Triggered bool
	Stale     bool
	Future    bool
	Valid     bool
}

// CheckAutoNextThreshold evaluates whether an account's quota has dropped strictly below the
// auto-next thresholds (5h < 0.20 OR weekly < 0.15).
//
// Rules:
//   - Min over ALL matching buckets across all quota groups.
//   - Strict OR condition: (has5h && min5h < 0.20) || (hasWeekly && minWeekly < 0.15).
//   - Equality does NOT trigger (0.20 or 0.15 does not trigger).
//   - Missing windows are NOT treated as zero and cannot trigger.
//   - Unusable fractions (NaN, Inf, < 0.0, or > 1.0) are ignored.
//   - Unknown data (nil account, nil snapshot, malformed timestamps/groups) fails closed (returns false).
//   - Stale observations (> 2 minutes) or observations in the future are rejected (returns false).
func CheckAutoNextThreshold(account Account, now time.Time) (bool, AutoNextThresholdResult) {
	var res AutoNextThresholdResult
	if account == nil {
		return false, res
	}
	snapshot := getMap(account["quota_snapshot"])
	if snapshot == nil {
		return false, res
	}
	observedStr := getString(snapshot, "observed_at")
	if observedStr == "" {
		return false, res
	}
	observed, err := parseUTC(observedStr)
	if err != nil {
		return false, res
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	// Reject future observations
	if observed.After(now) {
		res.Future = true
		return false, res
	}
	// Reject stale observations (> 2 minutes)
	if now.Sub(observed) > AutoNextMaxQuotaAge {
		res.Stale = true
		return false, res
	}

	groups := getSlice(snapshot["groups"])
	if len(groups) == 0 {
		return false, res
	}

	for _, rawGroup := range groups {
		group := getMap(rawGroup)
		if group == nil {
			continue
		}
		buckets := getSlice(group["buckets"])
		for _, rawBucket := range buckets {
			bucket := getMap(rawBucket)
			if bucket == nil {
				continue
			}
			window := strings.ToLower(strings.TrimSpace(getString(bucket, "window")))
			fraction, ok := getFloat(bucket["remaining_fraction"])
			if !ok || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0.0 || fraction > 1.0 {
				continue
			}
			switch window {
			case "5h":
				if !res.Has5h || fraction < res.Min5h {
					res.Min5h = fraction
					res.Has5h = true
				}
			case "weekly":
				if !res.HasWeekly || fraction < res.MinWeekly {
					res.MinWeekly = fraction
					res.HasWeekly = true
				}
			}
		}
	}

	if !res.Has5h && !res.HasWeekly {
		// Unknown data fail closed
		return false, res
	}
	res.Valid = true

	if (res.Has5h && res.Min5h < AutoNext5hThreshold) || (res.HasWeekly && res.MinWeekly < AutoNextWeeklyThreshold) {
		res.Triggered = true
		return true, res
	}
	return false, res
}

// ShouldAutoNext returns true if the account is eligible for auto-next rotation due to low quota.
func ShouldAutoNext(account Account, now time.Time) bool {
	triggered, _ := CheckAutoNextThreshold(account, now)
	return triggered
}

// isSnapshotFresh checks if an account has a valid, non-stale, non-future quota observation.
func isSnapshotFresh(account Account, now time.Time) bool {
	if account == nil {
		return false
	}
	snapshot := getMap(account["quota_snapshot"])
	if snapshot == nil {
		return false
	}
	observedStr := getString(snapshot, "observed_at")
	if observedStr == "" {
		return false
	}
	observed, err := parseUTC(observedStr)
	if err != nil {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if observed.After(now) {
		return false
	}
	if now.Sub(observed) > AutoNextMaxQuotaAge {
		return false
	}
	return true
}

// hasQuotaError checks if the given email has a recorded refresh or auth error.
func hasQuotaError(quotaErrors map[string]string, email string) bool {
	if quotaErrors == nil {
		return false
	}
	if err, ok := quotaErrors[email]; ok && strings.TrimSpace(err) != "" {
		return true
	}
	for k, v := range quotaErrors {
		if strings.EqualFold(k, email) && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// AutoNextCandidateOptions specifies optional filters for candidate selection.
type AutoNextCandidateOptions struct {
	Profile string
	Family  string
	Tag     string
}

// autoNextAccountRemaining calculates the limiting remaining percentage (0-100)
// considering only finite, in-range [0.0, 1.0] fractions in known quota windows ("5h" and "weekly").
// Any malformed fractions (NaN, Inf, < 0, > 1) or non-5h/weekly windows are ignored and not clamped.
func autoNextAccountRemaining(account Account, family string) (float64, bool) {
	if account == nil {
		return 0, false
	}
	snapshot := getMap(account["quota_snapshot"])
	if snapshot == nil {
		return 0, false
	}
	groups := getSlice(snapshot["groups"])
	if len(groups) == 0 {
		return 0, false
	}

	found := false
	minFraction := 1.0

	for _, rawGroup := range groups {
		group := getMap(rawGroup)
		if group == nil {
			continue
		}
		groupId := strings.ToLower(cleanText(getString(group, "id")))
		if !quotaFamilyMatches(groupId, family) {
			continue
		}
		buckets := getSlice(group["buckets"])
		for _, rawBucket := range buckets {
			bucket := getMap(rawBucket)
			if bucket == nil {
				continue
			}
			window := strings.ToLower(strings.TrimSpace(getString(bucket, "window")))
			if window != "5h" && window != "weekly" {
				continue
			}
			fraction, ok := getFloat(bucket["remaining_fraction"])
			if !ok || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0.0 || fraction > 1.0 {
				continue
			}
			if !found || fraction < minFraction {
				minFraction = fraction
				found = true
			}
		}
	}

	if !found {
		return 0, false
	}
	return minFraction * 100, true
}

// SelectAutoNextCandidate selects the best eligible candidate account to auto-switch to.
//
// Candidate selection rules:
//   - Existing next policy: if policy is "sticky", it is treated as "round-robin".
//   - Preserves configured minRemaining and family eligibility.
//   - Excludes active account.
//   - Excludes accounts with refresh errors or auth errors.
//   - Excludes accounts in cooldown.
//   - Excludes accounts with stale (>2m) or future snapshots.
//   - Excludes accounts below either threshold (5h < 0.20 OR weekly < 0.15).
//   - Zero credential or network IO on the UI thread.
//   - Returns (account, true) if an eligible candidate is found; (nil, false) otherwise (no loops).
func SelectAutoNextCandidate(
	accounts *Accounts,
	activeEmail string,
	settings AppSettings,
	quotaErrors map[string]string,
	now time.Time,
) (Account, bool) {
	return SelectAutoNextCandidateWithOptions(accounts, activeEmail, settings, quotaErrors, now, AutoNextCandidateOptions{})
}

// SelectAutoNextCandidateWithOptions selects the best eligible candidate account with specific options.
func SelectAutoNextCandidateWithOptions(
	accounts *Accounts,
	activeEmail string,
	settings AppSettings,
	quotaErrors map[string]string,
	now time.Time,
	opts AutoNextCandidateOptions,
) (Account, bool) {
	if accounts == nil || accounts.Len() == 0 {
		return nil, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	policy := settings.Policy.Name
	profile, hasProfile := settings.Profiles[opts.Profile]
	family := opts.Family
	if hasProfile {
		if family == "" {
			family = profile.Family
		}
		if profile.Policy != "" {
			policy = profile.Policy
		}
	}
	if family == "" {
		family = settings.Policy.PreferFamily
	}
	// Rotation advance: sticky becomes round-robin
	if policy == "sticky" {
		policy = "round-robin"
	}

	activeIndex := -1
	for i, email := range accounts.Order {
		if activeEmail != "" && strings.EqualFold(email, activeEmail) {
			activeIndex = i
			break
		}
	}

	type candidateItem struct {
		account   Account
		email     string
		score     int
		isReserve bool
	}

	var candidates []candidateItem
	for index, email := range accounts.Order {
		// Exclude active
		if activeEmail != "" && strings.EqualFold(email, activeEmail) {
			continue
		}
		// Tag filter
		if opts.Tag != "" && !containsStringValue(settings.Tags[email], opts.Tag) {
			continue
		}
		// Reserve profile filter
		if hasProfile && len(profile.ReserveAccounts) > 0 && email != profile.Account && !containsStringValue(profile.ReserveAccounts, email) {
			continue
		}
		// Exclude refresh errors and auth errors
		if hasQuotaError(quotaErrors, email) {
			continue
		}
		account := accounts.ByEmail[email]
		if account == nil {
			continue
		}
		// Exclude stale or future snapshots
		if !isSnapshotFresh(account, now) {
			continue
		}
		// Require evaluated result.Valid == true and !triggered.
		// Rejects candidate accounts with invalid-only fractions (1.5, NaN, Inf) or unknown-window snapshots.
		triggered, threshRes := CheckAutoNextThreshold(account, now)
		if !threshRes.Valid || triggered {
			continue
		}
		// Preserving configured minRemaining / family eligibility using finite in-range usable known-window quota.
		// Malformed fractions and non-5h/weekly windows are ignored and never clamped.
		remaining, known := autoNextAccountRemaining(account, family)
		if !known || remaining <= 0 {
			continue
		}
		if remaining < float64(settings.Policy.MinRemainingPct) {
			continue
		}
		// Exclude cooldown
		wait, waitKnown := accountCooldown(account, now, family)
		if waitKnown && wait > 0 {
			continue
		}

		score := int(remaining)
		switch policy {
		case "round-robin":
			score = accounts.Len() - (index-activeIndex-1+accounts.Len())%accounts.Len()
		case "balanced":
			score = int(remaining)
		}

		isReserve := false
		if hasProfile && containsStringValue(profile.ReserveAccounts, email) {
			isReserve = true
		}

		candidates = append(candidates, candidateItem{
			account:   account,
			email:     email,
			score:     score,
			isReserve: isReserve,
		})
	}

	if len(candidates) == 0 {
		return nil, false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if hasProfile {
			if candidates[i].isReserve != candidates[j].isReserve {
				return !candidates[i].isReserve
			}
		}
		return candidates[i].score > candidates[j].score
	})

	return candidates[0].account, true
}

// SelectAutoNextCandidateEmail returns the email of the selected candidate account, if any.
func SelectAutoNextCandidateEmail(
	accounts *Accounts,
	activeEmail string,
	settings AppSettings,
	quotaErrors map[string]string,
	now time.Time,
) (string, bool) {
	cand, ok := SelectAutoNextCandidate(accounts, activeEmail, settings, quotaErrors, now)
	if !ok {
		return "", false
	}
	return getString(cand, "email"), true
}
