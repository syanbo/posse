package app

import "testing"

func TestKindOf(t *testing.T) {
	for name, want := range map[string]fileKind{
		"main.go":                     kindGo,
		"internal/app/view.go":        kindGo,
		"App.TSX":                     kindTS,
		"package.json":                kindData,
		"package-lock.json":           kindLock,
		"go.sum":                      kindLock,
		"Cargo.lock":                  kindLock,
		"Makefile":                    kindShell,
		"Dockerfile":                  kindDocker,
		"Dockerfile.dev":              kindDocker,
		"app.Dockerfile":              kindDocker,
		"docker-compose.override.yml": kindDocker,
		"compose.dev.yaml":            kindDocker,
		".dockerignore":               kindDocker,
		"api.proto":                   kindCode,
		".env.local":                  kindData,
		".gitignore":                  kindGit,
		"LICENSE":                     kindText,
		"README.md":                   kindMarkdown,
		"logo.PNG":                    kindImage,
		"build/posse.bin":             kindOther,
		"noext":                       kindOther,
		"archive.tar.gz":              kindArchive,
		"docs/design/flow.mmd":        kindOther,
		"mobile/lib/main.dart":        kindCode,
		"scripts/dev-app.sh":          kindShell,
	} {
		if got := kindOf(name); got != want {
			t.Errorf("kindOf(%q) = %d, want %d", name, got, want)
		}
	}
}

// 每一类都有图标，颜色下标在数据色范围里。
func TestFileKindsComplete(t *testing.T) {
	for k, e := range fileKinds {
		if e.icon == nil {
			t.Errorf("kind %d 没有图标", k)
		}
		if e.color >= len(darkData) || e.color >= len(lightData) {
			t.Errorf("kind %d 的颜色下标 %d 超出数据色", k, e.color)
		}
	}
}
