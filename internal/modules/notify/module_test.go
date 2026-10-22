package notify

import "testing"

func TestPackagePresent(t *testing.T) {
	// Channel CRUD and sealed config covered by API tests; package must build.
	var s *Service
	if s != nil {
		t.Fatal("nil")
	}
}
