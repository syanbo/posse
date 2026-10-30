package app

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文件按类型的图标，照 WebStorm 新界面的项目树：每类一个线条图形，配这一类的颜色。
// mygo 的 SVG 不画 <text>，TS、JS、GO 的字母用路径画。
const (
	pageShape   = `<path d="M6.75 3.75h6.5l4 4v11.5c0 .55-.45 1-1 1h-9.5c-.55 0-1-.45-1-1V4.75c0-.55.45-1 1-1z"/><path d="M13.25 3.75v4h4"/>`
	letterS     = `M20.25 8.5c-.55-.95-1.6-1.5-2.9-1.5-1.8 0-3.1.95-3.1 2.4 0 3.35 6.25 1.75 6.25 5.05 0 1.5-1.35 2.55-3.2 2.55-1.4 0-2.55-.6-3.1-1.6`
	strokeBold  = `stroke-width="1.75"`
	letterGroup = `<g ` + strokeBold + `>`
)

var (
	iconFileTS   = icon(letterGroup + `<path d="M3.25 7h8M7.25 7v10"/><path d="` + letterS + `"/></g>`)
	iconFileJS   = icon(letterGroup + `<path d="M9.5 7v7.25a2.75 2.75 0 0 1-5.5 0"/><path d="` + letterS + `"/></g>`)
	iconFileGo   = icon(letterGroup + `<path d="M10.25 8.4A4.25 5 0 1 0 11.25 12H7.75"/><ellipse cx="17.25" cy="12" rx="3.75" ry="5"/></g>`)
	iconFileMark = icon(`<path d="m9 6.5-5.5 5.5L9 17.5M15 6.5l5.5 5.5-5.5 5.5"/>`)
	iconFileCSS  = icon(`<path d="M10.5 4.5 8.5 19.5M16 4.5l-2 15M5 9.25h14.5M4.5 14.75H19"/>`)
	iconFileMD   = icon(`<path d="M3.25 17V7l4.5 5.5L12.25 7v10M17.25 7v10M14 13.75l3.25 3.25 3.25-3.25"/>`)
	iconFileText = icon(pageShape + `<path d="M9 12h6M9 15h6M9 18h3.5"/>`)
	iconFileVid  = icon(`<rect x="3.75" y="5" width="16.5" height="14" rx="2.5"/><path d="M10.25 9.25v5.5l4.5-2.75z"/>`)
	iconFileAud  = icon(`<path d="M9.5 17.25V6.25l9-2v11"/><circle cx="7.25" cy="17.25" r="2.25"/><circle cx="16.25" cy="15.25" r="2.25"/>`)
	iconFileGrid = icon(`<rect x="4" y="4.5" width="16" height="15" rx="2"/><path d="M4 9.5h16M4 14.5h16M10 9.5v10"/>`)
	iconFileDB   = icon(`<ellipse cx="12" cy="6.5" rx="7" ry="2.75"/><path d="M5 6.5v11c0 1.5 3.1 2.75 7 2.75s7-1.25 7-2.75v-11M5 12c0 1.5 3.1 2.75 7 2.75S19 13.5 19 12"/>`)
	iconFileZip  = icon(`<rect x="5" y="3.75" width="14" height="16.5" rx="2"/><path d="M12 3.75v1.5M12 7v1.5M12 10.25v1.5"/><rect x="10.25" y="13.5" width="3.5" height="3.75" rx="1"/>`)
	iconFilePDF  = icon(pageShape + `<path d="M9.5 18v-6h2.25a1.75 1.75 0 0 1 0 3.5H9.5"/>`)
	iconFileLock = icon(`<rect x="5" y="10.5" width="14" height="9.75" rx="2"/><path d="M8.25 10.5V8a3.75 3.75 0 0 1 7.5 0v2.5M12 14.25v2.25"/>`)
	iconFileBox  = icon(`<path d="M3.5 12.5h17c-.5 4.25-3.9 7.25-8.5 7.25s-8-3-8.5-7.25z"/><path d="M6 12.5v-4h9v4M9 8.5v4M12 8.5v4M9 8.5v-4h3v4"/>`)
)

type fileKind int

const (
	kindOther fileKind = iota
	kindTS
	kindJS
	kindGo
	kindCode
	kindShell
	kindMarkup
	kindStyle
	kindData
	kindMarkdown
	kindText
	kindImage
	kindVideo
	kindAudio
	kindTable
	kindDatabase
	kindArchive
	kindPDF
	kindLock
	kindGit
	kindDocker
)

// 颜色是 palette.data 的下标（橙、蓝、绿、紫、金、青），-1 是次要色。
var fileKinds = [...]struct {
	icon  *ui.SVG
	color int
}{
	kindOther:    {iconFile, -1},
	kindTS:       {iconFileTS, 1},
	kindJS:       {iconFileJS, 4},
	kindGo:       {iconFileGo, 5},
	kindCode:     {iconCode, 1},
	kindShell:    {iconTerminal, 2},
	kindMarkup:   {iconFileMark, 0},
	kindStyle:    {iconFileCSS, 3},
	kindData:     {iconBraces, 4},
	kindMarkdown: {iconFileMD, 1},
	kindText:     {iconFileText, -1},
	kindImage:    {iconImage, 2},
	kindVideo:    {iconFileVid, 3},
	kindAudio:    {iconFileAud, 3},
	kindTable:    {iconFileGrid, 2},
	kindDatabase: {iconFileDB, 5},
	kindArchive:  {iconFileZip, 4},
	kindPDF:      {iconFilePDF, 0},
	kindLock:     {iconFileLock, -1},
	kindGit:      {iconBranch, 0},
	kindDocker:   {iconFileBox, 1},
}

// 完整文件名先于扩展名：package-lock.json 是锁文件，不是 JSON。
var kindByName = map[string]fileKind{
	"makefile": kindShell, "gnumakefile": kindShell, "justfile": kindShell,
	".bashrc": kindShell, ".zshrc": kindShell, ".profile": kindShell, ".zprofile": kindShell,
	"dockerfile": kindDocker, "containerfile": kindDocker, ".dockerignore": kindDocker,
	"compose.yaml": kindDocker, "compose.yml": kindDocker, "docker-compose.yaml": kindDocker, "docker-compose.yml": kindDocker,
	"go.sum": kindLock, "package-lock.json": kindLock, "pnpm-lock.yaml": kindLock, "bun.lockb": kindLock,
	"license": kindText, "licence": kindText, "copying": kindText, "notice": kindText,
	".gitignore": kindGit, ".gitattributes": kindGit, ".gitmodules": kindGit, ".gitkeep": kindGit,
	".env": kindData, ".editorconfig": kindData, ".npmrc": kindData,
}

var kindByExt = map[string]fileKind{
	".ts": kindTS, ".tsx": kindTS, ".mts": kindTS, ".cts": kindTS,
	".js": kindJS, ".jsx": kindJS, ".mjs": kindJS, ".cjs": kindJS,
	".go": kindGo,
	".py": kindCode, ".rs": kindCode, ".swift": kindCode, ".dart": kindCode, ".c": kindCode, ".h": kindCode,
	".cc": kindCode, ".cpp": kindCode, ".hpp": kindCode, ".m": kindCode, ".mm": kindCode, ".java": kindCode,
	".kt": kindCode, ".kts": kindCode, ".scala": kindCode, ".rb": kindCode, ".php": kindCode, ".lua": kindCode,
	".zig": kindCode, ".cs": kindCode, ".ex": kindCode, ".exs": kindCode, ".hs": kindCode, ".ml": kindCode,
	".r": kindCode, ".proto": kindCode, ".graphql": kindCode, ".gql": kindCode, ".pl": kindCode, ".groovy": kindCode, ".gradle": kindCode,
	".sh": kindShell, ".bash": kindShell, ".zsh": kindShell, ".fish": kindShell, ".ps1": kindShell, ".mk": kindShell,
	".html": kindMarkup, ".htm": kindMarkup, ".xml": kindMarkup, ".vue": kindMarkup, ".svelte": kindMarkup, ".astro": kindMarkup,
	".css": kindStyle, ".scss": kindStyle, ".sass": kindStyle, ".less": kindStyle,
	".json": kindData, ".jsonc": kindData, ".json5": kindData, ".yaml": kindData, ".yml": kindData, ".toml": kindData,
	".ini": kindData, ".conf": kindData, ".cfg": kindData, ".plist": kindData, ".env": kindData, ".mod": kindData,
	".md": kindMarkdown, ".markdown": kindMarkdown, ".mdx": kindMarkdown,
	".txt": kindText, ".log": kindText, ".rst": kindText,
	".png": kindImage, ".jpg": kindImage, ".jpeg": kindImage, ".gif": kindImage, ".webp": kindImage,
	".svg": kindImage, ".ico": kindImage, ".heic": kindImage, ".icns": kindImage, ".bmp": kindImage, ".tiff": kindImage,
	".mp4": kindVideo, ".mov": kindVideo, ".webm": kindVideo, ".mkv": kindVideo, ".avi": kindVideo,
	".mp3": kindAudio, ".wav": kindAudio, ".m4a": kindAudio, ".flac": kindAudio, ".aac": kindAudio, ".ogg": kindAudio,
	".csv": kindTable, ".tsv": kindTable, ".xlsx": kindTable, ".xls": kindTable, ".numbers": kindTable,
	".sql": kindDatabase, ".db": kindDatabase, ".sqlite": kindDatabase, ".sqlite3": kindDatabase,
	".zip": kindArchive, ".tar": kindArchive, ".gz": kindArchive, ".tgz": kindArchive, ".bz2": kindArchive,
	".xz": kindArchive, ".7z": kindArchive, ".rar": kindArchive, ".dmg": kindArchive, ".jar": kindArchive,
	".pdf":  kindPDF,
	".lock": kindLock,
}

// kindOf 按文件名认类型，name 可以带目录。
func kindOf(name string) fileKind {
	base := strings.ToLower(filepath.Base(name))
	if k, ok := kindByName[base]; ok {
		return k
	}
	switch {
	case strings.HasPrefix(base, "dockerfile."), strings.HasSuffix(base, ".dockerfile"):
		return kindDocker
	case (strings.HasPrefix(base, "compose.") || strings.HasPrefix(base, "docker-compose.")) &&
		(strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")):
		return kindDocker
	case strings.HasPrefix(base, ".env."):
		return kindData
	}
	return kindByExt[filepath.Ext(base)]
}

// fileIcon 是 name 这类文件的图标和颜色。
func fileIcon(t *ui.Theme, name string) (*ui.SVG, ui.Color) {
	k := fileKinds[kindOf(name)]
	if k.color < 0 {
		return k.icon, t.TextMuted
	}
	return k.icon, colors(t).data[k.color]
}

// fileGlyph 画 name 这类文件的图标，颜色由类型定。
func fileGlyph(c *ui.Context, name string, size float32) ui.Element {
	svg, color := fileIcon(c.Theme(), name)
	return ui.Icon(c, svg).FontSize(size).TextColor(color).Shrink(0)
}
