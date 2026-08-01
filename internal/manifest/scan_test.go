package manifest

import (
	"errors"
	"syscall"
	"testing"
)

func TestSkipWalkErrorENOTCONN(t *testing.T) {
	err := errors.New("open /root/.fueltronics/drive: transport endpoint is not connected")
	if !skipWalkError(err) {
		t.Fatal("expected skip for ENOTCONN message")
	}
	if !skipWalkError(syscall.ENOTCONN) {
		t.Fatal("expected skip for syscall.ENOTCONN")
	}
	if skipWalkError(errors.New("something fatal")) {
		t.Fatal("should not skip unknown errors")
	}
}
