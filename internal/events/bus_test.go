package events

import "testing"

func TestPublishFansOutAndNeverBlocks(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	b.Publish("x", 1)
	if ev := <-ch; ev.Name != "x" {
		t.Fatal("wrong event")
	}
	for i := 0; i < 1000; i++ { // slow subscriber: must drop, not block
		b.Publish("y", i)
	}
	cancel()
	b.Publish("z", 0)
}
