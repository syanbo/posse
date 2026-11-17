package terminal

import "regexp"

// Posse: 连接在读输出的协程交出状态，不能让界面线程等 SSH 握手。
type stateConn interface {
	TerminalState() (cols, rows int, readOnly bool, err error)
}

// ReadOnly 在握手期间也返回 true，避免尚未取得控制时的操作迟到执行。Posse。
func (t *Terminal) ReadOnly() bool { return t.readOnly.Load() }

// Posse: 只在启动和首次握手时调用；控制转移由服务端断开连接后重新接入。
func (t *Terminal) setReadOnly(readOnly bool) {
	t.in.mu.Lock()
	t.in.blocked = readOnly
	t.readOnly.Store(readOnly)
	t.in.mu.Unlock()
}

func (t *Terminal) readConnState() bool {
	conn, ok := t.src.(stateConn)
	if !ok {
		return true
	}
	cols, rows, readOnly, err := conn.TerminalState()
	if err != nil {
		return false
	}
	// Posse: 整帧到齐后再在同一把锁里设网格、回放；不能让 paint 插到快照分片之间。
	var snapshot []byte
	if source, ok := t.src.(interface{ TerminalSnapshot() ([]byte, error) }); ok {
		if snapshot, err = source.TerminalSnapshot(); err != nil {
			return false
		}
	}
	t.mu.Lock()
	if t.closed || t.term == nil {
		t.mu.Unlock()
		return false
	}
	if cols > 0 && rows > 0 {
		t.size.cols, t.size.rows = cols, rows
		t.term.Resize(cols, rows, t.size.cellW, t.size.cellH)
	}
	t.setReadOnly(t.opts.ReadOnly || readOnly)
	t.term.Write(snapshot)
	ev := t.events
	t.events = events{}
	title := ""
	if ev.title {
		title = t.term.Title()
	}
	t.awaitingState = false
	t.mu.Unlock()
	t.fire(ev, title)
	t.redraw()
	return true
}

// What Posse adds to the terminal's API, kept in a file of its own so that
// upgrading MyGo merges less. (Posse)

// Selection returns the selected text, "" when nothing is selected, for a
// context menu of the app's own (Options.ContextMenu).
func (t *Terminal) Selection() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return ""
	}
	text, ok := t.term.SelectionText()
	if !ok {
		return ""
	}
	return text
}

// PasteClipboard pastes the clipboard as the Paste item of the terminal's
// menu does, through Options.OnPaste, on the main thread.
func (t *Terminal) PasteClipboard() {
	if t.v != nil {
		t.v.paste()
	}
}

// SelectAll selects the whole scrollback and screen, as the Select All
// item of the terminal's menu does.
func (t *Terminal) SelectAll() {
	if t.v != nil {
		t.v.selectAll()
	}
}

// ScrollToEdge scrolls the scrollback to its top, or to the bottom where
// the screen is, for Command-Up and Command-Down.
func (t *Terminal) ScrollToEdge(top bool) {
	if t.v == nil {
		return
	}
	if top {
		t.v.scrollBack(ActionScrollToTop)
	} else {
		t.v.scrollBack(ActionScrollToBottom)
	}
}

// AltScreen reports whether a full-screen program (vim, less) shows its
// alternate screen: its editing keys leave those programs the keys.
func (t *Terminal) AltScreen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.term != nil && t.term.AltScreen()
}

// imageTagPattern is an agent's placeholder for an image pasted into its
// prompt, as Claude Code's "[Image #1]": plain text until the prompt is
// sent, which Options.LinkUsable and LinkClick turn into the image's file.
var imageTagPattern = regexp.MustCompile(`\[Image #\d+\]`)
