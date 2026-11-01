//go:build !linux && !darwin

package web

import "os"

func panelRenameNoReplace(parent *os.File, from, to string) error {
	return officeFail(501, "SAFE_RENAME_UNAVAILABLE", "当前平台尚不支持安全重命名。")
}
