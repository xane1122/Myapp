package api

import (
	"testing"
	"time"
)

func TestScreenPeekCaptureCanOnlyBeConsumedOnce(t *testing.T) {
	resetScreenPeekTestState()
	started := time.Now().UTC().Add(-time.Minute)
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{
		Path:       "/one-use.png",
		MIMEType:   "image/png",
		CapturedAt: started.Add(time.Second),
	}
	screenPeekState.Unlock()

	first, ok := takeScreenShotAfter(started)
	if !ok || first.Path != "/one-use.png" {
		t.Fatalf("fresh capture was not consumed: ok=%v shot=%#v", ok, first)
	}
	if second, ok := takeScreenShotAfter(started); ok || second.Path != "" {
		t.Fatalf("capture was reused: ok=%v shot=%#v", ok, second)
	}
}
