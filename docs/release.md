# 发布

三端都在本机打包，传到 Cloudflare R2 的 `posse-updates`，经 `https://posse.shaoyan.uk` 下载。上传用 wrangler（本机登录的 Cloudflare 账号，过期了 `npx wrangler login`）。签名证书和更新签名私钥都不进仓库，丢了已装的 App 就收不到更新或不能覆盖安装，位置写在各节里。

更新说明（`CHANGELOG.md`、`mobile/CHANGELOG.md`）每节先写中文条目，再写 `### English` 小节放对应的英文条目：Mac 端按界面语言显示其中一份（`appupdate.go` 的 `localNotes`）；手机的发布脚本把中文条目写进 `latest.json` 的 `notes` 和安装页，英文条目写进 `notes_en`。

## Mac

先在 `CHANGELOG.md` 加 `## <版本>` 一节（更新时显示的更新内容，可以不提交），再跑 `scripts/release.sh <版本>`：它把版本号写进 `mygo.json` 和 `internal/daemon/daemon.go`（`TestVersionMatchesPackage` 保证两处一致），连同 CHANGELOG 提交成 `chore(release): 发布 <版本>`、打标签 `v<版本>`（不推送），再打包、公证到 `build/release/<版本>`，传到 R2。Apple 芯片和 Intel 各打一个包（`darwin-arm64`、`darwin-amd64`），各有自己的更新清单；下载链接 `Posse.dmg` 是 Apple 芯片的，`Posse-Intel.dmg` 是 Intel 的，`install.sh` 按本机架构选。打包或上传失败后不带参数重跑，发布 `mygo.json` 里现在的版本：线上清单已是这个版本的平台跳过，只重新打包、上传没发完的平台。更新包的签名私钥在 `~/Library/Application Support/mygo/update-keys/`，丢了已装的 Posse 就收不到更新；只有 `release.sh` 把它交给 mygo，日常打包不做更新包（会提示 Not signing an update）。方案和换包时机见 `docs/plans/mac-update.md`。

## iOS（Ad Hoc）

先在 `mobile/CHANGELOG.md` 加 `## <版本>` 一节（App 提示更新时显示的更新内容，可以不提交），再跑 `scripts/ios-adhoc.sh <版本>`：它把版本号写进 `mobile/pubspec.yaml`，连同 CHANGELOG 提交成 `chore(release): 发布 iOS <版本>`（不推送；`mobile`、`internal/mquic` 有别的未提交改动时拒绝），用 Apple Distribution 证书打 ipa（导出设置 `mobile/ios/ExportOptions-adhoc.plist`，`--dart-define` 写进版本号和构建号），连同 itms-services 清单、安装页和 `latest.json` 传到 R2 的 `ios/`，iPhone 用 Safari 打开 `https://posse.shaoyan.uk/ios/index.html` 安装，装过的 App 读 `latest.json` 在首页提示更新。不带参数时，CHANGELOG 最上面一节的版本和 `pubspec.yaml` 不同就照它发新版，相同就按现在的版本重新打包上传（登记新设备、上传失败后）。只有开发者账号里登记过 UDID 的设备装得上；构建号是提交数加 8（开源前捋直历史去掉了 8 个合并提交，见脚本），覆盖安装不清配对。

## 安卓

同样先在 `mobile/CHANGELOG.md` 加一节（和 iOS 共用版本号，iOS 刚发过同一版就不用改），再跑 `scripts/android-release.sh <版本>`：版本号逻辑和 iOS 脚本一样（提交成 `chore(release): 发布安卓 <版本>`），打 arm64 的 APK（构建号同 iOS，写成 versionCode），连同下载页和 `latest.json` 传到 R2 的 `android/`，手机浏览器打开 `https://posse.shaoyan.uk/android/index.html` 下载安装，装过的 App 在首页提示更新、应用内下载后交给系统安装器。发布证书在 `~/Library/Application Support/Posse/android/`（第一次运行时生成），丢了已装的 App 不能覆盖更新，只能卸载重装、重新配对。
