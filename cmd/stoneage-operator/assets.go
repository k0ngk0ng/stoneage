package main

import (
	"errors"
	"time"
)

func (value *operator) startAssetSync() error {
	value.assetSyncMu.Lock()
	if value.assetSync.Phase == "running" {
		value.assetSyncMu.Unlock()
		return errors.New("资源同步任务正在运行，请稍后刷新")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	value.assetSync = assetSyncStatus{Phase: "running", StartedAt: now, UpdatedAt: now, Message: "正在把 assets、maps、audio 批量同步到 OSS"}
	value.assetSyncMu.Unlock()

	// Do not hold the operator request open while a complete 2.5 client data
	// tree is uploaded. The fixed script and its fixed source paths are the
	// security boundary; the admin only starts this asynchronous job.
	go func() {
		err := value.runAssetSyncScript(30 * time.Minute)
		value.assetSyncMu.Lock()
		defer value.assetSyncMu.Unlock()
		updated := time.Now().UTC().Format(time.RFC3339)
		value.assetSync.UpdatedAt = updated
		value.assetSync.Phase = "succeeded"
		value.assetSync.Message = "资源已同步到 OSS"
		if err != nil {
			value.assetSync.Phase = "failed"
			value.assetSync.Message = err.Error()
		}
	}()
	return nil
}

func (value *operator) assetSyncStatus() assetSyncStatus {
	value.assetSyncMu.Lock()
	defer value.assetSyncMu.Unlock()
	if value.assetSync.Phase == "" {
		return assetSyncStatus{Phase: "idle", Message: "尚未执行资源同步"}
	}
	return value.assetSync
}
