package core

import (
	"testing"
	"time"
)

func mustParseLocal(t *testing.T, layout, value string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", value, err)
	}
	return ts
}

func TestMostRecentPastSlotAt(t *testing.T) {
	tests := []struct {
		name     string
		schedule []string
		now      string // "15:04" on 2026-07-18
		wantSlot string
		wantOK   bool
	}{
		{
			name:     "now before all slots returns none",
			schedule: []string{"09:00", "12:00", "18:00"},
			now:      "08:00",
			wantSlot: "",
			wantOK:   false,
		},
		{
			name:     "now after all slots returns last slot",
			schedule: []string{"09:00", "12:00", "18:00"},
			now:      "23:59",
			wantSlot: "18:00",
			wantOK:   true,
		},
		{
			name:     "now between slots returns the earlier one",
			schedule: []string{"09:00", "12:00", "18:00"},
			now:      "15:00",
			wantSlot: "12:00",
			wantOK:   true,
		},
		{
			name:     "now exactly equal to a slot is treated as past (boundary: After() is false so it is included, not skipped)",
			schedule: []string{"09:00", "12:00", "18:00"},
			now:      "12:00",
			wantSlot: "12:00",
			wantOK:   true,
		},
		{
			name:     "empty schedule returns none",
			schedule: []string{},
			now:      "12:00",
			wantSlot: "",
			wantOK:   false,
		},
		{
			name:     "malformed entries are skipped",
			schedule: []string{"9am", "", "12:00"},
			now:      "13:00",
			wantSlot: "12:00",
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 "+tt.now)
			slot, ok := mostRecentPastSlotAt(tt.schedule, now)
			if slot != tt.wantSlot || ok != tt.wantOK {
				t.Errorf("mostRecentPastSlotAt(%v, %s) = (%q, %v), want (%q, %v)",
					tt.schedule, tt.now, slot, ok, tt.wantSlot, tt.wantOK)
			}
		})
	}
}

func TestCalculateNextScheduledTimeAt(t *testing.T) {
	t.Run("next slot today", func(t *testing.T) {
		now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 10:00")
		schedule := []string{"09:00", "12:00", "18:00"}

		nextTime, slot := calculateNextScheduledTimeAt(schedule, now)

		wantTime := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 12:00")
		if !nextTime.Equal(wantTime) {
			t.Errorf("nextTime = %v, want %v", nextTime, wantTime)
		}
		if slot != "12:00" {
			t.Errorf("slot = %q, want %q", slot, "12:00")
		}
	})

	t.Run("all slots past rolls over to tomorrow's first slot", func(t *testing.T) {
		now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 20:00")
		schedule := []string{"09:00", "12:00", "18:00"}

		nextTime, slot := calculateNextScheduledTimeAt(schedule, now)

		wantTime := mustParseLocal(t, "2006-01-02 15:04", "2026-07-19 09:00")
		if !nextTime.Equal(wantTime) {
			t.Errorf("nextTime = %v, want %v", nextTime, wantTime)
		}
		if slot != "09:00" {
			t.Errorf("slot = %q, want %q", slot, "09:00")
		}
	})

	t.Run("now exactly equal to a slot skips it as not-future (boundary: rolls over to tomorrow when it's the only/last slot)", func(t *testing.T) {
		now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 12:00")
		schedule := []string{"12:00"}

		nextTime, slot := calculateNextScheduledTimeAt(schedule, now)

		wantTime := mustParseLocal(t, "2006-01-02 15:04", "2026-07-19 12:00")
		if !nextTime.Equal(wantTime) {
			t.Errorf("nextTime = %v, want %v", nextTime, wantTime)
		}
		if slot != "12:00" {
			t.Errorf("slot = %q, want %q", slot, "12:00")
		}
	})

	t.Run("empty schedule falls back to now plus 24h with empty slot", func(t *testing.T) {
		now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 12:00")
		var schedule []string

		nextTime, slot := calculateNextScheduledTimeAt(schedule, now)

		wantTime := now.Add(24 * time.Hour)
		if !nextTime.Equal(wantTime) {
			t.Errorf("nextTime = %v, want %v", nextTime, wantTime)
		}
		if slot != "" {
			t.Errorf("slot = %q, want empty", slot)
		}
	})

	t.Run("returned slot label matches returned time", func(t *testing.T) {
		now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 05:00")
		schedule := []string{"07:30", "09:00"}

		nextTime, slot := calculateNextScheduledTimeAt(schedule, now)

		wantSlot := nextTime.Format("15:04")
		if slot != wantSlot {
			t.Errorf("slot = %q, want it to match nextTime formatted as %q", slot, wantSlot)
		}
		if slot != "07:30" {
			t.Errorf("slot = %q, want %q", slot, "07:30")
		}
	})

	t.Run("malformed entries are skipped", func(t *testing.T) {
		now := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 05:00")
		schedule := []string{"9am", "", "09:00"}

		nextTime, slot := calculateNextScheduledTimeAt(schedule, now)

		wantTime := mustParseLocal(t, "2006-01-02 15:04", "2026-07-18 09:00")
		if !nextTime.Equal(wantTime) {
			t.Errorf("nextTime = %v, want %v", nextTime, wantTime)
		}
		if slot != "09:00" {
			t.Errorf("slot = %q, want %q", slot, "09:00")
		}
	})
}

func TestScheduleRunKey(t *testing.T) {
	e := &NewsEngine{}
	got := e.scheduleRunKey("vix-term", "2026-07-18", "21:35")
	want := "s|vix-term|2026-07-18|21:35"
	if got != want {
		t.Errorf("scheduleRunKey() = %q, want %q", got, want)
	}
}
