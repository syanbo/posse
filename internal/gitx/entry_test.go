package gitx

// 测试里按暂存区、工作区两边看一项的状态：界面直接看 X、Y。

func (e Entry) Staged() bool { return !e.Untracked && !e.Conflict && e.X != '.' }

func (e Entry) Unstaged() bool { return !e.Untracked && !e.Conflict && e.Y != '.' }
