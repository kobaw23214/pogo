//go:build !unix

package tools

import (
	"context"
	"errors"
	"os"
)

func AdoptSubagentEditLease() error {
	_, edit := os.LookupEnv(EnvSubagentEditLeaseFD)
	_, retained := os.LookupEnv(envSubagentContextLeaseFD)
	if !edit && !retained {
		return nil
	}
	_ = os.Unsetenv(EnvSubagentEditLeaseFD)
	_ = os.Unsetenv(envSubagentContextLeaseFD)
	return errors.New("inherited writable delegation leases require Unix")
}

func acquireEditLease(context.Context) (*os.File, error) {
	return nil, errors.New("writable delegation requires host-wide file locking on a Unix platform; use explore or perform the work in the main conversation")
}
