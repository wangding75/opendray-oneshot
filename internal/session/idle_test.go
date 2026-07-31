package session

import (
	"testing"
	"time"
)

func TestRunningSession_BecomesIdleOnce(t *testing.T) {
	rs := &runningSession{lastActivity: time.Now().Add(-time.Minute)}

	if !rs.checkIdle(time.Now(), 30*time.Second) {
		t.Fatal("expected idle transition on first check")
	}
	if rs.checkIdle(time.Now(), 30*time.Second) {
		t.Fatal("idle event must fire only once per idle window")
	}
}

func TestRunningSession_MarkActiveResetsIdle(t *testing.T) {
	rs := &runningSession{
		lastActivity: time.Now().Add(-time.Minute),
		isIdle:       true,
	}
	wasIdle := rs.markActive(time.Now())
	if !wasIdle {
		t.Fatal("markActive must report wasIdle=true on transition")
	}
	if rs.isIdle {
		t.Fatal("isIdle must be false after markActive")
	}
	wasIdle = rs.markActive(time.Now())
	if wasIdle {
		t.Fatal("second markActive must report wasIdle=false")
	}
}

func TestRunningSession_NotYetIdle(t *testing.T) {
	rs := &runningSession{lastActivity: time.Now()}
	if rs.checkIdle(time.Now(), 30*time.Second) {
		t.Fatal("active session must not be reported idle")
	}
}
