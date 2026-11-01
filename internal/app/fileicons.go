package app

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文件按类型的图标，照 WebStorm 新界面的项目树：每类一个线条图形，配这一类的颜色。
// mygo 的 SVG 不画 <text>，TS、JS、GO 的字母用路径画。
const (
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
	iconFileText = icon(fileShape + `<path d="M9 12h6M9 15h6M9 18h3.5"/>`)
	iconFileVid  = icon(`<rect x="3.75" y="5" width="16.5" height="14" rx="2.5"/><path d="M10.25 9.25v5.5l4.5-2.75z"/>`)
	iconFileAud  = icon(`<path d="M9.5 17.25V6.25l9-2v11"/><circle cx="7.25" cy="17.25" r="2.25"/><circle cx="16.25" cy="15.25" r="2.25"/>`)
	iconFileGrid = icon(`<rect x="4" y="4.5" width="16" height="15" rx="2"/><path d="M4 9.5h16M4 14.5h16M10 9.5v10"/>`)
	iconFileDB   = icon(`<ellipse cx="12" cy="6.5" rx="7" ry="2.75"/><path d="M5 6.5v11c0 1.5 3.1 2.75 7 2.75s7-1.25 7-2.75v-11M5 12c0 1.5 3.1 2.75 7 2.75S19 13.5 19 12"/>`)
	iconFileZip  = icon(`<rect x="5" y="3.75" width="14" height="16.5" rx="2"/><path d="M12 3.75v1.5M12 7v1.5M12 10.25v1.5"/><rect x="10.25" y="13.5" width="3.5" height="3.75" rx="1"/>`)
	iconFilePDF  = icon(fileShape + `<path d="M9.5 18v-6h2.25a1.75 1.75 0 0 1 0 3.5H9.5"/>`)
	iconFileLock = icon(`<rect x="5" y="10.5" width="14" height="9.75" rx="2"/><path d="M8.25 10.5V8a3.75 3.75 0 0 1 7.5 0v2.5M12 14.25v2.25"/>`)
	// Dart 用 Android Studio（Dart 插件 expui/dart.svg）的实心标志：深浅两块蓝改成同一颜色的两档透明度；原图几乎占满 16 格，四周各留 2 才和别的图标一样大
	iconFileDart    = svgOf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="-2 -2 20 20"><path fill="currentColor" fill-opacity="0.55" fill-rule="evenodd" d="M3.37928 10.2715L3.37328 3.37354L1.19128 7.92554C0.936284 8.46854 0.828284 9.13254 1.57328 9.87654L3.71128 12.0145C3.33228 11.6355 3.38428 11.0175 3.37928 10.2715ZM12.5258 4.22274L12.0148 3.71074C11.6358 3.33174 11.0178 3.38374 10.2718 3.37874L3.37328 3.37354L12 12H15L15.0008 6.69674L12.5258 4.22274Z"/><path fill="currentColor" d="M3.37402 3.37297L10.271 3.37897C10.3587 3.37956 10.4445 3.37936 10.5285 3.37916C11.1591 3.3777 11.6805 3.37649 12.015 3.71097L9.87702 1.57297C9.13302 0.82897 8.46902 0.935969 7.92502 1.19097L3.37402 3.37297Z"/><path fill="currentColor" d="M12 12L3.37402 3.37297L3.38002 10.271C3.38061 10.3587 3.38041 10.4446 3.38022 10.5285C3.37875 11.1591 3.37754 11.6805 3.71202 12.014L4.22168 12.5259L6.69568 14.9999L12 15V12Z"/></svg>`)
	iconFileFlutter = icon(`<path d="M14.25 3.25h5.5L8.5 14.5 5.75 11.75z"/><path d="M14.25 11.25h5.5l-5 5 5 5h-5.5l-5-5z"/>`)
	iconFileSwift   = icon(`<path d="M19.25 15.5c1.1-3.6-.6-7.8-5.25-11 2.4 3.4 2.6 6.4 1.65 8.4C12.6 10.8 8.4 7.6 5 4.75c2.6 3.3 5 5.75 7.1 7.4-2.75-1.25-5.1-2.6-7.6-4.4 2.9 4.8 7.6 9.5 13.75 9 .8.55 1.4 1.3 1.75 2.25.35-1.35.15-2.5-.75-3.5z"/>`)
	iconFileKotlin  = icon(`<path d="M5 5h14l-7 7 7 7H5z"/>`)
	iconFileVue     = icon(`<path d="M2.75 4.5h5.5L12 11l3.75-6.5h5.5L12 20.25z"/><path d="M8.25 4.5h7.5"/>`)
	iconFileDroid   = icon(`<path d="M4.5 17.5v-2.25a7.5 7.5 0 0 1 15 0v2.25z"/><path d="M7.5 9.25 5.75 6.5M16.5 9.25l1.75-2.75"/><circle cx="9.25" cy="13.75" r="1" fill="currentColor" stroke="none"/><circle cx="14.75" cy="13.75" r="1" fill="currentColor" stroke="none"/>`)
	iconFileGradle  = icon(`<path d="M14.75 4.5a4.5 4.5 0 0 0-5.6 5.6L4.6 14.65a2.12 2.12 0 0 0 3 3l4.55-4.55a4.5 4.5 0 0 0 5.6-5.6l-2.6 2.6-2.4-.6-.6-2.4z"/>`)
	iconFileLayout  = icon(`<rect x="3.75" y="4.5" width="16.5" height="15" rx="2.5"/><path d="M3.75 9h16.5M9.5 9v10.5"/>`)
	iconFileKey     = icon(`<circle cx="8" cy="15.5" r="3.75"/><path d="m10.75 12.75 8.5-8.5M15.75 7.75l2.5 2.5M13.5 10l2 2"/>`)
	// C 头文件、Objective-C 照 Xcode：纸上一个小写字母
	iconFileH = icon(fileShape + `<path d="M10 10.5v7.5M10 14.75a2.25 2.25 0 0 1 4.5 0V18"/>`)
	iconFileM = icon(fileShape + `<path d="M8.25 13v5M8.25 14.5a1.75 1.75 0 0 1 3.5 0V18M11.75 14.5a1.75 1.75 0 0 1 3.5 0V18"/>`)
	// Xcode 的包目录：工程、工作区、资源目录、图标集、本地化
	iconDirXcode     = icon(`<rect x="3.75" y="3.75" width="16.5" height="16.5" rx="4"/><path d="m8 16.5 6-6"/><path d="m12.25 8.75 1.75-1.75 3 3-1.75 1.75z"/>`)
	iconDirWorkspace = icon(`<rect x="7.75" y="3.75" width="12.5" height="12.5" rx="3"/><path d="M16.25 20.25h-9.5a3 3 0 0 1-3-3v-9.5"/>`)
	iconDirAssets    = icon(`<rect x="4" y="4" width="7" height="7" rx="1.75"/><rect x="13" y="4" width="7" height="7" rx="1.75"/><rect x="4" y="13" width="7" height="7" rx="1.75"/><rect x="13" y="13" width="7" height="7" rx="1.75"/>`)
	iconDirAppIcon   = icon(`<rect x="4.5" y="4.5" width="15" height="15" rx="4.25"/><path d="m12 8.5 1 2.25 2.5.25-1.9 1.65.6 2.45L12 13.8l-2.2 1.3.6-2.45L8.5 11l2.5-.25z"/>`)
	iconDirBundle    = icon(`<path d="M12 3.5 19.5 7.75v8.5L12 20.5l-7.5-4.25v-8.5z"/><path d="M4.5 7.75 12 12l7.5-4.25M12 12v8.5"/>`)
	iconFileBox      = icon(`<path d="M3.5 12.5h17c-.5 4.25-3.9 7.25-8.5 7.25s-8-3-8.5-7.25z"/><path d="M6 12.5v-4h9v4M9 8.5v4M12 8.5v4M9 8.5v-4h3v4"/>`)
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
	kindDart
	kindFlutter
	kindSwift
	kindKotlin
	kindJava
	kindVue
	kindAndroid
	kindGradle
	kindLayout
	kindKey
	kindHeader
	kindObjC
	kindXcodeProj
	kindXcodeWorkspace
	kindAssets
	kindAppIcon
	kindLocale
	kindBundle
)

// 颜色是 palette.data 的下标（橙、蓝、绿、紫、金、青），-1 是次要色。
var fileKinds = [...]struct {
	icon  *ui.SVG
	color int
}{
	kindOther:          {iconFile, -1},
	kindTS:             {iconFileTS, 1},
	kindJS:             {iconFileJS, 4},
	kindGo:             {iconFileGo, 5},
	kindCode:           {iconCode, 1},
	kindShell:          {iconTerminal, 2},
	kindMarkup:         {iconFileMark, 0},
	kindStyle:          {iconFileCSS, 3},
	kindData:           {iconBraces, 4},
	kindMarkdown:       {iconFileMD, 1},
	kindText:           {iconFileText, -1},
	kindImage:          {iconImage, 2},
	kindVideo:          {iconFileVid, 3},
	kindAudio:          {iconFileAud, 3},
	kindTable:          {iconFileGrid, 2},
	kindDatabase:       {iconFileDB, 5},
	kindArchive:        {iconFileZip, 4},
	kindPDF:            {iconFilePDF, 0},
	kindLock:           {iconFileLock, -1},
	kindGit:            {iconBranch, 0},
	kindDocker:         {iconFileBox, 1},
	kindDart:           {iconFileDart, 1},
	kindFlutter:        {iconFileFlutter, 1},
	kindSwift:          {iconFileSwift, 0},
	kindKotlin:         {iconFileKotlin, 3},
	kindJava:           {iconCoffee, 0},
	kindVue:            {iconFileVue, 2},
	kindAndroid:        {iconFileDroid, 2},
	kindGradle:         {iconFileGradle, 5},
	kindLayout:         {iconFileLayout, 1},
	kindKey:            {iconFileKey, 4},
	kindHeader:         {iconFileH, 0},
	kindObjC:           {iconFileM, 1},
	kindXcodeProj:      {iconDirXcode, 1},
	kindXcodeWorkspace: {iconDirWorkspace, 1},
	kindAssets:         {iconDirAssets, 2},
	kindAppIcon:        {iconDirAppIcon, 2},
	kindLocale:         {iconGlobe, 1},
	kindBundle:         {iconDirBundle, 4},
}

// 完整文件名先于扩展名：package-lock.json 是锁文件，不是 JSON。
var kindByName = map[string]fileKind{
	"makefile": kindShell, "gnumakefile": kindShell, "justfile": kindShell,
	".bashrc": kindShell, ".zshrc": kindShell, ".profile": kindShell, ".zprofile": kindShell,
	"dockerfile": kindDocker, "containerfile": kindDocker, ".dockerignore": kindDocker,
	"go.sum": kindLock, "package-lock.json": kindLock, "pnpm-lock.yaml": kindLock, "bun.lockb": kindLock,
	"license": kindText, "licence": kindText, "copying": kindText, "notice": kindText,
	".gitignore": kindGit, ".gitattributes": kindGit, ".gitmodules": kindGit, ".gitkeep": kindGit,
	".editorconfig": kindData, ".npmrc": kindData,
	// Flutter、Android、iOS 工程里的固定文件
	"pubspec.yaml": kindFlutter, "analysis_options.yaml": kindFlutter, ".metadata": kindFlutter,
	"androidmanifest.xml": kindAndroid, "gradlew": kindGradle, "gradlew.bat": kindGradle, "gradle.properties": kindGradle, "gradle-wrapper.properties": kindGradle,
	"podfile": kindCode, "gemfile": kindCode, "fastfile": kindCode, "appfile": kindCode, "matchfile": kindCode,
}

var kindByExt = map[string]fileKind{
	".ts": kindTS, ".tsx": kindTS, ".mts": kindTS, ".cts": kindTS,
	".js": kindJS, ".jsx": kindJS, ".mjs": kindJS, ".cjs": kindJS,
	".go": kindGo,
	".py": kindCode, ".rs": kindCode, ".c": kindCode,
	".cc": kindCode, ".cpp": kindCode,
	".scala": kindCode, ".rb": kindCode, ".php": kindCode, ".lua": kindCode,
	".zig": kindCode, ".cs": kindCode, ".ex": kindCode, ".exs": kindCode, ".hs": kindCode, ".ml": kindCode,
	".r": kindCode, ".proto": kindCode, ".graphql": kindCode, ".gql": kindCode, ".pl": kindCode, ".groovy": kindCode,
	".sh": kindShell, ".bash": kindShell, ".zsh": kindShell, ".fish": kindShell, ".ps1": kindShell, ".mk": kindShell,
	".html": kindMarkup, ".htm": kindMarkup, ".xml": kindMarkup, ".svelte": kindMarkup, ".astro": kindMarkup,
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
	".pdf": kindPDF, ".dart": kindDart, ".arb": kindData,
	".h": kindHeader, ".hpp": kindHeader, ".m": kindObjC, ".mm": kindObjC,
	".swift": kindSwift, ".java": kindJava, ".kt": kindKotlin, ".kts": kindKotlin, ".vue": kindVue,
	".apk": kindAndroid, ".aab": kindAndroid, ".gradle": kindGradle,
	".storyboard": kindLayout, ".xib": kindLayout,
	".xcconfig": kindData, ".entitlements": kindData, ".pbxproj": kindData, ".xcscheme": kindData, ".xcworkspacedata": kindData, ".xcsettings": kindData, ".strings": kindData, ".xcstrings": kindData,
	".ipa": kindArchive, ".aar": kindArchive,
	".jks": kindKey, ".keystore": kindKey, ".p12": kindKey, ".pem": kindKey, ".cer": kindKey, ".mobileprovision": kindKey,
	".lock": kindLock,
}

// kindOf 按文件名认类型，name 可以带目录。
func kindOf(name string) fileKind {
	base := strings.ToLower(filepath.Base(name))
	if k, ok := kindByName[base]; ok {
		return k
	}
	for _, r := range kindByAffix {
		if strings.HasPrefix(base, r.prefix) && strings.HasSuffix(base, r.suffix) {
			return r.kind
		}
	}
	return kindByExt[filepath.Ext(base)]
}

// kindByAffix 是按名字前后缀认的类型（两头都要对上，空的不限），在完整文件名之后、扩展名之前查。
var kindByAffix = []struct {
	prefix, suffix string
	kind           fileKind
}{
	{"dockerfile.", "", kindDocker}, {"", ".dockerfile", kindDocker},
	{"compose.", ".yaml", kindDocker}, {"compose.", ".yml", kindDocker},
	{"docker-compose.", ".yaml", kindDocker}, {"docker-compose.", ".yml", kindDocker},
	{"", ".gradle.kts", kindGradle},
	{".env.", "", kindData},
}

// Xcode 把这些目录当成一个整体（包），按扩展名认；其余目录是普通文件夹。
var kindByDirExt = map[string]fileKind{
	".xcodeproj": kindXcodeProj, ".xcworkspace": kindXcodeWorkspace,
	".xcassets": kindAssets, ".appiconset": kindAppIcon, ".imageset": kindImage, ".colorset": kindStyle,
	".lproj":     kindLocale,
	".framework": kindBundle, ".xcframework": kindBundle, ".bundle": kindBundle, ".app": kindBundle,
}

// dirIcon 是目录 name 的图标和颜色：Xcode 的包目录有自己的图标，其余是强调色的文件夹。
func dirIcon(t *ui.Theme, name string) (*ui.SVG, ui.Color) {
	// Git 列表的目录行是并起来的路径（vendor/.bundle），按最后一段认
	name = filepath.Base(name)
	ext := filepath.Ext(name)
	k, ok := kindByDirExt[strings.ToLower(ext)]
	// 点开头的目录（Ruby 的 .bundle 配置目录）只是名字像，不是包
	if !ok || ext == name {
		return iconFolder, t.Accent
	}
	return fileKinds[k].icon, colors(t).data[fileKinds[k].color]
}

// fileIcon 是 name 这类文件的图标和颜色。
func fileIcon(t *ui.Theme, name string) (*ui.SVG, ui.Color) {
	k := fileKinds[kindOf(name)]
	if k.color < 0 {
		return k.icon, t.TextMuted
	}
	return k.icon, colors(t).data[k.color]
}

// dirGlyph 画目录 name 的图标。
func dirGlyph(c *ui.Context, name string, size float32) ui.Element {
	svg, color := dirIcon(c.Theme(), name)
	return ui.Icon(c, svg).FontSize(size).TextColor(color).Shrink(0)
}

// changeGlyph 是 Git 文件列表里一项的图标：目录（子模块、嵌套仓库）画目录图标，删除的和文件名一样变灰。
func changeGlyph(c *ui.Context, name string, dir, deleted bool) {
	t := c.Theme()
	svg, color := fileIcon(t, name)
	if dir {
		svg, color = dirIcon(t, name)
	}
	if deleted {
		color = t.TextMuted
	}
	ui.Icon(c, svg).FontSize(iconSize).TextColor(color).Shrink(0)
}

// fileGlyph 画 name 这类文件的图标，颜色由类型定。
func fileGlyph(c *ui.Context, name string, size float32) ui.Element {
	svg, color := fileIcon(c.Theme(), name)
	return ui.Icon(c, svg).FontSize(size).TextColor(color).Shrink(0)
}
