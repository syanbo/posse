package app

import "slices"

var globalTiles = []string{discBoardID, chatBoardID, taskBoardID, historyBoardID, skillsBoardID, mobileBoardID}

// 旧磁贴只拆一次：全局入口留在导航顺序里，工作区 id 仍记在 pinnedWorkspaces，界面不再显示。
func migrateNavigation(s *uiState) {
	for _, id := range s.TopTiles {
		dst := &s.PinnedWorkspaces
		if slices.Contains(globalTiles, id) {
			dst = &s.NavigationOrder
		}
		if id != "" && !slices.Contains(*dst, id) {
			*dst = append(*dst, id)
		}
	}
	s.TopTiles = nil
}

func (a *app) projectMain(projectID string) string {
	if ws := a.byProject[projectID]; len(ws) > 0 {
		return ws[0].ID
	}
	return ""
}
