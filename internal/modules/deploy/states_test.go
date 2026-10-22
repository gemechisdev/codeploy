package deploy

import "testing"

func TestTerminalAndRunning(t *testing.T) {
	for _, s := range []string{StSucceeded, StFailed, StCancelled, StInterrupted, StRolledBack} {
		if !Terminal(s) {
			t.Errorf("%s should be terminal", s)
		}
		if Running(s) {
			t.Errorf("%s should not be running", s)
		}
		if Cancellable(s) {
			t.Errorf("%s should not be cancellable", s)
		}
	}
	for _, s := range []string{StPreparing, StBuilding, StStarting, StVerifying, StSwitching, StObserving} {
		if !Running(s) {
			t.Errorf("%s should be running", s)
		}
		if Terminal(s) {
			t.Errorf("%s should not be terminal", s)
		}
	}
	if !Cancellable(StQueued) || !Cancellable(StBuilding) {
		t.Fatal("queued/building must be cancellable")
	}
	if Cancellable(StSwitching) || Cancellable(StObserving) {
		t.Fatal("after switch, cancel is not offered")
	}
	if !InProgress(StQueued) || !InProgress(StObserving) || InProgress(StSucceeded) {
		t.Fatal("InProgress matrix wrong")
	}
}
