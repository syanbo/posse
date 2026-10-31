package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mobileKindNames 是各类文件在手机端 FileKind 里的名字，顺序和 fileKind 一致。
var mobileKindNames = [...]string{
	kindOther: "other", kindTS: "ts", kindJS: "js", kindGo: "go", kindCode: "code", kindShell: "shell",
	kindMarkup: "markup", kindStyle: "style", kindData: "data", kindMarkdown: "markdown", kindText: "text",
	kindImage: "image", kindVideo: "video", kindAudio: "audio", kindTable: "table", kindDatabase: "database",
	kindArchive: "archive", kindPDF: "pdf", kindLock: "lock", kindGit: "git", kindDocker: "docker",
	kindDart: "dart", kindFlutter: "flutter", kindSwift: "swift", kindKotlin: "kotlin", kindJava: "java",
	kindVue: "vue", kindAndroid: "android", kindGradle: "gradle", kindLayout: "layout", kindKey: "key",
	kindHeader: "header", kindObjC: "objc", kindXcodeProj: "xcodeProj", kindXcodeWorkspace: "xcodeWorkspace",
	kindAssets: "assets", kindAppIcon: "appIcon", kindLocale: "locale", kindBundle: "bundle",
}

const mobileFileIcons = "../../mobile/lib/ui/file_icons_data.dart"

// 手机端的文件类型图标和识别规则由这张表生成，和桌面一致。改了 fileicons.go 后
// POSSE_GEN=1 go test ./internal/app -run TestMobileFileIcons 重新生成。
func TestMobileFileIcons(t *testing.T) {
	if len(mobileKindNames) != len(fileKinds) {
		t.Fatalf("mobileKindNames 有 %d 项，fileKinds 有 %d 项", len(mobileKindNames), len(fileKinds))
	}
	var b strings.Builder
	b.WriteString("// 由 internal/app/fileicons_mobile_test.go 照桌面的 fileicons.go 生成，不要手改：\n")
	b.WriteString("// POSSE_GEN=1 go test ./internal/app -run TestMobileFileIcons\n\n")
	b.WriteString("part of 'file_icons.dart';\n\n")
	b.WriteString("/// 文件类型：图标的 SVG 和数据色下标（橙、蓝、绿、紫、金、青，-1 是次要色）。\n")
	b.WriteString("enum FileKind {\n")
	for k, e := range fileKinds {
		src, ok := svgSource[e.icon]
		if !ok {
			t.Fatalf("%s 的图标没有经 icon 或 svgOf 建，拿不到源码", mobileKindNames[k])
		}
		if strings.Contains(src, "'''") {
			t.Fatalf("%s 的 SVG 里有 '''", mobileKindNames[k])
		}
		sep := ","
		if k == len(fileKinds)-1 {
			sep = ";"
		}
		fmt.Fprintf(&b, "  %s(\n    r'''%s''',\n    %d,\n  )%s\n", mobileKindNames[k], src, e.color, sep)
	}
	b.WriteString("\n  const FileKind(this.svg, this.color);\n\n  final String svg;\n  final int color;\n}\n")
	writeMap := func(name string, m map[string]fileKind) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		fmt.Fprintf(&b, "\nconst %s = <String, FileKind>{\n", name)
		for _, k := range keys {
			fmt.Fprintf(&b, "  '%s': FileKind.%s,\n", k, mobileKindNames[m[k]])
		}
		b.WriteString("};\n")
	}
	writeMap("_kindByName", kindByName)
	writeMap("_kindByExt", kindByExt)
	writeMap("_kindByDirExt", kindByDirExt)
	b.WriteString("\nconst _kindByAffix = <(String, String, FileKind)>[\n")
	for _, r := range kindByAffix {
		fmt.Fprintf(&b, "  ('%s', '%s', FileKind.%s),\n", r.prefix, r.suffix, mobileKindNames[r.kind])
	}
	b.WriteString("];\n")
	// 符号链接不按类型，和桌面文件树一样是带拐弯箭头的纸和文件夹
	fmt.Fprintf(&b, "\nconst _fileLinkSvg =\n    r'''%s''';\n", svgSource[iconFileLink])
	fmt.Fprintf(&b, "\nconst _folderLinkSvg =\n    r'''%s''';\n", svgSource[iconFolderLink])
	want := b.String()

	if os.Getenv("POSSE_GEN") != "" {
		if err := os.WriteFile(mobileFileIcons, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(mobileFileIcons)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s 和桌面的 fileicons.go 对不上：POSSE_GEN=1 go test ./internal/app -run TestMobileFileIcons 重新生成", filepath.Base(mobileFileIcons))
	}
}
