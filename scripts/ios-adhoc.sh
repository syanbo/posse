#!/bin/sh
# 发布 iOS 的 Ad Hoc 安装包：打 ipa，连同安装清单、安装页和 latest.json 传到 R2，
# 在 iPhone 的 Safari 里打开 https://posse.shaoyan.uk/ios/index.html 点安装；装过的 App 读 latest.json 在首页提示更新。
#
#   scripts/ios-adhoc.sh 0.1.1   先在 mobile/CHANGELOG.md 写好 "## 0.1.1" 一节（可以不提交），脚本把版本号写进
#                                mobile/pubspec.yaml，连同 CHANGELOG 提交成 chore(release): 发布 iOS 0.1.1，再打包上传
#   scripts/ios-adhoc.sh         CHANGELOG 最上面一节的版本和 pubspec.yaml 不同时发这一版（同上，提交后打包上传）；
#                                相同时不改版本，按现在的版本重新打包上传（登记了新设备、上传失败后重跑）
#
# 只有在开发者账号里登记过 UDID 的设备装得上：Ad Hoc 描述文件打包时列进这些设备，
# 新设备登记后要重新运行。构建号取提交数，iOS 按构建号覆盖安装、不清数据，App 也按构建号判断有没有新版本。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

bucket=posse-updates
prefix=ios
base=$(plutil -extract updates.url raw mygo.json)/$prefix
wrangler() { npx --yes wrangler@4 "$@"; }
changelog=mobile/CHANGELOG.md
# 先查登录再提交版本号，免得提交了传不上去
if ! wrangler whoami >/dev/null 2>&1; then
	echo "wrangler 没有登录：先运行 npx wrangler login" >&2
	exit 1
fi

# 写了新一节的 CHANGELOG 就是要发新版，不带版本号也照这一节升，免得只换了构建号、版本号没动
if [ $# -eq 0 ]; then
	latest=$(sed -n 's/^## \([0-9][0-9.]*\)$/\1/p' "$changelog" | head -n 1)
	if [ -n "$latest" ] && ! grep -q "^version: $latest+" mobile/pubspec.yaml; then
		set -- "$latest"
	fi
fi
if [ $# -gt 0 ]; then
	version=$1
	# 只允许 CHANGELOG 没提交：它和版本号放在同一个提交里；包外的改动（电脑端）不管
	if [ -n "$(git status --porcelain -- mobile internal/mquic | grep -v " $changelog\$" || true)" ]; then
		echo "mobile 或 internal/mquic 除了 CHANGELOG.md 还有未提交的改动，先提交再发布" >&2
		exit 1
	fi
else
	version=$(sed -n 's/^version: \([0-9.]*\).*/\1/p' mobile/pubspec.yaml)
fi
if ! grep -q "^## $version\$" "$changelog"; then
	echo "$changelog 里没有 \"## $version\" 一节：它是 App 里提示更新时显示的更新内容" >&2
	exit 1
fi
if [ $# -gt 0 ] && ! grep -q "^version: $version+" mobile/pubspec.yaml; then
	sed -i '' "s/^version: .*/version: $version+1/" mobile/pubspec.yaml
	git add mobile/pubspec.yaml "$changelog"
	git commit -q -m "chore(release): 发布 iOS $version" -- mobile/pubspec.yaml "$changelog"
fi

bundle=$(grep -m1 'PRODUCT_BUNDLE_IDENTIFIER = ' mobile/ios/Runner.xcodeproj/project.pbxproj | sed 's/.*= \(.*\);/\1/')
build=$(git rev-list --count HEAD)
if [ -n "$(git status --porcelain -- mobile internal/mquic)" ]; then
	echo "注意：mobile 或 internal/mquic 有未提交的改动，会一起打进包里" >&2
fi
# 这一版的更新内容：CHANGELOG 里 "## 版本" 下面的列表，一条一项（续行接到上一条后面）
notes=$(awk -v v="## $version" '
	$0 == v { on = 1; next }
	on && /^## / { exit }
	on && /^- / { if (item != "") print item; item = substr($0, 3); next }
	on && /^[ \t]+[^ \t]/ && item != "" { sub(/^[ \t]+/, ""); item = item $0; next }
	END { if (item != "") print item }
' "$changelog")

out=build/ios-adhoc
rm -rf "$out" mobile/build/ios/ipa
mkdir -p "$out"
(cd mobile && fvm flutter build ipa --release --build-number="$build" --export-options-plist=ios/ExportOptions-adhoc.plist \
	--dart-define=POSSE_VERSION="$version" --dart-define=POSSE_BUILD="$build")
ipa=$(ls mobile/build/ios/ipa/*.ipa)
cp "$ipa" "$out/Posse.ipa"
sips -z 57 57 mobile/assets/icon.png --out "$out/icon-57.png" >/dev/null
cp mobile/assets/icon.png "$out/icon-512.png"

# itms-services 按这份清单下载、安装；bundle-version 只用来显示，装哪个版本看 ipa
cat >"$out/manifest.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>items</key>
	<array>
		<dict>
			<key>assets</key>
			<array>
				<dict><key>kind</key><string>software-package</string><key>url</key><string>$base/Posse-$version-$build.ipa</string></dict>
				<dict><key>kind</key><string>display-image</string><key>url</key><string>$base/icon-57.png</string></dict>
				<dict><key>kind</key><string>full-size-image</string><key>url</key><string>$base/icon-512.png</string></dict>
			</array>
			<key>metadata</key>
			<dict>
				<key>bundle-identifier</key><string>$bundle</string>
				<key>bundle-version</key><string>$version</string>
				<key>kind</key><string>software</string>
				<key>title</key><string>Posse</string>
			</dict>
		</dict>
	</array>
</dict>
</plist>
EOF

size=$(stat -f%z "$out/Posse.ipa" | awk '{ printf "%.1f MB", $1 / 1048576 }')
cat >"$out/index.html" <<EOF
<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>安装 Posse</title>
<style>
body { font-family: -apple-system, sans-serif; margin: 0; padding: 48px 24px; text-align: center; color: #1d1d1f; }
img { width: 96px; height: 96px; border-radius: 22px; }
h1 { font-size: 24px; margin: 16px 0 4px; }
.meta { color: #86868b; font-size: 14px; }
a.install { display: inline-flex; align-items: center; gap: 8px; margin: 28px 0; padding: 12px 40px; border-radius: 22px; background: #e8743b; color: #fff; font-size: 17px; text-decoration: none; }
a.install.busy { opacity: .6; pointer-events: none; }
a.install.busy::before { content: ""; width: 14px; height: 14px; border: 2px solid rgba(255,255,255,.4); border-top-color: #fff; border-radius: 50%; animation: spin .8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
ul.notes { text-align: left; max-width: 360px; margin: 0 auto 8px; padding-left: 20px; font-size: 15px; line-height: 1.6; }
.note { color: #86868b; font-size: 13px; line-height: 1.6; text-align: left; max-width: 360px; margin: 0 auto; }
@media (prefers-color-scheme: dark) { body { background: #000; color: #f5f5f7; } }
</style>
</head>
<body>
<img src="icon-512.png" alt="">
<h1>Posse</h1>
<div class="meta">${version}（${build}） · ${size} · $(date -r "$out/Posse.ipa" '+%Y-%m-%d %H:%M')</div>
<a class="install" href="itms-services://?action=download-manifest&amp;url=$base/manifest.plist">安装</a>
<ul class="notes">
$(printf '%s\n' "$notes" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's|.*|<li>&</li>|')
</ul>
<div class="note">
请用 Safari 打开这个页面。点安装后回到主屏幕看进度。<br>
只有登记过的 iPhone 能装，其他设备会提示无法安装。
</div>
<script>
// 点了以后系统要过几秒才弹安装确认，期间按钮转圈、点不动，免得连点弹出好几次；
// 用户在弹框里取消的话页面收不到消息，过一会儿恢复成能再点
const a = document.querySelector("a.install");
a.addEventListener("click", (e) => {
	if (a.classList.contains("busy")) { e.preventDefault(); return; }
	a.classList.add("busy");
	a.textContent = "正在请求安装…";
	setTimeout(() => { a.classList.remove("busy"); a.textContent = "安装"; }, 15000);
});
</script>
</body>
</html>
EOF

# App 按它判断有没有新版本、显示更新内容，点「更新」打开 install
printf '%s\n' "$notes" | jq -R . | jq -s \
	--arg version "$version" --argjson build "$build" --arg size "$size" \
	--arg install "itms-services://?action=download-manifest&url=$base/manifest.plist" \
	'{version: $version, build: $build, size: $size, notes: map(select(. != "")), install: $install}' >"$out/latest.json"

put() { # put <键> <文件> <类型> [缓存]
	wrangler r2 object put "$bucket/$prefix/$1" --file "$2" --content-type "$3" ${4:+--cache-control "$4"} --remote
}
# 先传包和图标，最后传清单和安装页
put "Posse-$version-$build.ipa" "$out/Posse.ipa" application/octet-stream
put icon-57.png "$out/icon-57.png" image/png no-cache
put icon-512.png "$out/icon-512.png" image/png no-cache
put manifest.plist "$out/manifest.plist" application/xml no-cache
put index.html "$out/index.html" "text/html; charset=utf-8" no-cache
put latest.json "$out/latest.json" application/json no-cache

echo "已发布 ${version}（${build}）：$base/index.html"
