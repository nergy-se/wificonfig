package main

import (
	"testing"
	"time"
)

func TestWantAP(t *testing.T) {
	now := time.Now()
	a := &App{apDelay: 5 * time.Minute, lastStation: now.Add(-time.Minute)}
	if !a.wantAP(0, now) {
		t.Error("without station network the AP is the only way to configure one")
	}
	if a.wantAP(1, now) {
		t.Error("should wait apDelay for the station network to come back")
	}
	if !a.wantAP(1, now.Add(5*time.Minute)) {
		t.Error("should start the AP after apDelay")
	}
}

func TestStationRetryDelay(t *testing.T) {
	a := &App{apRetryInterval: 5 * time.Minute}
	expected := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour}
	for failures, want := range expected {
		a.stationFailures = failures
		if got := a.stationRetryDelay(); got != want {
			t.Errorf("failures %d: expected %s got %s", failures, want, got)
		}
	}
	a.stationFailures = 1000
	if got := a.stationRetryDelay(); got != time.Hour {
		t.Errorf("expected cap of an hour got %s", got)
	}
}
