package app

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func helperMakeAccount(email string, observed time.Time, buckets []map[string]any) Account {
	anyBuckets := make([]any, len(buckets))
	for i, b := range buckets {
		anyBuckets[i] = b
	}
	snapshot := map[string]any{
		"observed_at": isoTime(observed),
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

func helperMakeMultiGroupAccount(email string, observed time.Time, groups []map[string]any) Account {
	anyGroups := make([]any, len(groups))
	for i, g := range groups {
		anyGroups[i] = g
	}
	snapshot := map[string]any{
		"observed_at": isoTime(observed),
		"groups":      anyGroups,
	}
	return Account{
		"email":          email,
		"name":           email,
		"quota_snapshot": snapshot,
	}
}

func TestAutoNextThresholdBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	tests := []struct {
		name    string
		buckets []map[string]any
		want    bool
		desc    string
	}{
		{
			name: "5h exactly 0.15 does not trigger (equality boundary)",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.15},
				{"window": "weekly", "remaining_fraction": 0.50},
			},
			want: false,
		},
		{
			name: "5h just below 0.15 (0.149) triggers",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.149},
				{"window": "weekly", "remaining_fraction": 0.50},
			},
			want: true,
		},
		{
			name: "5h just above 0.15 (0.151) does not trigger",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.151},
				{"window": "weekly", "remaining_fraction": 0.50},
			},
			want: false,
		},
		{
			name: "weekly exactly 0.08 does not trigger (equality boundary)",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.50},
				{"window": "weekly", "remaining_fraction": 0.08},
			},
			want: false,
		},
		{
			name: "weekly just below 0.08 (0.079) triggers",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.50},
				{"window": "weekly", "remaining_fraction": 0.079},
			},
			want: true,
		},
		{
			name: "weekly just above 0.08 (0.081) does not trigger",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.50},
				{"window": "weekly", "remaining_fraction": 0.081},
			},
			want: false,
		},
		{
			name: "OR condition: 5h low (0.10) and weekly high (0.50) triggers",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.10},
				{"window": "weekly", "remaining_fraction": 0.50},
			},
			want: true,
		},
		{
			name: "OR condition: 5h high (0.50) and weekly low (0.05) triggers",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.50},
				{"window": "weekly", "remaining_fraction": 0.05},
			},
			want: true,
		},
		{
			name: "Both high: 5h 0.50 and weekly 0.50 does not trigger",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.50},
				{"window": "weekly", "remaining_fraction": 0.50},
			},
			want: false,
		},
		{
			name: "Both low: 5h 0.10 and weekly 0.05 triggers",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.10},
				{"window": "weekly", "remaining_fraction": 0.05},
			},
			want: true,
		},
		{
			name: "Both at exact boundaries (0.15 and 0.08) does not trigger",
			buckets: []map[string]any{
				{"window": "5h", "remaining_fraction": 0.15},
				{"window": "weekly", "remaining_fraction": 0.08},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := helperMakeAccount("test@example.com", obs, tt.buckets)
			got := ShouldAutoNext(acc, now)
			if got != tt.want {
				t.Fatalf("%s: ShouldAutoNext = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestAutoNextThresholdMultiGroupMin(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	// Group 1 has 5h = 0.25 (healthy), Group 2 has 5h = 0.12 (low).
	// Min over ALL matching buckets across all groups is 0.12, so it must trigger.
	acc := helperMakeMultiGroupAccount("test@example.com", obs, []map[string]any{
		{
			"id":   "gemini",
			"name": "Gemini Models",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.25},
				map[string]any{"window": "weekly", "remaining_fraction": 0.50},
			},
		},
		{
			"id":   "third_party",
			"name": "Third Party",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.12},
				map[string]any{"window": "weekly", "remaining_fraction": 0.50},
			},
		},
	})

	if !ShouldAutoNext(acc, now) {
		t.Fatalf("expected trigger when one group has 5h=0.12 even if other group has 5h=0.25")
	}

	// Group 1 has weekly = 0.20, Group 2 has weekly = 0.06 (< 0.08)
	acc2 := helperMakeMultiGroupAccount("test@example.com", obs, []map[string]any{
		{
			"id":   "gemini",
			"name": "Gemini Models",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.50},
				map[string]any{"window": "weekly", "remaining_fraction": 0.20},
			},
		},
		{
			"id":   "third_party",
			"name": "Third Party",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.50},
				map[string]any{"window": "weekly", "remaining_fraction": 0.06},
			},
		},
	})

	if !ShouldAutoNext(acc2, now) {
		t.Fatalf("expected trigger when one group has weekly=0.06 even if other group has weekly=0.20")
	}
}

func TestAutoNextThresholdMissingWindows(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	// Missing 5h window: only weekly is present with 0.50.
	// Missing 5h must NOT be treated as 0 (which would have falsely triggered < 0.15).
	acc1 := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	if ShouldAutoNext(acc1, now) {
		t.Fatalf("missing 5h window must not be treated as 0")
	}

	// Missing weekly window: only 5h is present with 0.50.
	// Missing weekly must NOT be treated as 0 (which would have falsely triggered < 0.08).
	acc2 := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.50},
	})
	if ShouldAutoNext(acc2, now) {
		t.Fatalf("missing weekly window must not be treated as 0")
	}

	// Missing 5h window, but weekly is low (0.05). Should trigger via weekly.
	acc3 := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "weekly", "remaining_fraction": 0.05},
	})
	if !ShouldAutoNext(acc3, now) {
		t.Fatalf("weekly < 0.08 should trigger even when 5h is missing")
	}

	// Missing weekly window, but 5h is low (0.10). Should trigger via 5h.
	acc4 := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
	})
	if !ShouldAutoNext(acc4, now) {
		t.Fatalf("5h < 0.15 should trigger even when weekly is missing")
	}

	// Neither 5h nor weekly is present (e.g. only "daily" window).
	// Unknown data fail closed -> false.
	acc5 := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "daily", "remaining_fraction": 0.01},
	})
	if ShouldAutoNext(acc5, now) {
		t.Fatalf("buckets without 5h or weekly window should fail closed")
	}

	// Empty buckets -> fails closed -> false.
	acc6 := helperMakeAccount("test@example.com", obs, []map[string]any{})
	if ShouldAutoNext(acc6, now) {
		t.Fatalf("empty buckets should fail closed")
	}
}

func TestAutoNextThresholdMalformedAndUnusable(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	// NaN fraction is ignored as unusable
	accNaN := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": math.NaN()},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	if ShouldAutoNext(accNaN, now) {
		t.Fatalf("NaN fraction must be ignored and not trigger")
	}

	// +Inf and -Inf are ignored
	accInf := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": math.Inf(1)},
		{"window": "weekly", "remaining_fraction": math.Inf(-1)},
	})
	if ShouldAutoNext(accInf, now) {
		t.Fatalf("infinite fractions must be ignored and fail closed")
	}

	// Out of [0, 1] range: negative fraction
	accNeg := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": -0.5},
	})
	if ShouldAutoNext(accNeg, now) {
		t.Fatalf("negative fractions outside [0, 1] must be ignored and fail closed")
	}

	// Out of [0, 1] range: > 1.0
	accOver := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 1.5},
	})
	if ShouldAutoNext(accOver, now) {
		t.Fatalf("fractions > 1.0 must be ignored and fail closed")
	}

	// Non-numeric string fraction
	accStr := helperMakeAccount("test@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": "not-a-number"},
	})
	if ShouldAutoNext(accStr, now) {
		t.Fatalf("non-numeric fraction must be ignored and fail closed")
	}

	// Group 1 has NaN, Group 2 has valid 5h = 0.10.
	// NaN is ignored, valid 0.10 is considered and triggers.
	accMixed := helperMakeMultiGroupAccount("test@example.com", obs, []map[string]any{
		{
			"id": "g1",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": math.NaN()},
			},
		},
		{
			"id": "g2",
			"buckets": []any{
				map[string]any{"window": "5h", "remaining_fraction": 0.10},
			},
		},
	})
	if !ShouldAutoNext(accMixed, now) {
		t.Fatalf("usable fraction in g2 should be evaluated when g1 is NaN")
	}

	// Nil / missing data
	if ShouldAutoNext(nil, now) {
		t.Fatalf("nil account must fail closed")
	}
	if ShouldAutoNext(Account{}, now) {
		t.Fatalf("empty account must fail closed")
	}
	if ShouldAutoNext(Account{"quota_snapshot": nil}, now) {
		t.Fatalf("nil snapshot must fail closed")
	}
	if ShouldAutoNext(Account{"quota_snapshot": map[string]any{"observed_at": "invalid-time"}}, now) {
		t.Fatalf("invalid observed_at must fail closed")
	}
}

func TestAutoNextThresholdStaleAndFuture(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	lowBuckets := []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10},
		{"window": "weekly", "remaining_fraction": 0.05},
	}

	// Future observation: observed_at is in the future relative to now -> reject
	accFuture := helperMakeAccount("test@example.com", now.Add(5*time.Second), lowBuckets)
	if ShouldAutoNext(accFuture, now) {
		t.Fatalf("future observation must be rejected even with low quota")
	}

	// Stale observation: older than 2 minutes -> reject
	accStale := helperMakeAccount("test@example.com", now.Add(-2*time.Minute-time.Second), lowBuckets)
	if ShouldAutoNext(accStale, now) {
		t.Fatalf("stale observation (>2m) must be rejected even with low quota")
	}

	// Exactly 2 minutes old -> boundary accepted
	accExact2m := helperMakeAccount("test@example.com", now.Add(-2*time.Minute), lowBuckets)
	if !ShouldAutoNext(accExact2m, now) {
		t.Fatalf("observation at exactly 2m boundary should be accepted")
	}

	// 1 minute old -> accepted
	acc1m := helperMakeAccount("test@example.com", now.Add(-1*time.Minute), lowBuckets)
	if !ShouldAutoNext(acc1m, now) {
		t.Fatalf("fresh observation (1m) should trigger")
	}

	// Exactly now -> accepted
	accNow := helperMakeAccount("test@example.com", now, lowBuckets)
	if !ShouldAutoNext(accNow, now) {
		t.Fatalf("fresh observation (now) should trigger")
	}
}

func TestAutoNextCandidateSelection(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	makeHealthyBuckets := func(pct float64) []map[string]any {
		return []map[string]any{
			{"window": "5h", "remaining_fraction": pct},
			{"window": "weekly", "remaining_fraction": pct},
		}
	}

	accA := helperMakeAccount("a@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10}, // Low quota -> triggers switch
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	accB := helperMakeAccount("b@example.com", obs, makeHealthyBuckets(0.60))
	accC := helperMakeAccount("c@example.com", obs, makeHealthyBuckets(0.70))

	accounts := NewAccounts()
	accounts.Set("a@example.com", accA)
	accounts.Set("b@example.com", accB)
	accounts.Set("c@example.com", accC)

	settings := defaultSettings()
	settings.Policy.Name = "sticky" // sticky should become round-robin for next

	// 1. Basic round-robin from active A -> candidate should be B
	cand, ok := SelectAutoNextCandidate(accounts, "a@example.com", settings, nil, now)
	if !ok || cand == nil {
		t.Fatalf("expected candidate, got ok=%v cand=%v", ok, cand)
	}
	if getString(cand, "email") != "b@example.com" {
		t.Fatalf("expected B as next round-robin candidate, got %s", getString(cand, "email"))
	}

	// 2. Active account exclusion: active account must never be selected
	accountsOnlyActive := NewAccounts()
	accountsOnlyActive.Set("a@example.com", accA)
	_, okActiveOnly := SelectAutoNextCandidate(accountsOnlyActive, "a@example.com", settings, nil, now)
	if okActiveOnly {
		t.Fatalf("active account should never be selected as next candidate")
	}

	// 3. Exclude refresh errors: if B has a refresh error, C should be selected
	errorsB := map[string]string{"b@example.com": "500 internal server error"}
	candErr, okErr := SelectAutoNextCandidate(accounts, "a@example.com", settings, errorsB, now)
	if !okErr || getString(candErr, "email") != "c@example.com" {
		t.Fatalf("expected C when B has refresh error, got %v", getString(candErr, "email"))
	}

	// 4. Exclude stale snapshots: if B has a stale snapshot, C should be selected
	accBStale := helperMakeAccount("b@example.com", now.Add(-3*time.Minute), makeHealthyBuckets(0.60))
	accountsStaleB := NewAccounts()
	accountsStaleB.Set("a@example.com", accA)
	accountsStaleB.Set("b@example.com", accBStale)
	accountsStaleB.Set("c@example.com", accC)
	candStale, okStale := SelectAutoNextCandidate(accountsStaleB, "a@example.com", settings, nil, now)
	if !okStale || getString(candStale, "email") != "c@example.com" {
		t.Fatalf("expected C when B has stale snapshot, got %v", getString(candStale, "email"))
	}

	// 5. Exclude future snapshots: if B has a future snapshot, C should be selected
	accBFuture := helperMakeAccount("b@example.com", now.Add(10*time.Second), makeHealthyBuckets(0.60))
	accountsFutureB := NewAccounts()
	accountsFutureB.Set("a@example.com", accA)
	accountsFutureB.Set("b@example.com", accBFuture)
	accountsFutureB.Set("c@example.com", accC)
	candFuture, okFuture := SelectAutoNextCandidate(accountsFutureB, "a@example.com", settings, nil, now)
	if !okFuture || getString(candFuture, "email") != "c@example.com" {
		t.Fatalf("expected C when B has future snapshot, got %v", getString(candFuture, "email"))
	}

	// 6. Exclude candidate below threshold:
	// If B has 5h = 0.12 (< 0.15), B is below threshold and must be excluded.
	accBLow := helperMakeAccount("b@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.12},
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	accountsLowB := NewAccounts()
	accountsLowB.Set("a@example.com", accA)
	accountsLowB.Set("b@example.com", accBLow)
	accountsLowB.Set("c@example.com", accC)
	candLowB, okLowB := SelectAutoNextCandidate(accountsLowB, "a@example.com", settings, nil, now)
	if !okLowB || getString(candLowB, "email") != "c@example.com" {
		t.Fatalf("expected C when B is below 5h threshold, got %v", getString(candLowB, "email"))
	}

	// 7. All candidates below threshold -> returns (nil, false) (no loops)
	accCLow := helperMakeAccount("c@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.50},
		{"window": "weekly", "remaining_fraction": 0.05}, // below weekly threshold
	})
	accountsAllLow := NewAccounts()
	accountsAllLow.Set("a@example.com", accA)
	accountsAllLow.Set("b@example.com", accBLow)
	accountsAllLow.Set("c@example.com", accCLow)
	candNone, okNone := SelectAutoNextCandidate(accountsAllLow, "a@example.com", settings, nil, now)
	if okNone || candNone != nil {
		t.Fatalf("expected no candidate when all accounts are below threshold, got %v", candNone)
	}

	// 8. Preserving configured minRemainingPct:
	// If Policy.MinRemainingPct is 75%, and B has 60%, B is excluded. C has 70%, also excluded.
	settingsHighMin := settings
	settingsHighMin.Policy.MinRemainingPct = 75
	_, okHighMin := SelectAutoNextCandidate(accounts, "a@example.com", settingsHighMin, nil, now)
	if okHighMin {
		t.Fatalf("expected exclusion when remaining is below MinRemainingPct")
	}

	// 9. Cooldown exclusion
	accBCooldown := helperMakeAccount("b@example.com", obs, makeHealthyBuckets(0.60))
	accBCooldown["quota_limits"] = map[string]any{
		"gemini": map[string]any{
			"model":    "gemini",
			"reset_at": isoTime(now.Add(10 * time.Minute)),
		},
	}
	accountsCooldownB := NewAccounts()
	accountsCooldownB.Set("a@example.com", accA)
	accountsCooldownB.Set("b@example.com", accBCooldown)
	accountsCooldownB.Set("c@example.com", accC)
	candCool, okCool := SelectAutoNextCandidate(accountsCooldownB, "a@example.com", settings, nil, now)
	if !okCool || getString(candCool, "email") != "c@example.com" {
		t.Fatalf("expected C when B is in cooldown, got %v", getString(candCool, "email"))
	}
}

func TestAutoNextCandidateSelectionRegressionF2P2(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	accActive := helperMakeAccount("active@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10}, // triggers auto-next
		{"window": "weekly", "remaining_fraction": 0.50},
	})
	accFallback := helperMakeAccount("fallback@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.60},
		{"window": "weekly", "remaining_fraction": 0.60},
	})

	settings := defaultSettings()

	// 1. Rejection of invalid-only candidate snapshots (1.5, NaN, Inf, -0.5)
	invalidFractions := []struct {
		name string
		val  float64
	}{
		{"1.5", 1.5},
		{"negative", -0.5},
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}

	for _, tc := range invalidFractions {
		t.Run("invalid fraction "+tc.name, func(t *testing.T) {
			accInvalid := helperMakeAccount("invalid@example.com", obs, []map[string]any{
				{"window": "5h", "remaining_fraction": tc.val},
			})

			// Sole candidate is invalid -> no candidate selected
			accountsSingle := NewAccounts()
			accountsSingle.Set("active@example.com", accActive)
			accountsSingle.Set("invalid@example.com", accInvalid)
			cand, ok := SelectAutoNextCandidate(accountsSingle, "active@example.com", settings, nil, now)
			if ok || cand != nil {
				t.Fatalf("expected invalid-only candidate (%s) to be rejected, got %v", tc.name, cand)
			}

			// When fallback healthy candidate is available, fallback must be selected
			accountsWithFallback := NewAccounts()
			accountsWithFallback.Set("active@example.com", accActive)
			accountsWithFallback.Set("invalid@example.com", accInvalid)
			accountsWithFallback.Set("fallback@example.com", accFallback)
			candFb, okFb := SelectAutoNextCandidate(accountsWithFallback, "active@example.com", settings, nil, now)
			if !okFb || getString(candFb, "email") != "fallback@example.com" {
				t.Fatalf("expected fallback candidate when other has invalid fraction (%s), got %v", tc.name, candFb)
			}
		})
	}

	// 2. Rejection of unknown-window snapshots (e.g. only daily window)
	t.Run("unknown-window snapshot rejected", func(t *testing.T) {
		accUnknown := helperMakeAccount("unknown@example.com", obs, []map[string]any{
			{"window": "daily", "remaining_fraction": 0.50},
		})

		// Sole candidate has unknown window -> rejected
		accountsSingle := NewAccounts()
		accountsSingle.Set("active@example.com", accActive)
		accountsSingle.Set("unknown@example.com", accUnknown)
		cand, ok := SelectAutoNextCandidate(accountsSingle, "active@example.com", settings, nil, now)
		if ok || cand != nil {
			t.Fatalf("expected unknown-window candidate to be rejected, got %v", cand)
		}

		// When fallback is available -> fallback selected
		accountsWithFallback := NewAccounts()
		accountsWithFallback.Set("active@example.com", accActive)
		accountsWithFallback.Set("unknown@example.com", accUnknown)
		accountsWithFallback.Set("fallback@example.com", accFallback)
		candFb, okFb := SelectAutoNextCandidate(accountsWithFallback, "active@example.com", settings, nil, now)
		if !okFb || getString(candFb, "email") != "fallback@example.com" {
			t.Fatalf("expected fallback candidate when other has unknown window, got %v", candFb)
		}
	})

	// 3. Acceptance of weekly-only valid snapshots
	t.Run("weekly-only valid snapshot accepted", func(t *testing.T) {
		// Weekly-only with 0.50
		accWeeklyOnly := helperMakeAccount("weekly@example.com", obs, []map[string]any{
			{"window": "weekly", "remaining_fraction": 0.50},
		})
		accounts := NewAccounts()
		accounts.Set("active@example.com", accActive)
		accounts.Set("weekly@example.com", accWeeklyOnly)
		cand, ok := SelectAutoNextCandidate(accounts, "active@example.com", settings, nil, now)
		if !ok || cand == nil {
			t.Fatalf("expected weekly-only candidate to be accepted, got ok=%v", ok)
		}
		if getString(cand, "email") != "weekly@example.com" {
			t.Fatalf("expected weekly@example.com, got %s", getString(cand, "email"))
		}

		// Weekly-only at boundary 0.08
		accWeeklyBoundary := helperMakeAccount("weekly8@example.com", obs, []map[string]any{
			{"window": "weekly", "remaining_fraction": 0.08},
		})
		accountsBoundary := NewAccounts()
		accountsBoundary.Set("active@example.com", accActive)
		accountsBoundary.Set("weekly8@example.com", accWeeklyBoundary)
		settingsBoundary := settings
		settingsBoundary.Policy.MinRemainingPct = 8
		candB, okB := SelectAutoNextCandidate(accountsBoundary, "active@example.com", settingsBoundary, nil, now)
		if !okB || candB == nil {
			t.Fatalf("expected weekly-only candidate at 0.08 boundary to be accepted, got ok=%v", okB)
		}
		if getString(candB, "email") != "weekly8@example.com" {
			t.Fatalf("expected weekly8@example.com, got %s", getString(candB, "email"))
		}
	})

	// 4. Acceptance of exact 15% (0.15 for 5h) and 8% (0.08 for weekly) boundaries
	t.Run("exact boundaries accepted", func(t *testing.T) {
		// 5h exact 0.15, weekly 0.50
		acc5hBoundary := helperMakeAccount("5h15@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.15},
			{"window": "weekly", "remaining_fraction": 0.50},
		})
		accounts5h := NewAccounts()
		accounts5h.Set("active@example.com", accActive)
		accounts5h.Set("5h15@example.com", acc5hBoundary)
		cand5h, ok5h := SelectAutoNextCandidate(accounts5h, "active@example.com", settings, nil, now)
		if !ok5h || getString(cand5h, "email") != "5h15@example.com" {
			t.Fatalf("expected 5h=0.15 candidate to be accepted, got ok=%v cand=%v", ok5h, cand5h)
		}

		// 5h 0.50, weekly exact 0.08
		accWeeklyBoundary := helperMakeAccount("w8@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.50},
			{"window": "weekly", "remaining_fraction": 0.08},
		})
		accountsW := NewAccounts()
		accountsW.Set("active@example.com", accActive)
		accountsW.Set("w8@example.com", accWeeklyBoundary)
		settingsW := settings
		settingsW.Policy.MinRemainingPct = 8
		candW, okW := SelectAutoNextCandidate(accountsW, "active@example.com", settingsW, nil, now)
		if !okW || getString(candW, "email") != "w8@example.com" {
			t.Fatalf("expected weekly=0.08 candidate to be accepted, got ok=%v cand=%v", okW, candW)
		}

		// Both exact boundaries: 5h = 0.15, weekly = 0.08
		accBothBoundary := helperMakeAccount("both@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.15},
			{"window": "weekly", "remaining_fraction": 0.08},
		})
		accountsBoth := NewAccounts()
		accountsBoth.Set("active@example.com", accActive)
		accountsBoth.Set("both@example.com", accBothBoundary)
		settingsBoth := settings
		settingsBoth.Policy.MinRemainingPct = 8
		candBoth, okBoth := SelectAutoNextCandidate(accountsBoth, "active@example.com", settingsBoth, nil, now)
		if !okBoth || getString(candBoth, "email") != "both@example.com" {
			t.Fatalf("expected both boundary candidate (0.15, 0.08) to be accepted, got ok=%v cand=%v", okBoth, candBoth)
		}

		// Just below 5h boundary: 5h = 0.149 must be rejected
		accBelow5h := helperMakeAccount("below5h@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.149},
			{"window": "weekly", "remaining_fraction": 0.50},
		})
		accountsBelow5h := NewAccounts()
		accountsBelow5h.Set("active@example.com", accActive)
		accountsBelow5h.Set("below5h@example.com", accBelow5h)
		candBelow5h, okBelow5h := SelectAutoNextCandidate(accountsBelow5h, "active@example.com", settings, nil, now)
		if okBelow5h || candBelow5h != nil {
			t.Fatalf("expected candidate just below 5h boundary (0.149) to be rejected, got %v", candBelow5h)
		}

		// Just below weekly boundary: weekly = 0.079 must be rejected
		accBelowW := helperMakeAccount("belowW@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.50},
			{"window": "weekly", "remaining_fraction": 0.079},
		})
		accountsBelowW := NewAccounts()
		accountsBelowW.Set("active@example.com", accActive)
		accountsBelowW.Set("belowW@example.com", accBelowW)
		settingsBelowW := settings
		settingsBelowW.Policy.MinRemainingPct = 8
		candBelowW, okBelowW := SelectAutoNextCandidate(accountsBelowW, "active@example.com", settingsBelowW, nil, now)
		if okBelowW || candBelowW != nil {
			t.Fatalf("expected candidate just below weekly boundary (0.079) to be rejected, got %v", candBelowW)
		}
	})

	// 5. Daily window alongside valid 5h/weekly does not drag quota or clamp
	t.Run("daily window alongside valid 5h/weekly does not drag quota or clamp", func(t *testing.T) {
		accMixed := helperMakeAccount("mixed@example.com", obs, []map[string]any{
			{"window": "daily", "remaining_fraction": 0.01}, // would fail minRemaining if evaluated
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.80},
		})
		accounts := NewAccounts()
		accounts.Set("active@example.com", accActive)
		accounts.Set("mixed@example.com", accMixed)
		cand, ok := SelectAutoNextCandidate(accounts, "active@example.com", settings, nil, now)
		if !ok || getString(cand, "email") != "mixed@example.com" {
			t.Fatalf("expected candidate with valid 5h/weekly to be accepted despite daily bucket, got ok=%v cand=%v", ok, cand)
		}
	})
}

func TestAutoNextPolicyReserveRegressions(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	obs := now.Add(-30 * time.Second)

	accActive := helperMakeAccount("active@example.com", obs, []map[string]any{
		{"window": "5h", "remaining_fraction": 0.10}, // triggers auto-switch
		{"window": "weekly", "remaining_fraction": 0.50},
	})

	// 1. Candidate with 5h 80% and weekly 9% with default min10 (MinRemainingPct = 10): MUST be rejected.
	t.Run("default min10 rejects candidate with 9 percent", func(t *testing.T) {
		settings := defaultSettings() // Policy.MinRemainingPct == 10
		if settings.Policy.MinRemainingPct != 10 {
			t.Fatalf("expected default MinRemainingPct to be 10, got %d", settings.Policy.MinRemainingPct)
		}

		accCand9 := helperMakeAccount("cand9@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.09},
		})

		accounts := NewAccounts()
		accounts.Set("active@example.com", accActive)
		accounts.Set("cand9@example.com", accCand9)

		cand, ok := SelectAutoNextCandidate(accounts, "active@example.com", settings, nil, now)
		if ok || cand != nil {
			t.Fatalf("expected candidate with 9%% to be rejected by default min10, got cand=%v ok=%v", cand, ok)
		}
	})

	// 2. With MinRemainingPct = 9: candidate with 8% is rejected, candidate with equal reserve 9% is accepted.
	t.Run("min9 rejects 8 percent and accepts equal 9 percent", func(t *testing.T) {
		settings9 := defaultSettings()
		settings9.Policy.MinRemainingPct = 9

		acc8 := helperMakeAccount("cand8@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.08},
		})
		acc9 := helperMakeAccount("cand9@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.09},
		})

		// Candidate with 8% is rejected when sole candidate
		accounts8 := NewAccounts()
		accounts8.Set("active@example.com", accActive)
		accounts8.Set("cand8@example.com", acc8)
		cand8, ok8 := SelectAutoNextCandidate(accounts8, "active@example.com", settings9, nil, now)
		if ok8 || cand8 != nil {
			t.Fatalf("expected candidate with 8%% to be rejected when min_remaining_pct=9, got cand=%v", cand8)
		}

		// Candidate with equal reserve 9% is accepted when sole candidate
		accounts9 := NewAccounts()
		accounts9.Set("active@example.com", accActive)
		accounts9.Set("cand9@example.com", acc9)
		cand9, ok9 := SelectAutoNextCandidate(accounts9, "active@example.com", settings9, nil, now)
		if !ok9 || cand9 == nil || getString(cand9, "email") != "cand9@example.com" {
			t.Fatalf("expected candidate with 9%% to be accepted when min_remaining_pct=9, got ok=%v cand=%v", ok9, cand9)
		}

		// When both 8% and 9% are present, 8% is rejected and 9% is accepted
		accountsBoth := NewAccounts()
		accountsBoth.Set("active@example.com", accActive)
		accountsBoth.Set("cand8@example.com", acc8)
		accountsBoth.Set("cand9@example.com", acc9)
		candBoth, okBoth := SelectAutoNextCandidate(accountsBoth, "active@example.com", settings9, nil, now)
		if !okBoth || candBoth == nil || getString(candBoth, "email") != "cand9@example.com" {
			t.Fatalf("expected 9%% candidate to be selected over 8%% candidate with min_remaining_pct=9, got ok=%v cand=%v", okBoth, candBoth)
		}
	})

	// 3. With MinRemainingPct = 10: candidate with 9% is rejected, candidate with equal reserve 10% is accepted.
	t.Run("min10 rejects 9 percent and accepts equal 10 percent", func(t *testing.T) {
		settings10 := defaultSettings()
		settings10.Policy.MinRemainingPct = 10

		acc9 := helperMakeAccount("cand9@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.09},
		})
		acc10 := helperMakeAccount("cand10@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.10},
		})

		// Candidate with 9% is rejected when sole candidate
		accounts9 := NewAccounts()
		accounts9.Set("active@example.com", accActive)
		accounts9.Set("cand9@example.com", acc9)
		cand9, ok9 := SelectAutoNextCandidate(accounts9, "active@example.com", settings10, nil, now)
		if ok9 || cand9 != nil {
			t.Fatalf("expected candidate with 9%% to be rejected when min_remaining_pct=10, got cand=%v", cand9)
		}

		// Candidate with equal reserve 10% is accepted when sole candidate
		accounts10 := NewAccounts()
		accounts10.Set("active@example.com", accActive)
		accounts10.Set("cand10@example.com", acc10)
		cand10, ok10 := SelectAutoNextCandidate(accounts10, "active@example.com", settings10, nil, now)
		if !ok10 || cand10 == nil || getString(cand10, "email") != "cand10@example.com" {
			t.Fatalf("expected candidate with 10%% to be accepted when min_remaining_pct=10, got ok=%v cand=%v", ok10, cand10)
		}

		// When both 9% and 10% are present, 9% is rejected and 10% is accepted
		accountsBoth := NewAccounts()
		accountsBoth.Set("active@example.com", accActive)
		accountsBoth.Set("cand9@example.com", acc9)
		accountsBoth.Set("cand10@example.com", acc10)
		candBoth, okBoth := SelectAutoNextCandidate(accountsBoth, "active@example.com", settings10, nil, now)
		if !okBoth || candBoth == nil || getString(candBoth, "email") != "cand10@example.com" {
			t.Fatalf("expected 10%% candidate to be selected over 9%% candidate with min_remaining_pct=10, got ok=%v cand=%v", okBoth, candBoth)
		}
	})

	// 4. Strict auto thresholds (5h < 0.15 OR weekly < 0.08) remain strict.
	t.Run("strict auto thresholds remain strict regardless of policy reserve", func(t *testing.T) {
		// Even with relaxed min_remaining_pct = 5 or 0:
		settingsRelaxed := defaultSettings()
		settingsRelaxed.Policy.MinRemainingPct = 5

		// Candidate with 5h = 0.149 (< 0.15 threshold) and weekly = 0.80:
		// Remaining is 14.9% (> 5% min remaining), but threshold triggers -> must be rejected!
		accBelow5h := helperMakeAccount("below5h@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.149},
			{"window": "weekly", "remaining_fraction": 0.80},
		})
		accounts5h := NewAccounts()
		accounts5h.Set("active@example.com", accActive)
		accounts5h.Set("below5h@example.com", accBelow5h)
		cand5h, ok5h := SelectAutoNextCandidate(accounts5h, "active@example.com", settingsRelaxed, nil, now)
		if ok5h || cand5h != nil {
			t.Fatalf("expected candidate with 5h=0.149 to be rejected by strict threshold even when min_remaining_pct=5, got cand=%v", cand5h)
		}

		// Candidate with weekly = 0.079 (< 0.08 threshold) and 5h = 0.80:
		// Remaining is 7.9% (> 5% min remaining), but threshold triggers -> must be rejected!
		accBelowW := helperMakeAccount("belowW@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.80},
			{"window": "weekly", "remaining_fraction": 0.079},
		})
		accountsW := NewAccounts()
		accountsW.Set("active@example.com", accActive)
		accountsW.Set("belowW@example.com", accBelowW)
		candW, okW := SelectAutoNextCandidate(accountsW, "active@example.com", settingsRelaxed, nil, now)
		if okW || candW != nil {
			t.Fatalf("expected candidate with weekly=0.079 to be rejected by strict threshold even when min_remaining_pct=5, got cand=%v", candW)
		}

		// Exact threshold boundary: 5h = 0.15, weekly = 0.08
		// With MinRemainingPct = 5: remaining is 8.0% (>= 5% reserve and >= 0.08 threshold) -> accepted!
		accBoundary := helperMakeAccount("boundary@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.15},
			{"window": "weekly", "remaining_fraction": 0.08},
		})
		accountsBoundary := NewAccounts()
		accountsBoundary.Set("active@example.com", accActive)
		accountsBoundary.Set("boundary@example.com", accBoundary)
		candBoundary, okBoundary := SelectAutoNextCandidate(accountsBoundary, "active@example.com", settingsRelaxed, nil, now)
		if !okBoundary || candBoundary == nil || getString(candBoundary, "email") != "boundary@example.com" {
			t.Fatalf("expected boundary candidate (5h=0.15, weekly=0.08) to be accepted with min_remaining_pct=5, got ok=%v cand=%v", okBoundary, candBoundary)
		}

		// Active account threshold check direct verification:
		// 5h = 0.149 triggers
		accTestActive5h := helperMakeAccount("active5h@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.149},
			{"window": "weekly", "remaining_fraction": 0.50},
		})
		trig5h, res5h := CheckAutoNextThreshold(accTestActive5h, now)
		if !res5h.Valid || !trig5h {
			t.Fatalf("expected 5h=0.149 to trigger auto-next, got valid=%v trig=%v", res5h.Valid, trig5h)
		}

		// weekly = 0.079 triggers
		accTestActiveW := helperMakeAccount("activeW@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.50},
			{"window": "weekly", "remaining_fraction": 0.079},
		})
		trigW, resW := CheckAutoNextThreshold(accTestActiveW, now)
		if !resW.Valid || !trigW {
			t.Fatalf("expected weekly=0.079 to trigger auto-next, got valid=%v trig=%v", resW.Valid, trigW)
		}

		// 5h = 0.15 and weekly = 0.08 does NOT trigger
		accTestActiveSafe := helperMakeAccount("activeSafe@example.com", obs, []map[string]any{
			{"window": "5h", "remaining_fraction": 0.15},
			{"window": "weekly", "remaining_fraction": 0.08},
		})
		trigSafe, resSafe := CheckAutoNextThreshold(accTestActiveSafe, now)
		if !resSafe.Valid || trigSafe {
			t.Fatalf("expected exact boundary (0.15, 0.08) to not trigger, got valid=%v trig=%v", resSafe.Valid, trigSafe)
		}
	})
}

func TestUIConfigAutoNextPersistenceAndCLI(t *testing.T) {
	paths := testPaths(t)
	store := NewStore(paths)

	// 1. Verify default settings has AutoNext == false
	defaults := defaultSettings()
	if defaults.UI.AutoNext != false {
		t.Fatalf("expected default AutoNext to be false, got %v", defaults.UI.AutoNext)
	}

	// 2. Roundtrip persistence: save true, load back
	settings, err := store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.UI.AutoNext != false {
		t.Fatalf("expected initial AutoNext to be false, got %v", settings.UI.AutoNext)
	}

	settings.UI.AutoNext = true
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UI.AutoNext {
		t.Fatalf("expected AutoNext to persist as true, got %v", loaded.UI.AutoNext)
	}
	if loaded.Schema != stateSchema {
		t.Fatalf("schema must not change: got %d, want %d", loaded.Schema, stateSchema)
	}

	// 3. Roundtrip persistence: save false, load back
	loaded.UI.AutoNext = false
	if err := store.SaveSettings(loaded); err != nil {
		t.Fatal(err)
	}
	loadedAgain, err := store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loadedAgain.UI.AutoNext != false {
		t.Fatalf("expected AutoNext to persist as false, got %v", loadedAgain.UI.AutoNext)
	}

	// 4. CLI config set ui.auto_next true / false
	var out, errOut bytes.Buffer
	app := &Application{
		Version: "2.10.0",
		In:      strings.NewReader(""),
		Out:     &out,
		Err:     &errOut,
		paths:   paths,
		store:   store,
		vault:   fakeAccountVault{},
		p:       makePalette(false),
	}

	if code := app.Run(context.Background(), []string{"config", "set", "ui.auto_next", "true"}); code != 0 {
		t.Fatalf("config set ui.auto_next true failed: code=%d err=%s", code, errOut.String())
	}
	cliLoaded, err := store.LoadSettings()
	if err != nil || !cliLoaded.UI.AutoNext {
		t.Fatalf("expected AutoNext=true via CLI, got %v err=%v", cliLoaded.UI.AutoNext, err)
	}

	// CLI config set ui.auto_next false
	out.Reset()
	errOut.Reset()
	if code := app.Run(context.Background(), []string{"config", "set", "ui.auto_next", "false"}); code != 0 {
		t.Fatalf("config set ui.auto_next false failed: code=%d err=%s", code, errOut.String())
	}
	cliLoaded2, err := store.LoadSettings()
	if err != nil || cliLoaded2.UI.AutoNext != false {
		t.Fatalf("expected AutoNext=false via CLI, got %v err=%v", cliLoaded2.UI.AutoNext, err)
	}

	// CLI config set auto_next true (alias key without ui. prefix)
	out.Reset()
	errOut.Reset()
	if code := app.Run(context.Background(), []string{"config", "set", "auto_next", "true"}); code != 0 {
		t.Fatalf("config set auto_next true failed: code=%d err=%s", code, errOut.String())
	}
	cliLoaded3, err := store.LoadSettings()
	if err != nil || !cliLoaded3.UI.AutoNext {
		t.Fatalf("expected AutoNext=true via CLI alias auto_next, got %v err=%v", cliLoaded3.UI.AutoNext, err)
	}

	// CLI boolean parser error on invalid value
	out.Reset()
	errOut.Reset()
	if code := app.Run(context.Background(), []string{"config", "set", "ui.auto_next", "not-a-bool"}); code == 0 {
		t.Fatalf("expected failure on invalid boolean value, got exit code 0")
	}
	if !strings.Contains(errOut.String(), "value must be true or false") {
		t.Fatalf("expected boolean error message, got %s", errOut.String())
	}
}
