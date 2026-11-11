package app

// saveUIState 是测试里直接写一份 ui.json（窗口经 saveState 排着号写）。
func saveUIState(home string, s uiState) error {
	return saveUIStateSeq(home, s, uiSaveSeq.Add(1))
}

// diagramsPending 报告还有没有在画的图（离屏预览等它画完再截图）。
func diagramsPending() bool {
	diagrams.Lock()
	defer diagrams.Unlock()
	return diagrams.pending > 0
}
