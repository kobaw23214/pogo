package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

const officeForgottenMaxReads = 3
const officeForgottenReadCooldown = 5 * time.Second

func officeForgottenReadAllowedLocked(d *officeDocumentSession) bool {
	copy := d.recoveryCopy
	if d.closed || !d.recoverySealed || !officeHandoffQuietLocked(d) || !officeRecoveryTargetMatchesLocked(d, copy) || copy.State == "ready" {
		return false
	}
	attempts := copy.ForgottenAttempts
	if attempts == 0 && copy.ForgottenChecked {
		attempts = 1
	}
	if attempts >= officeForgottenMaxReads {
		return false
	}
	if copy.ForgottenLastAt != "" {
		last, err := time.Parse(time.RFC3339Nano, copy.ForgottenLastAt)
		if err != nil || time.Since(last) < officeForgottenReadCooldown {
			return false
		}
	}
	return true
}
func officeCanReadForgottenLocked(d *officeDocumentSession) bool {
	return d.recoveryCopy != nil && (d.recoveryCopy.State == "unknown" || d.recoveryCopy.State == "failed") && officeForgottenReadAllowedLocked(d)
}

// Forgotten files use a different storage namespace from live exports. This
// deliberately does not broaden the normal canonical-export allowlist.
func officeForgottenDocumentPath(path, key, fileType string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 6 && parts[0] == "cache" && parts[1] == "files" && parts[2] == "forgotten" && parts[3] == key && parts[4] == "output."+fileType && parts[5] == parts[4]
}

// This is a one-shot read of a specific engine key, never a force-save retry,
// a global forgotten-file listing or a canonical commit. The recovered bytes
// are an engine snapshot, not proof that offline browser input was uploaded.
func (o *officeService) readForgottenRecovery(ctx context.Context, d *officeDocumentSession, id string) error {
	d.mu.Lock()
	if d.closed || !d.recoverySealed || d.recoveryCopy == nil || d.recoveryCopy.ID != id || !officeRecoveryTargetMatchesLocked(d, d.recoveryCopy) {
		d.mu.Unlock()
		return officeFail(409, "RECOVERY_CHANGED", "恢复目标已变化。")
	}
	if !officeForgottenReadAllowedLocked(d) {
		d.mu.Unlock()
		return nil
	}
	if d.recoveryCopy.ForgottenAttempts == 0 && d.recoveryCopy.ForgottenChecked {
		d.recoveryCopy.ForgottenAttempts = 1
	}
	d.recoveryCopy.ForgottenChecked = true
	d.recoveryCopy.ForgottenAttempts++
	d.recoveryCopy.ForgottenLastAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := o.persistEditorLocked(d); err != nil {
		d.mu.Unlock()
		return err
	}
	key, kind, origin, expectedReceiptHash := d.key, d.fileType, d.browserOrigin, d.recoveryCopy.EngineReceiptHash
	d.mu.Unlock()
	result, err := o.recoveryCommand(ctx, "getForgotten", key, "")
	if err != nil {
		return err
	}
	if result.Key != key || result.URL == "" {
		return officeFail(403, "RECOVERY_DOCUMENT_REJECTED", "引擎恢复回执身份无法核对。")
	}
	source, err := o.exportURL(result.URL, origin)
	if err != nil || !officeForgottenDocumentPath(source.Path, key, kind) {
		return officeFail(403, "RECOVERY_DOCUMENT_REJECTED", "引擎恢复文件不属于当前文档。")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", source.String(), nil)
	if err != nil {
		return err
	}
	response, err := o.cfg.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return officeFail(502, "RECOVERY_FETCH_FAILED", "引擎恢复文件尚未下载完成。")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, officeMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > officeMaxBytes {
		return officeFail(413, "DOCUMENT_LIMIT", "恢复副本超过当前文档大小上限。")
	}
	if err = officeCheckPackage(data, kind); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.recoverySealed || d.recoveryCopy == nil || d.recoveryCopy.ID != id || !officeRecoveryTargetMatchesLocked(d, d.recoveryCopy) {
		return officeFail(409, "RECOVERY_CHANGED", "恢复目标已变化，未替换现有副本。")
	}
	if d.recoveryCopy.State == "ready" {
		return nil
	}
	if d.recoveryCopy.EngineReceiptHash != expectedReceiptHash {
		return officeFail(409, "RECOVERY_CHANGED", "原生保存回执正在完成，未用较早的引擎副本替换。")
	}
	if d.recoveryCopy.Hash != "" && d.recoveryCopy.Hash != officeHash(data) {
		return officeFail(409, "RECOVERY_STORAGE_CHANGED", "已有恢复副本的字节不同，不会覆盖或替换它。")
	}
	d.recoveryCopy.Source, d.recoveryCopy.Coverage = "forgotten", "engine-snapshot"
	d.recoveryCopy.EngineReceiptHash = officeHash([]byte(key + "\x00forgotten\x00" + result.URL))
	return o.storeRecoveryCopyLocked(d, data)
}
