# Drive web pages in the Posse window (posse web)

Posse's web tabs are real WebKit pages inside the Posse window, next to the terminals. Use them to check a dev server, fill a form or read a page the user is looking at. They need the Posse window to be open; if a command says the window is not open, ask the user to open Posse instead of retrying.

Commands act on the web tabs of your workspace (the one this terminal is in). With several tabs, they use the one shown most recently; `--tab N` picks another (numbers come from `posse web open` and `posse web list`). Every command takes `--json`, `--workspace W` and `--timeout D` (default 30s).

## Typical loop

```bash
posse web open http://localhost:3000      # prints the tab number
posse web wait                            # until the page has loaded
posse web snapshot                        # interactive elements with refs like [e3]
posse web fill e3 "alice@example.com"
posse web click e5
posse web wait --for text "Welcome"
posse web screenshot                      # prints a PNG path; read the image to see the page
```

Take a fresh `snapshot` after the page changes: refs stay on the elements they were given to, new elements get new refs, and a new page starts over.

## Commands

| Command | What it does |
|---|---|
| `open <url>` | Open a new web tab in your workspace and show it (keyboard stays where the user is). `localhost:3000` is enough. A local HTML file path (relative or absolute) opens the file; pages it loads from its own folder and below work. |
| `list` | Your workspace's web tabs; `*` marks the one used without `--tab`. |
| `goto <url>`, `reload`, `back`, `forward` | Navigate the tab. They return at once; follow with `wait`. |
| `wait [--for load\|selector <css>\|text <s>]` | Wait until the page has loaded, a visible element matches, or the text appears. Exit code 3 on timeout. |
| `snapshot` | Visible interactive elements, headings and landmarks as an indented list: `[ref] role "name" value=… checked disabled focused -> href`. |
| `click <ref\|css>` | Scroll into view and click (pointer and mouse events, then click). |
| `fill <ref\|css> <text>` | Set a text field, textarea, contenteditable or select (by option value or text), firing input and change. |
| `press <key> [--on <ref\|css>]` | Key on the focused element: Enter, Tab, Escape, Space, Backspace, arrows, or one character. |
| `eval <js>` (`-` reads stdin) | Run JavaScript as the body of an async function; `return` a value to get it back as JSON. |
| `screenshot [-o path]` | PNG of the visible part of the page, saved to a temp file unless `-o`. |
| `console` | The page's last 200 console messages and uncaught errors since it loaded. |
| `device <iphone\|se\|pixel\|ipad\|off> [--landscape]` | Show the page at a phone's (or iPad's) CSS size with its user agent, and reload it; `screenshot` then captures that size. `off` goes back to desktop. Follow with `wait`. |

## Limits

- Keys and clicks are synthetic DOM events. Enter submits the form of a focused input, Tab moves focus, Space toggles checkboxes and presses buttons, Enter on links and buttons clicks them; other keys only reach the page's own handlers (no text is typed: use `fill`).
- Only the top frame: elements and console output inside iframes are not seen. Closed shadow roots are not seen.
- `open` makes the tab the current one of your workspace's file tab, but the window only shows the workspace selected in its sidebar. A tab that has never been on screen cannot be captured (`screenshot` says so); the other commands work.
- Pages run with the user's cookies of the Posse window, not those of their browser.
- `device` sets the width and the user agent only: no touch events, safe areas or device pixel ratio. Sites that pick the mobile layout by `ontouchstart` will still look like desktop.
