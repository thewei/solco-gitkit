package engine

import (
	"os"
	"testing"
)

// Git runs this test binary as its editor during a rewrite (RunDaemon).
func TestMain(m *testing.M) {
	if RunDaemon() {
		return
	}
	os.Exit(m.Run())
}
