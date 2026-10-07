package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestSharedCalendarSpecification(t *testing.T) {
	var spec struct {
		Relative []struct{ Today, Date, Label string }
		Typed    []struct{ Date string }
		Zones    []struct{ Instant, Zone, Date string }
		Repeat   []struct{ Rule, Due, Today, Date string }
		Invalid  []string
	}
	data, err := os.ReadFile("testdata/calendar.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for _, item := range spec.Relative {
		now, err := time.Parse("2006-01-02", item.Today)
		if err != nil {
			t.Fatal(err)
		}
		if got := relativeDate(item.Date, now); got != item.Label {
			t.Errorf("%s from %s: %q want %q", item.Date, item.Today, got, item.Label)
		}
	}
	for _, item := range spec.Zones {
		instant, err := time.Parse(time.RFC3339, item.Instant)
		if err != nil {
			t.Fatal(err)
		}
		zone, err := time.LoadLocation(item.Zone)
		if err != nil {
			t.Fatal(err)
		}
		if got := instant.In(zone).Format("2006-01-02"); got != item.Date {
			t.Errorf("%s in %s: %s", item.Instant, item.Zone, got)
		}
	}
	for _, item := range spec.Repeat {
		if got := nextDueDate(item.Due, item.Rule, item.Today); got != item.Date {
			t.Errorf("%s %s: %s want %s", item.Due, item.Rule, got, item.Date)
		}
	}
	for _, date := range spec.Invalid {
		if _, err = time.Parse("2006-01-02", date); err == nil {
			t.Errorf("accepted %s", date)
		}
	}
	for _, item := range spec.Typed {
		if item.Date != "" {
			if _, err = time.Parse("2006-01-02", item.Date); err != nil {
				t.Fatal(err)
			}
		}
	}
}
