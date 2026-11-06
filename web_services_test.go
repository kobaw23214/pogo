package main

import (
	"strings"
	"testing"
)

func TestManagedWebServicesDefaultOff(t *testing.T) {
	t.Setenv("POGO_WEB_SERVICE_ENABLED", "")
	t.Setenv("POGO_WEB_SERVICE_RUNNER_TOKEN", "must-not-enable-a-runtime")
	manager, dirs, cleanup, err := managedWebServicesFromEnv(nil)
	if err != nil || manager != nil || len(dirs) != 0 || cleanup == nil {
		t.Fatalf("default enabled optional runtime: %v %v %v", manager, dirs, err)
	}
	cleanup()
}

func TestManagedWebServicesRejectPartialConfigurationWithoutLeakingToken(t *testing.T) {
	t.Setenv("POGO_WEB_SERVICE_ENABLED", "1")
	t.Setenv("POGO_WEB_SERVICE_ROOT", "")
	t.Setenv("POGO_WEB_SERVICE_RUNNER_TOKEN", "private-runner-token-must-not-appear")
	_, _, _, err := managedWebServicesFromEnv(nil)
	if err == nil || strings.Contains(err.Error(), "private-runner-token") {
		t.Fatalf("invalid configuration error: %v", err)
	}
}
