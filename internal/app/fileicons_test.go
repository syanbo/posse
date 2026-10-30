package app

import "testing"

func TestKindOf(t *testing.T) {
	for name, want := range map[string]fileKind{
		"main.go":                      kindGo,
		"internal/app/view.go":         kindGo,
		"App.TSX":                      kindTS,
		"package.json":                 kindData,
		"package-lock.json":            kindLock,
		"go.sum":                       kindLock,
		"Cargo.lock":                   kindLock,
		"Makefile":                     kindShell,
		"Dockerfile":                   kindDocker,
		"Dockerfile.dev":               kindDocker,
		"app.Dockerfile":               kindDocker,
		"docker-compose.override.yml":  kindDocker,
		"compose.dev.yaml":             kindDocker,
		".dockerignore":                kindDocker,
		"api.proto":                    kindCode,
		"lib/main.dart":                kindDart,
		"pubspec.yaml":                 kindFlutter,
		"analysis_options.yaml":        kindFlutter,
		"l10n/app_zh.arb":              kindData,
		"ios/Runner/AppDelegate.swift": kindSwift,
		"Package.swift":                kindSwift,
		"Main.storyboard":              kindLayout,
		"Runner.entitlements":          kindData,
		"project.pbxproj":              kindData,
		"Podfile":                      kindCode,
		"Podfile.lock":                 kindLock,
		"MainActivity.kt":              kindKotlin,
		"build.gradle.kts":             kindGradle,
		"settings.gradle":              kindGradle,
		"gradlew":                      kindGradle,
		"AndroidManifest.xml":          kindAndroid,
		"app-release.apk":              kindAndroid,
		"upload.jks":                   kindKey,
		"App.java":                     kindJava,
		"src/App.vue":                  kindVue,
		".env.local":                   kindData,
		".gitignore":                   kindGit,
		"LICENSE":                      kindText,
		"README.md":                    kindMarkdown,
		"logo.PNG":                     kindImage,
		"build/posse.bin":              kindOther,
		"noext":                        kindOther,
		"archive.tar.gz":               kindArchive,
		"docs/design/flow.mmd":         kindOther,
		"scripts/dev-app.sh":           kindShell,
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
