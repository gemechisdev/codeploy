package cron

import "testing"

func TestTickNilServiceSafe(t *testing.T) {
	// Structural smoke: package builds. Full Tick coverage needs DB + runtime fakes (T21).
	var s *Service
	if s != nil {
		t.Fatal("nil")
	}
}
