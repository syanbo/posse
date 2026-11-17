package editor

// MarkSnapshotSaved 供 Posse 异步保存完成时标记已提交的快照，避免 MarkSaved
// 把保存期间的新编辑也误标为已保存。
func (b *Buffer) MarkSnapshotSaved(s Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saved = s.t
}
