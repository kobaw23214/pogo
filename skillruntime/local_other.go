//go:build !linux

package skillruntime

import (
	"context"
	"errors"
)

func NewLocalExecutor(context.Context, string) (*LocalExecutor, error) {
	return nil, currentPlatform().issue
}
func (e *LocalExecutor) ExecuteDetailed(context.Context, Execution) (DetailedResult, error) {
	return DetailedResult{}, errors.New("本地Skill 沙箱不支持当前平台")
}
func RunLocalRunner([]string) int { return 1 }
