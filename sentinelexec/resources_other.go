//go:build !linux

package sentinelexec

import "context"

// Production launch already fails closed outside Linux. Non-Linux protocol
// tests replace the launcher and do not claim OS resource isolation.
func monitorResources(ctx context.Context, _ int, _ Config) error {
	<-ctx.Done()
	return nil
}
