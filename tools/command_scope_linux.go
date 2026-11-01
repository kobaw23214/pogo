//go:build linux

package tools

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// WrapScopedCommand does not alter process-group/PTY attributes, cancellation,
// stdin/stdout or stderr. Call it before starting the command or PTY. Any
// namespace setup error fails the command; there is no weaker fallback.
func WrapScopedCommand(cmd *exec.Cmd, scope *CommandScope) error {
	if cmd == nil || cmd.Process != nil || len(cmd.ExtraFiles) != 0 {
		return scopeError(errors.New("command must be unstarted and must not inherit extra file descriptors"))
	}
	spec, err := normalizedCommandScope(scope, cmd.Dir)
	if err != nil {
		return scopeError(err)
	}
	var bwrap string
	for _, candidate := range []string{"/usr/bin/bwrap", "/bin/bwrap", "/usr/local/bin/bwrap"} {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			bwrap, err = filepath.EvalSymlinks(candidate)
			if err == nil {
				break
			}
		}
	}
	if bwrap == "" {
		return ErrScopedCommandUnavailable
	}
	// /etc is intentionally not mounted wholesale: no passwd/shadow, private
	// TLS keys, user config, signing configuration or service secrets.
	paths := []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt/venv",
		"/etc/ld.so.cache", "/etc/nsswitch.conf", "/etc/hosts", "/etc/resolv.conf",
		"/etc/localtime", "/etc/timezone", "/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/fonts"}
	var binds []scopeBind
	for _, target := range paths {
		if _, err := os.Stat(target); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return scopeError(errors.New("runtime directory is unavailable"))
		}
		source, err := filepath.EvalSymlinks(target)
		if err != nil {
			return scopeError(errors.New("runtime mount cannot be resolved"))
		}
		binds = append(binds, scopeBind{source: source, target: target})
	}
	inner := append([]string(nil), cmd.Args...)
	if len(inner) == 0 {
		inner = []string{cmd.Path}
	}
	if inner[0] == "" {
		return scopeError(errors.New("command executable is required"))
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	spec.env = scopedEnvironment(env, spec.cwd)
	cmd.Path = bwrap
	cmd.Args = append([]string{bwrap}, scopedBwrapArgs(spec, binds, inner)...)
	cmd.Env = append([]string(nil), spec.env...)
	cmd.Dir = spec.workdir
	return nil
}
