# posse_mobile

Posse 手机客户端。Flutter 用 fvm 固定在 3.47.7（Dart 3.13.5），在这个目录里执行：

```sh
fvm flutter run
fvm flutter test
fvm flutter analyze
```

不要用全局 `flutter`。本机 `fvm default` 仍是 3.35.7，给别的工程用。

构建时 `packages/posse_quic` 的构建钩子会调 `go build` 把 Go 库（QUIC 客户端、Mermaid 排版）编进 App，本机要装 Go（版本照仓库根目录的 go.mod；找不到时设 `GO` 指向 go 可执行文件），安卓还要 NDK。改了 `packages/posse_quic/go`、`internal/mquic`、`internal/mermaid` 后它自己重编。`packages/posse_quic` 自己的测试（起一个 Go 写的回显服务，经 FFI 收发）在那个目录里跑 `fvm dart test`。

## 自己构建

- 装到 iPhone：在 Xcode 打开 `ios/Runner.xcworkspace`，Runner 的 Signing & Capabilities 里把 Team 换成自己的、Bundle Identifier 换成自己的（工程里写的是发布者的团队 `QKSM9Z4U85` 和包 ID），再 `fvm flutter run --release`。
- 安卓：`fvm flutter build apk --release`。没有发布证书（`~/Library/Application Support/Posse/android/key.properties`，可用环境变量 `POSSE_ANDROID_KEY` 换路径）时用 debug 证书签，装得上，但和官方安装包不能互相覆盖安装。
- 自己构建的包不带构建号（只有发布脚本用 `--dart-define=POSSE_BUILD` 写进去），不检查更新。

## 构建注意

- iOS 给登记过的 iPhone 分发用仓库根目录的 `scripts/ios-adhoc.sh`，安装页 `https://posse.shaoyan.uk/ios/index.html`（见 `docs/release.md`）。

- 安卓 release 构建的混淆规则在 `android/app/proguard-rules.pro`：shared_preferences_android 2.5 起经 package:jni 按名字和签名调 Kotlin，插件没带保留规则，不保住这些类名（含 jni 用到的 Kotlin 协程类）冷启动就读不出存着的配对和设置。升级 shared_preferences 或 jni 后要在真机上确认冷启动能自动连上。
- 闪屏的标志图和 slogan 图（原生各端和 `assets/splash_slogan.png` 共用）由 `POSSE_SPLASH_EXPORT=1 fvm flutter test test/splash_assets_test.dart` 导出，改了底色、标志、文字要重新导出。iOS 会缓存启动屏，改了以后真机要重启手机才看得到。
