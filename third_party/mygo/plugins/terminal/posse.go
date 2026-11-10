package terminal

import "regexp"

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
