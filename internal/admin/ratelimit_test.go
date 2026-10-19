package admin

import (
	"testing"
	"time"
)

func TestAuthLimiterUsesPeerHostAndExpires(t *testing.T) {
	l := newAuthLimiter()
	now := time.Now()
	for i := 0; i < 30; i++ {
		if !l.allow("192.0.2.1:1", now) {
			t.Fatal("early limit")
		}
	}
	if l.allow("192.0.2.1:2", now) {
		t.Fatal("source port bypass")
	}
	if !l.allow("192.0.2.1:2", now.Add(time.Minute)) {
		t.Fatal("did not expire")
	}
}
