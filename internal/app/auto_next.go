package app

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// AutoNext5hThreshold is the strict upper bound for the 5-hour rolling quota window (25%).
	AutoNext5hThreshold = 0.25

	// AutoNextWeeklyThreshold is the strict upper bound for the weekly quota window (15%).
	AutoNextWeeklyThreshold = 0.15

	// AutoNextMaxQuotaAge is the maximum allowed age of a quota snapshot before it is considered stale.
	AutoNextMaxQuotaAge = 15 * time.Minute

	// Bounded reset bonuses favor expiring quota without overpowering capacity.
	autoNextWeeklyResetBonus = 30.0
	autoNext5hResetBonus     = 20.0

	// High quotas offer comparable capacity for this workload: favor their resets.
	autoNextFullQuotaFraction = 0.90

	// Workload estimate: a full 5h bucket consumes about 20% of weekly quota.
	// Only pair windows within the same model group.
	autoNextWeeklyPer5hFraction = 0.20
)

// autoNextThresholdLimits returns threshold fractions (0.0 - 1.0) configured in settings.
func autoNextThresholdLimits(settings AppSettings) (weekly, fiveHour float64) {
	weekly = AutoNextWeeklyThreshold
	fiveHour = AutoNext5hThreshold
	if settings.UI.AutoNextWeeklyThreshold > 0 {
		weekly = float64(settings.UI.AutoNextWeeklyThreshold) / 100.0
	}
	if settings.UI.AutoNext5hThreshold > 0 {
		fiveHour = float64(settings.UI.AutoNext5hThreshold) / 100.0
	}
	return weekly, fiveHour
}

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
// default auto-next thresholds (5h < 0.25 OR weekly < 0.15).
func CheckAutoNextThreshold(account Account, now time.Time) (bool, AutoNextThresholdResult) {
	return CheckAutoNextThresholdWithLimits(account, now, AutoNextWeeklyThreshold, AutoNext5hThreshold)
}

// CheckAutoNextThresholdWithLimits evaluates whether an account's quota has dropped strictly below the
// configured auto-next thresholds.
//
// Rules:
//   - Min over ALL matching buckets across all quota groups.
//   - Strict OR condition: (has5h && min5h < fiveHourThreshold) || (hasWeekly && minWeekly < weeklyThreshold).
//   - Equality does NOT trigger.
//   - Missing windows are NOT treated as zero and cannot trigger.
//   - Unusable fractions (NaN, Inf, < 0.0, or > 1.0) are ignored.
//   - Unknown data (nil account, nil snapshot, malformed timestamps/groups) fails closed (returns false).
//   - Stale observations (> AutoNextMaxQuotaAge) or observations in the future are rejected (returns false).
func CheckAutoNextThresholdWithLimits(account Account, now time.Time, weeklyThreshold, fiveHourThreshold float64) (bool, AutoNextThresholdResult) {
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
	// Reject stale observations (> 15 minutes)
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

	if (res.Has5h && res.Min5h < fiveHourThreshold) || (res.HasWeekly && res.MinWeekly < weeklyThreshold) {
		res.Triggered = true
		return true, res
	}
	return false, res
}

// ShouldAutoNext returns true if the account is eligible for auto-next rotation due to low quota with defaults.
func ShouldAutoNext(account Account, now time.Time) bool {
	triggered, _ := CheckAutoNextThreshold(account, now)
	return triggered
}

// ShouldAutoNextWithSettings returns true if the account is eligible for auto-next rotation with configured settings.
func ShouldAutoNextWithSettings(account Account, settings AppSettings, now time.Time) bool {
	weekly, fiveHour := autoNextThresholdLimits(settings)
	triggered, _ := CheckAutoNextThresholdWithLimits(account, now, weekly, fiveHour)
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
	Profile        string
	Family         string
	Tag            string
	ExcludedEmails []string
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

// autoNextCandidateScore balances current capacity against upcoming resets.
// For groups with both windows, weekly capacity above its reserve is converted
// to 5h units before applying the limiting capacity and headroom. Ample weekly
// quota cannot penalize an otherwise equivalent 5h window just for being lower.
// Unpaired windows retain their native capacity. Each window uses the least
// favorable reset across matching buckets; missing or elapsed resets add nothing.
// Capacity and headroom saturate at 90% of the reference window. Thresholds at
// or above 90% retain headroom up to 100% for safe boundaries.
// This is a ranking heuristic, not a prediction of the task's consumption rate.
func autoNextCandidateScore(account Account, family string, weeklyLimit, fiveHourLimit float64, now time.Time) float64 {
	capacity := 1.0
	headroom := 1.0
	weeklyProximity, fiveHourProximity := 1.0, 1.0
	hasWeekly, hasFiveHour := false, false
	for _, rawGroup := range getSlice(getMap(account["quota_snapshot"])["groups"]) {
		group := getMap(rawGroup)
		if !quotaFamilyMatches(strings.ToLower(cleanText(getString(group, "id"))), family) {
			continue
		}
		groupWeekly, groupFiveHour := 1.0, 1.0
		groupHasWeekly, groupHasFiveHour := false, false
		for _, rawBucket := range getSlice(group["buckets"]) {
			bucket := getMap(rawBucket)
			window := strings.ToLower(strings.TrimSpace(getString(bucket, "window")))
			fraction, ok := getFloat(bucket["remaining_fraction"])
			if !ok || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0 || fraction > 1 {
				continue
			}
			var duration time.Duration
			switch window {
			case "weekly":
				duration = 7 * 24 * time.Hour
				groupWeekly = min(groupWeekly, fraction)
				groupHasWeekly = true
			case "5h":
				duration = 5 * time.Hour
				groupFiveHour = min(groupFiveHour, fraction)
				groupHasFiveHour = true
			default:
				continue
			}
			proximity := 0.0
			if reset, err := parseUTC(getString(bucket, "reset_at")); err == nil && reset.After(now) {
				proximity = max(0, 1-float64(reset.Sub(now))/float64(duration))
			}
			if window == "weekly" {
				hasWeekly = true
				weeklyProximity = min(weeklyProximity, proximity)
			} else {
				hasFiveHour = true
				fiveHourProximity = min(fiveHourProximity, proximity)
			}
		}
		if !groupHasWeekly && !groupHasFiveHour {
			continue
		}
		groupCapacity, reserve := groupWeekly, weeklyLimit
		spendable := max(0, groupWeekly-weeklyLimit)
		if groupHasFiveHour {
			groupCapacity, reserve = groupFiveHour, fiveHourLimit
			spendable = max(0, groupFiveHour-fiveHourLimit)
			if groupHasWeekly {
				weeklyBudget := max(0, groupWeekly-weeklyLimit) / autoNextWeeklyPer5hFraction
				spendable = min(spendable, weeklyBudget)
				groupCapacity = min(groupCapacity, reserve+spendable)
			}
		}
		capacity = min(capacity, groupCapacity)
		fullQuota := autoNextFullQuotaFraction
		if reserve >= fullQuota {
			fullQuota = 1
		}
		usable := 0.0
		if reserve < fullQuota {
			usable = min(1, spendable/(fullQuota-reserve))
		}
		headroom = min(headroom, usable)
	}
	if !hasWeekly && !hasFiveHour {
		return 0
	}
	if !hasWeekly {
		weeklyProximity = 0
	}
	if !hasFiveHour {
		fiveHourProximity = 0
	}
	return min(capacity, autoNextFullQuotaFraction)*100 + headroom*(autoNextWeeklyResetBonus*weeklyProximity+autoNext5hResetBonus*fiveHourProximity)
}

// SelectAutoNextCandidate selects the best eligible candidate account to auto-switch to.
//
// Candidate selection rules:
//   - Sticky/balanced/default rank by quota headroom and upcoming weekly/5h resets.
//   - Explicit round-robin preserves circular rotation; score ties also use that order.
//   - Preserves configured minRemaining and family eligibility.
//   - Excludes active account.
//   - Excludes accounts with refresh errors or auth errors.
//   - Excludes accounts in cooldown.
//   - Excludes accounts with stale (>15m) or future snapshots.
//   - Excludes accounts below either configured threshold (defaults: 5h < 25%, weekly < 15%).
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
	candidates := rankAutoNextCandidates(accounts, activeEmail, settings, quotaErrors, now, opts)
	if len(candidates) == 0 {
		return nil, false
	}
	return candidates[0].account, true
}

type autoNextCandidate struct {
	account   Account
	email     string
	score     float64
	rotation  int
	isReserve bool
}

// rankAutoNextCandidates is the shared, IO-free ranking for rotation and UI
// preview. It ranks the full account set independently of the UI search filter.
func rankAutoNextCandidates(
	accounts *Accounts,
	activeEmail string,
	settings AppSettings,
	quotaErrors map[string]string,
	now time.Time,
	opts AutoNextCandidateOptions,
) []autoNextCandidate {
	if accounts == nil || accounts.Len() == 0 {
		return nil
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
	activeIndex := -1
	for i, email := range accounts.Order {
		if activeEmail != "" && strings.EqualFold(email, activeEmail) {
			activeIndex = i
			break
		}
	}

	weeklyLimit, fiveHourLimit := autoNextThresholdLimits(settings)
	var candidates []autoNextCandidate
	for index, email := range accounts.Order {
		// Exclude active
		if activeEmail != "" && strings.EqualFold(email, activeEmail) {
			continue
		}
		// Exclude attempted
		if len(opts.ExcludedEmails) > 0 && containsStringValue(opts.ExcludedEmails, email) {
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
		// Require evaluated result.Valid == true and !triggered using configured thresholds.
		// Rejects candidate accounts with invalid-only fractions (1.5, NaN, Inf) or unknown-window snapshots.
		triggered, threshRes := CheckAutoNextThresholdWithLimits(account, now, weeklyLimit, fiveHourLimit)
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

		rotation := accounts.Len() - (index-activeIndex-1+accounts.Len())%accounts.Len()
		score := autoNextCandidateScore(account, family, weeklyLimit, fiveHourLimit, now)
		if policy == "round-robin" {
			score = float64(rotation)
		}

		isReserve := false
		if hasProfile && containsStringValue(profile.ReserveAccounts, email) {
			isReserve = true
		}

		candidates = append(candidates, autoNextCandidate{
			account:   account,
			email:     email,
			score:     score,
			rotation:  rotation,
			isReserve: isReserve,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if hasProfile {
			if candidates[i].isReserve != candidates[j].isReserve {
				return !candidates[i].isReserve
			}
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].rotation > candidates[j].rotation
	})

	return candidates
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
