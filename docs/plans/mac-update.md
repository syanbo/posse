# Mac 自动更新

## 目标

装在「应用程序」里的 Posse 自己发现、下载新版本，用户点一次「重启更新」就换上，终端不断。

## 发布

- 签名用 Developer ID Application（团队 QKSM9Z4U85），dmg 经公证并钉票据，票据也钉在 app 上（更新包里的 app 带着它）。
- 更新包和清单放在 Cloudflare R2 的 `posse-updates`，经 `https://posse.shaoyan.uk` 下载。用 mygo 自带的更新格式：`posse-<版本>-darwin-arm64.tar.gz`、增量包、清单 `update-darwin-arm64.json`，都用 Ed25519 签名（私钥在 `~/Library/Application Support/mygo/update-keys/`，不进仓库）。
- `scripts/release.sh <版本>`：除了 `CHANGELOG.md` 工作区干净、版本比现在和线上的都新、`CHANGELOG.md` 有这个版本的一节，才改版本号、提交、打标签、打包；不带版本号时发布现在的版本（失败后重试）。打包输出到 `build/release/`，不碰日常的 `build/darwin-arm64/`。上传顺序：更新包和增量包、dmg（另存一份 `Posse.dmg` 给下载链接用），最后清单（不缓存）：客户端看到清单时它指向的包一定已经在了。
- 上传用 wrangler（本机已登录的 Cloudflare 账号），仓库和钥匙串里不放 R2 密钥。

## 换包的时机

实测（PosseDev，2026-10-27）：包被换掉后，常驻进程在换之前访问过的受保护目录还能访问，换之后第一次访问的（如文稿）直接 EPERM、不弹授权；交接给新包的常驻进程后，新老终端都恢复。窗口同样（`relaunch.go`）。

所以不在后台换包：后台只把新版本下载、校验、解压到 app 旁边（`/Applications/.Posse.app.update-*`），用户点「重启更新」或退出 Posse 时才换包，紧接着交接常驻进程、重开窗口。mygo 的 `Update.Install` 把下载和换包绑在一起，在 `third_party/mygo` 里拆成 `Stage` 和 `Staged.Apply`。

## 检查

- 只有带更新地址的正式包、装在 `/Applications` 或 `~/Applications`、能写那个目录时才检查。`build/` 里的包会提示移到「应用程序」，PosseDev 和 `go build` 的不显示更新。
- 距上次成功检查满 6 小时才查（时间记在 ui.json）。触发：窗口启动 10 秒后、之后每 30 分钟、从睡眠唤醒。手动「检查更新」不看时间。
- 同一时间只跑一次。失败不记时间，下一轮再试；后台失败不打扰，只写在设置页。
- 已经下好一个版本时照常检查，有更新的就换成下更新的。

## 交互

```
侧栏底部卡片（和"已更新，重开窗口"同一处）
├─ Posse 0.2.0 已就绪，更新时终端不会断开。
├─ [重启更新]  更新内容（打开设置的「关于」）
└─ ✕：这个版本不再出卡片
设置 › 关于 › 更新
├─ 状态：已是最新（几分钟前检查）/ 正在下载 37% / 0.2.0 已就绪 / 检查失败：原因 / 移到「应用程序」后才能自动更新
├─ [检查更新] 或 [重启更新]
└─ 更新内容：CHANGELOG.md 里这个版本的一节
应用菜单：检查更新…（打开设置的「关于」并检查）
```

「重启更新」：先处理未保存的文件 → 换包 → 用新包交接常驻进程 → 重开窗口。交接失败也重开，新窗口的 `autoUpgrade` 会再试。

退出时有下好的版本：在事件循环停下后换包并交接常驻进程，下次打开就是新版本。

## 版本号

`mygo.json` 的 `version` 是 app 的版本（更新按它比较），`daemon.Version` 跟它一致，由测试保证。发布时在 `CHANGELOG.md` 写好 `## <版本>` 一节，`scripts/release.sh <版本>` 改这两处、提交、打标签再发布。
