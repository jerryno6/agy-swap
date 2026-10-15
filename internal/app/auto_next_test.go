package app

import (
	"math"
	"testing"
	"time"
)

func autoNextQuotaAccount(email string, now time.Time, weekly, fiveHour float64, weeklyReset, fiveHourReset time.Duration) Account {
	return Account{"email": email, "quota_snapshot": map[string]any{
		"observed_at": isoTime(now),
		"groups": []any{map[string]any{"id": "gemini", "buckets": []any{
			map[string]any{"window": "weekly", "remaining_fraction": weekly, "reset_at": isoTime(now.Add(weeklyReset))},
			map[string]any{"window": "5h", "remaining_fraction": fiveHour, "reset_at": isoTime(now.Add(fiveHourReset))},
		}}},
	}}
}

func autoNextTestBuckets(account Account) []any {
	return getSlice(getMap(getSlice(getMap(account["quota_snapshot"])["groups"])[0])["buckets"])
}

func TestAutoNextResetPriorities(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name                             string
		firstWeekly, firstFiveHour       float64
		secondWeekly, secondFiveHour     float64
		firstWeeklyReset, first5hReset   time.Duration
		secondWeeklyReset, second5hReset time.Duration
		want                             string
	}{
		{"weekly_3h_before_5_days", 1, 1, 1, 1, 5 * 24 * time.Hour, 5 * time.Hour, 3 * time.Hour, 5 * time.Hour, "second"},
		{"5h_1h_before_5h", 1, 1, 1, 1, 5 * 24 * time.Hour, 5 * time.Hour, 5 * 24 * time.Hour, time.Hour, "second"},
		{"weekly_priority_over_5h_bonus", 1, 1, 1, 1, 5 * 24 * time.Hour, time.Hour, 3 * time.Hour, 5 * time.Hour, "second"},
		{"both_resets_before_only_weekly", 1, 1, 1, 1, 3 * time.Hour, 5 * time.Hour, 3 * time.Hour, time.Hour, "second"},
		{"use_expiring_weekly_with_80_percent_session_capacity", .16, 1, 1, 1, time.Minute, time.Minute, 5 * 24 * time.Hour, 5 * time.Hour, "first"},
		{"healthy_before_near_depleted_5h", 1, .26, 1, 1, time.Minute, time.Minute, 5 * 24 * time.Hour, 5 * time.Hour, "second"},
		{"quota_priority_when_resets_match", .8, .8, 1, 1, time.Hour, time.Hour, time.Hour, time.Hour, "second"},
		{"fractional_quota_precision_below_full_band", .8001, .8001, .8009, .8009, 7 * 24 * time.Hour, 5 * time.Hour, 7 * 24 * time.Hour, 5 * time.Hour, "second"},
		{"use_expiring_healthy_quota", .9, .9, 1, 1, time.Hour, time.Hour, 7 * 24 * time.Hour, 5 * time.Hour, "first"},
	}
	for _, policy := range []string{"sticky", "balanced", ""} {
		for _, tc := range cases {
			t.Run(policy+"/"+tc.name, func(t *testing.T) {
				accounts := NewAccounts()
				accounts.Set("active", autoNextQuotaAccount("active", now, .1, .1, time.Hour, time.Hour))
				accounts.Set("first", autoNextQuotaAccount("first", now, tc.firstWeekly, tc.firstFiveHour, tc.firstWeeklyReset, tc.first5hReset))
				accounts.Set("second", autoNextQuotaAccount("second", now, tc.secondWeekly, tc.secondFiveHour, tc.secondWeeklyReset, tc.second5hReset))
				settings := defaultSettings()
				settings.Policy.Name = policy
				got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
				if !ok || got != tc.want {
					t.Fatalf("selected %q (%v), want %q", got, ok, tc.want)
				}
			})
		}
	}
}

func TestAutoNextTenAccounts(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	accounts := NewAccounts()
	// Reported ten-account Gemini snapshot, with Trang Linh active.
	for _, item := range []struct {
		email                      string
		weekly, fiveHour           float64
		weeklyReset, fiveHourReset time.Duration
	}{
		{"momizy-gaming", .2109, 1, 30*time.Hour + 23*time.Minute, 4*time.Hour + 58*time.Minute},
		{"le-dang-vu", 1, 1, 166*time.Hour + 6*time.Minute, 4*time.Hour + 58*time.Minute},
		{"trang-linh", .6050, 1, 99*time.Hour + 50*time.Minute, 4*time.Hour + 58*time.Minute},
		{"lan-nguyen", .2680, 1, 75*time.Hour + 43*time.Minute, 4*time.Hour + 22*time.Minute},
		{"david-martin", .2161, 1, 33*time.Hour + 47*time.Minute, 4*time.Hour + 58*time.Minute},
		{"vu-dang22", .5890, .2042, 126*time.Hour + 32*time.Minute, 2*time.Hour + 28*time.Minute},
		{"vu-le21", .4011, .2120, 78*time.Hour + 33*time.Minute, 3*time.Hour + 3*time.Minute},
		{"vu-le20", .9596, .8685, 157*time.Hour + 22*time.Minute, 4*time.Hour + 11*time.Minute},
		{"vu-le14", 0, 1, 9*time.Hour + 23*time.Minute, 4*time.Hour + 58*time.Minute},
		{"momizy-playing", 1, 1, 167*time.Hour + 58*time.Minute, 4*time.Hour + 58*time.Minute},
	} {
		accounts.Set(item.email, autoNextQuotaAccount(item.email, now, item.weekly, item.fiveHour, item.weeklyReset, item.fiveHourReset))
	}
	settings := defaultSettings()
	settings.UI.AutoNextWeeklyThreshold, settings.UI.AutoNext5hThreshold = 15, 40
	got, ok := SelectAutoNextCandidateEmail(accounts, "trang-linh", settings, nil, now)
	if !ok || got != "momizy-gaming" {
		t.Fatalf("selected %q (%v), want momizy-gaming", got, ok)
	}
	state := &tuiState{accounts: accounts, active: "trang-linh", settings: settings, settingsLoaded: true}
	ranks := tuiAutoNextRanks(state, now)
	if ranks[got] != 1 || ranks["david-martin"] != 2 || ranks["lan-nguyen"] != 3 {
		t.Fatalf("TUI priority does not match expiry ranking: %#v", ranks)
	}
}

func TestAutoNextCandidateSafety(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(Account, *AppSettings, map[string]string, *AutoNextCandidateOptions)
	}{
		{"weekly_below_threshold", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			getMap(autoNextTestBuckets(a)[0])["remaining_fraction"] = .149999
		}},
		{"5h_below_threshold", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			getMap(autoNextTestBuckets(a)[1])["remaining_fraction"] = .249999
		}},
		{"stale", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			getMap(a["quota_snapshot"])["observed_at"] = isoTime(now.Add(-AutoNextMaxQuotaAge - time.Second))
		}},
		{"future", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			getMap(a["quota_snapshot"])["observed_at"] = isoTime(now.Add(time.Second))
		}},
		{"missing_observation", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			delete(getMap(a["quota_snapshot"]), "observed_at")
		}},
		{"auth_error_case_insensitive", func(_ Account, _ *AppSettings, e map[string]string, _ *AutoNextCandidateOptions) {
			e["FIRST"] = "auth failed"
		}},
		{"excluded_fallback", func(_ Account, _ *AppSettings, _ map[string]string, o *AutoNextCandidateOptions) {
			o.ExcludedEmails = []string{"first"}
		}},
		{"manual_cooldown", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			a["quota_limits"] = map[string]any{"manual:gemini": map[string]any{"family": "gemini", "source": "manual", "reset_at": isoTime(now.Add(time.Hour)), "observed_at": isoTime(now)}}
		}},
		{"minimum_remaining", func(a Account, s *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			s.Policy.MinRemainingPct = 90
			getMap(autoNextTestBuckets(a)[0])["remaining_fraction"] = .8
		}},
		{"tag_filter", func(_ Account, s *AppSettings, _ map[string]string, o *AutoNextCandidateOptions) {
			s.Tags["second"] = []string{"work"}
			o.Tag = "work"
		}},
		{"missing_gemini", func(a Account, _ *AppSettings, _ map[string]string, _ *AutoNextCandidateOptions) {
			getMap(getSlice(getMap(a["quota_snapshot"])["groups"])[0])["id"] = "third_party"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accounts := NewAccounts()
			accounts.Set("first", autoNextQuotaAccount("first", now, 1, 1, time.Minute, time.Minute))
			accounts.Set("second", autoNextQuotaAccount("second", now, 1, 1, 7*24*time.Hour, 5*time.Hour))
			settings := defaultSettings()
			errors := map[string]string{}
			opts := AutoNextCandidateOptions{}
			tc.mutate(accounts.ByEmail["first"], &settings, errors, &opts)
			got, ok := SelectAutoNextCandidateWithOptions(accounts, "active", settings, errors, now, opts)
			if !ok || getString(got, "email") != "second" {
				t.Fatalf("selected %q (%v), want second", getString(got, "email"), ok)
			}
		})
	}
	for _, fraction := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1.1} {
		a := autoNextQuotaAccount("invalid", now, fraction, fraction, time.Minute, time.Minute)
		accounts := NewAccounts()
		accounts.Set("invalid", a)
		if _, ok := SelectAutoNextCandidate(accounts, "active", defaultSettings(), nil, now); ok {
			t.Fatalf("selected invalid-only quota %v", fraction)
		}
	}
}

func TestAutoNextInvalidResetDoesNotCreateBonus(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, reset := range []string{"", "invalid", isoTime(now), isoTime(now.Add(-time.Second))} {
		t.Run(reset, func(t *testing.T) {
			accounts := NewAccounts()
			first := autoNextQuotaAccount("first", now, 1, 1, time.Minute, time.Minute)
			for _, raw := range autoNextTestBuckets(first) {
				getMap(raw)["reset_at"] = reset
			}
			accounts.Set("first", first)
			accounts.Set("second", autoNextQuotaAccount("second", now, 1, 1, 3*time.Hour, time.Hour))
			got, ok := SelectAutoNextCandidateEmail(accounts, "active", defaultSettings(), nil, now)
			if !ok || got != "second" {
				t.Fatalf("selected %q (%v), want second", got, ok)
			}
		})
	}
}

func TestAutoNextPolicyAndTies(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	accounts := NewAccounts()
	for _, email := range []string{"before", "active", "after"} {
		accounts.Set(email, autoNextQuotaAccount(email, now, 1, 1, 7*24*time.Hour, 5*time.Hour))
	}
	for _, policy := range []string{"sticky", "balanced", "round-robin"} {
		settings := defaultSettings()
		settings.Policy.Name = policy
		got, ok := SelectAutoNextCandidateEmail(accounts, "ACTIVE", settings, nil, now)
		if !ok || got != "after" {
			t.Fatalf("%s selected %q (%v), want after", policy, got, ok)
		}
	}
	settings := defaultSettings()
	settings.Policy.Name = "round-robin"
	for _, raw := range autoNextTestBuckets(accounts.ByEmail["before"]) {
		getMap(raw)["reset_at"] = isoTime(now.Add(time.Minute))
	}
	got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
	if !ok || got != "after" {
		t.Fatalf("explicit round-robin selected %q (%v), want after", got, ok)
	}
	settings.UI.AutoNextWeeklyThreshold, settings.UI.AutoNext5hThreshold = 100, 100
	settings.Policy.Name = "balanced"
	got, ok = SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
	if !ok || got != "after" {
		t.Fatalf("100%% thresholds selected %q (%v), want after", got, ok)
	}
}

func TestAutoNextFamilyAndGroupResetAggregation(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	accounts := NewAccounts()
	first := autoNextQuotaAccount("first", now, 1, 1, time.Minute, time.Hour)
	thirdParty := map[string]any{"id": "third_party", "buckets": []any{
		map[string]any{"window": "weekly", "remaining_fraction": 1.0, "reset_at": isoTime(now.Add(7 * 24 * time.Hour))},
	}}
	snapshot := getMap(first["quota_snapshot"])
	snapshot["groups"] = append(getSlice(snapshot["groups"]), thirdParty)
	accounts.Set("first", first)
	accounts.Set("second", autoNextQuotaAccount("second", now, 1, 1, 3*time.Hour, time.Hour))
	settings := defaultSettings()
	settings.Policy.PreferFamily = "claude"
	for _, tc := range []struct{ family, want string }{{"", "first"}, {"gemini", "first"}, {"claude", "first"}, {"gpt", "first"}} {
		got, ok := SelectAutoNextCandidateWithOptions(accounts, "active", settings, nil, now, AutoNextCandidateOptions{Family: tc.family})
		if !ok || getString(got, "email") != tc.want {
			t.Fatalf("family %q selected %q (%v), want %s", tc.family, getString(got, "email"), ok, tc.want)
		}
	}
	// Exhausted third-party quota must not trigger rotation or reject Gemini.
	thirdPartyBucket := getMap(getSlice(thirdParty["buckets"])[0])
	thirdPartyBucket["remaining_fraction"] = 0.0
	if triggered, result := CheckAutoNextThreshold(first, now); triggered || !result.Valid {
		t.Fatalf("third-party quota affected Gemini trigger: %+v", result)
	}
	if got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now); !ok || got != "first" {
		t.Fatalf("exhausted third-party quota selected %q (%v), want first", got, ok)
	}
	thirdPartyBucket["remaining_fraction"] = 1.0
	// A duplicate bucket with a later reset must also limit the bonus.
	group := getMap(getSlice(snapshot["groups"])[0])
	group["buckets"] = append(getSlice(group["buckets"]), getSlice(thirdParty["buckets"])[0])
	got, ok := SelectAutoNextCandidateWithOptions(accounts, "active", settings, nil, now, AutoNextCandidateOptions{Family: "gemini"})
	if !ok || getString(got, "email") != "second" {
		t.Fatalf("duplicate reset selected %q (%v), want second", getString(got, "email"), ok)
	}
}

func TestAutoNextMissingWindowsAndMalformedBuckets(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, window := range []string{"weekly", "5h"} {
		t.Run(window, func(t *testing.T) {
			accounts := NewAccounts()
			for _, email := range []string{"first", "second"} {
				a := autoNextQuotaAccount(email, now, 1, 1, 7*24*time.Hour, 5*time.Hour)
				bucket := autoNextTestBuckets(a)[0]
				if window == "5h" {
					bucket = autoNextTestBuckets(a)[1]
				}
				if email == "second" {
					getMap(bucket)["reset_at"] = isoTime(now.Add(time.Hour))
				}
				group := getMap(getSlice(getMap(a["quota_snapshot"])["groups"])[0])
				group["buckets"] = []any{bucket,
					map[string]any{"window": "unknown", "remaining_fraction": 0.0, "reset_at": isoTime(now.Add(-time.Hour))},
					map[string]any{"window": window, "remaining_fraction": 1.1, "reset_at": isoTime(now.Add(time.Minute))},
				}
				accounts.Set(email, a)
			}
			got, ok := SelectAutoNextCandidateEmail(accounts, "active", defaultSettings(), nil, now)
			if !ok || got != "second" {
				t.Fatalf("partial snapshot selected %q (%v), want second", got, ok)
			}
		})
	}
}

func TestAutoNextProfilesAndFallback(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	accounts := NewAccounts()
	accounts.Set("unlisted", autoNextQuotaAccount("unlisted", now, 1, 1, time.Minute, time.Minute))
	accounts.Set("reserve-far", autoNextQuotaAccount("reserve-far", now, 1, 1, 7*24*time.Hour, 5*time.Hour))
	accounts.Set("reserve-near", autoNextQuotaAccount("reserve-near", now, 1, 1, 3*time.Hour, time.Hour))
	accounts.Set("primary", autoNextQuotaAccount("primary", now, 1, 1, 7*24*time.Hour, 5*time.Hour))
	settings := defaultSettings()
	settings.Profiles["work"] = Profile{Account: "primary", Family: "gemini", Policy: "balanced", ReserveAccounts: []string{"reserve-far", "reserve-near"}}
	opts := AutoNextCandidateOptions{Profile: "work"}
	for _, want := range []string{"primary", "reserve-near", "reserve-far"} {
		got, ok := SelectAutoNextCandidateWithOptions(accounts, "active", settings, nil, now, opts)
		if !ok || getString(got, "email") != want {
			t.Fatalf("selected %q (%v), want %s", getString(got, "email"), ok, want)
		}
		opts.ExcludedEmails = append(opts.ExcludedEmails, want)
	}
	if _, ok := SelectAutoNextCandidateWithOptions(accounts, "active", settings, nil, now, opts); ok {
		t.Fatal("selected an exhausted/unlisted fallback")
	}
}

func TestAutoNextConfiguredThresholdHeadroom(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	accounts := NewAccounts()
	accounts.Set("near-threshold", autoNextQuotaAccount("near-threshold", now, .8, .8, time.Minute, time.Minute))
	accounts.Set("healthy", autoNextQuotaAccount("healthy", now, .9, .9, 7*24*time.Hour, 5*time.Hour))
	settings := defaultSettings()
	settings.UI.AutoNextWeeklyThreshold = 80
	settings.UI.AutoNext5hThreshold = 80
	got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
	if !ok || got != "healthy" {
		t.Fatalf("configured headroom selected %q (%v), want healthy", got, ok)
	}
	delete(accounts.ByEmail, "healthy")
	got, ok = SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
	if !ok || got != "near-threshold" {
		t.Fatalf("equal threshold should remain eligible, got %q (%v)", got, ok)
	}
	if ShouldAutoNextWithSettings(accounts.ByEmail["near-threshold"], settings, now) {
		t.Fatal("equal threshold triggered rotation")
	}
	getMap(autoNextTestBuckets(accounts.ByEmail["near-threshold"])[0])["remaining_fraction"] = .799999
	if !ShouldAutoNextWithSettings(accounts.ByEmail["near-threshold"], settings, now) {
		t.Fatal("below configured threshold did not trigger")
	}
}

func TestAutoNextHighQuotaResetPriority(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name                            string
		nearWeekly, nearFiveHour        float64
		farWeeklyReset, nearWeeklyReset time.Duration
		far5hReset, near5hReset         time.Duration
		want                            string
	}{
		{"90_percent_5h_before_full", 1, .9, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 3 * time.Hour, "near"},
		{"90_percent_weekly_does_not_dilute_5h_bonus", .9, 1, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 3 * time.Hour, "near"},
		{"both_90_percent_before_full", .9, .9, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 3 * time.Hour, "near"},
		{"user_example_90_percent_5h_reset_in_1h", .95, .9, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, time.Hour, "near"},
		{"95_percent_before_full", .95, .95, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 3 * time.Hour, "near"},
		{"one_minute_reset_advantage", .9, .9, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 4*time.Hour - time.Minute, "near"},
		{"weekly_90_percent_reset_advantage", .9, .9, 3 * 24 * time.Hour, 3*24*time.Hour - time.Minute, 4 * time.Hour, 4 * time.Hour, "near"},
		{"same_reset_90_percent_ties_full", .9, .9, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 4 * time.Hour, "near"},
		{"below_90_percent_retains_capacity_penalty", .89, .89, 3 * 24 * time.Hour, 3 * 24 * time.Hour, 4 * time.Hour, 4 * time.Hour, "far"},
	}
	for _, policy := range []string{"sticky", "balanced", ""} {
		for _, tc := range cases {
			t.Run(policy+"/"+tc.name, func(t *testing.T) {
				accounts := NewAccounts()
				// Circular order puts near before far despite far's earlier table position.
				accounts.Set("far", autoNextQuotaAccount("far", now, 1, 1, tc.farWeeklyReset, tc.far5hReset))
				accounts.Set("active", autoNextQuotaAccount("active", now, .1, .1, time.Hour, time.Hour))
				accounts.Set("near", autoNextQuotaAccount("near", now, tc.nearWeekly, tc.nearFiveHour, tc.nearWeeklyReset, tc.near5hReset))
				settings := defaultSettings()
				settings.Policy.Name = policy
				got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
				if !ok || got != tc.want {
					t.Fatalf("selected %q (%v), want %q", got, ok, tc.want)
				}
			})
		}
	}
}

func TestAutoNextHighQuotaUsesRealEligibility(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, limits := range []struct{ weekly, fiveHour, minimum int }{
		{15, 25, 95}, {95, 25, 10}, {15, 95, 10}, {90, 90, 10}, {100, 100, 10},
	} {
		accounts := NewAccounts()
		accounts.Set("near", autoNextQuotaAccount("near", now, .9, .9, time.Minute, time.Minute))
		accounts.Set("far", autoNextQuotaAccount("far", now, 1, 1, 3*24*time.Hour, 4*time.Hour))
		settings := defaultSettings()
		settings.UI.AutoNextWeeklyThreshold = limits.weekly
		settings.UI.AutoNext5hThreshold = limits.fiveHour
		settings.Policy.MinRemainingPct = limits.minimum
		got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
		if !ok || got != "far" {
			t.Fatalf("limits %+v selected %q (%v), want far", limits, got, ok)
		}
		if ShouldAutoNextWithSettings(accounts.ByEmail["near"], defaultSettings(), now) {
			t.Fatal("90% quota unexpectedly triggered rotation")
		}
	}
}

func TestAutoNextWeeklyBudgetMatchesAccountsSixAndSeven(t *testing.T) {
	now := time.Date(2026, 10, 14, 8, 39, 20, 0, time.UTC)
	for _, policy := range []string{"sticky", "balanced", ""} {
		t.Run(policy, func(t *testing.T) {
			accounts := NewAccounts()
			// Sanitized quota values from the reported #6/#7 pair. Order favors #7
			// on ties, so this asserts that #6 actually outranks it.
			accounts.Set("active", autoNextQuotaAccount("active", now, 1, 1, time.Hour, time.Hour))
			accounts.Set("account7", autoNextQuotaAccount("account7", now, .83142585, 1,
				time.Date(2026, 10, 20, 7, 57, 11, 0, time.UTC).Sub(now),
				time.Date(2026, 10, 14, 13, 37, 4, 0, time.UTC).Sub(now)))
			accounts.Set("account6", autoNextQuotaAccount("account6", now, .59815186, 1,
				time.Date(2026, 10, 18, 7, 58, 2, 0, time.UTC).Sub(now),
				time.Date(2026, 10, 14, 13, 37, 3, 0, time.UTC).Sub(now)))
			settings := defaultSettings()
			settings.Policy.Name, settings.Policy.PreferFamily = policy, "gemini"
			got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
			if !ok || got != "account6" {
				t.Fatalf("selected %q (%v), want account6", got, ok)
			}
		})
	}
}

func TestAutoNextWeeklyBudgetRespectsReservesAndWindowPairs(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                                             string
		nearWeekly, nearFiveHour, farWeekly, farFiveHour float64
		weeklyThreshold                                  int
		want                                             string
	}{
		{"ample_weekly_budget_prefers_earlier_reset", .45, 1, .8, 1, 15, "near"},
		{"weekly_threshold_does_not_withhold_capacity", .16, 1, .8, 1, 15, "near"},
		{"20_percent_weekly_supports_full_5h", .20, 1, .8, 1, 15, "near"},
		{"configured_weekly_threshold_only_filters_eligibility", .85, 1, .99, 1, 80, "near"},
		{"5h_still_limits_low_capacity", .8, .26, .8, 1, 15, "far"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := NewAccounts()
			accounts.Set("far", autoNextQuotaAccount("far", now, tc.farWeekly, tc.farFiveHour, 6*24*time.Hour, 4*time.Hour))
			accounts.Set("near", autoNextQuotaAccount("near", now, tc.nearWeekly, tc.nearFiveHour, 4*24*time.Hour, time.Hour))
			settings := defaultSettings()
			settings.UI.AutoNextWeeklyThreshold = tc.weeklyThreshold
			got, ok := SelectAutoNextCandidateEmail(accounts, "active", settings, nil, now)
			if !ok || got != tc.want {
				t.Fatalf("selected %q (%v), want %q", got, ok, tc.want)
			}
		})
	}
	// A family with only weekly quota keeps its raw capacity comparison.
	accounts := NewAccounts()
	for _, item := range []struct {
		email  string
		weekly float64
	}{{"low", .6}, {"high", .8}} {
		a := autoNextQuotaAccount(item.email, now, item.weekly, 1, 4*24*time.Hour, time.Hour)
		group := getMap(getSlice(getMap(a["quota_snapshot"])["groups"])[0])
		group["buckets"] = []any{autoNextTestBuckets(a)[0]}
		accounts.Set(item.email, a)
	}
	got, ok := SelectAutoNextCandidateEmail(accounts, "active", defaultSettings(), nil, now)
	if !ok || got != "high" {
		t.Fatalf("weekly-only ranking selected %q (%v), want high", got, ok)
	}
	// Other families cannot reduce Gemini capacity or reset bonuses.
	first := accounts.ByEmail["high"]
	first["quota_snapshot"] = autoNextQuotaAccount("high", now, .6, 1, time.Hour, time.Hour)["quota_snapshot"]
	snapshot := getMap(first["quota_snapshot"])
	snapshot["groups"] = append(getSlice(snapshot["groups"]), map[string]any{"id": "third_party", "buckets": []any{
		map[string]any{"window": "weekly", "remaining_fraction": .16, "reset_at": isoTime(now.Add(time.Hour))},
	}})
	accounts.ByEmail["low"] = autoNextQuotaAccount("low", now, 1, 1, 4*24*time.Hour, 4*time.Hour)
	got, ok = SelectAutoNextCandidateEmail(accounts, "active", defaultSettings(), nil, now)
	if !ok || got != "high" {
		t.Fatalf("Gemini-only budget selected %q (%v), want high", got, ok)
	}
}
