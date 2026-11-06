package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const envHostPrivateDirs = "POGO_HOST_PRIVATE_DIRS"

var hostPrivateDirs = struct {
	sync.Mutex
	paths map[string]int
}{paths: map[string]int{}}

// RegisterHostPrivateDirs adds process-owned control stores to true filesystem
// masking. Registration is not model configuration; it must precede tool use.
// The returned release is called only after all Manager sessions have drained.
func RegisterHostPrivateDirs(dirs []string) func() {
	paths := make([]string, 0, len(dirs))
	hostPrivateDirs.Lock()
	for _, dir := range dirs {
		if absolute, err := filepath.Abs(dir); err == nil && filepath.Dir(absolute) != absolute {
			paths = append(paths, absolute)
			hostPrivateDirs.paths[absolute]++
		}
	}
	hostPrivateDirs.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			hostPrivateDirs.Lock()
			defer hostPrivateDirs.Unlock()
			for _, p := range paths {
				hostPrivateDirs.paths[p]--
				if hostPrivateDirs.paths[p] <= 0 {
					delete(hostPrivateDirs.paths, p)
				}
			}
		})
	}
}

func registeredHostPrivateDirs() []string {
	paths := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv(envHostPrivateDirs)) {
		if strings.TrimSpace(dir) != "" {
			paths[dir] = true
		}
	}
	hostPrivateDirs.Lock()
	for dir := range hostPrivateDirs.paths {
		paths[dir] = true
	}
	hostPrivateDirs.Unlock()
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func hostPrivateChildEnv(env []string) []string {
	paths := registeredHostPrivateDirs()
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !isHostControlSecretEnv(name) && !strings.HasPrefix(entry, envHostPrivateDirs+"=") {
			out = append(out, entry)
		}
	}
	if len(paths) > 0 {
		out = append(out, envHostPrivateDirs+"="+strings.Join(paths, string(os.PathListSeparator)))
	}
	return out
}
