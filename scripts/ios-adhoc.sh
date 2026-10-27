#!/bin/sh
# 发布 iOS 的 Ad Hoc 安装包：打 ipa，连同安装清单和安装页传到 R2，
# 在 iPhone 的 Safari 里打开 https://posse.shaoyan.uk/ios/index.html 点安装。
#
#   scripts/ios-adhoc.sh
#
# 只有在开发者账号里登记过 UDID 的设备装得上：Ad Hoc 描述文件打包时列进这些设备，
# 新设备登记后要重新运行。版本号取 mobile/pubspec.yaml，构建号取提交数，iOS 按构建号覆盖安装、不清数据。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

bucket=posse-updates
prefix=ios
base=$(plutil -extract updates.url raw mygo.json)/$prefix
wrangler() { npx --yes wrangler@4 "$@"; }

bundle=$(grep -m1 'PRODUCT_BUNDLE_IDENTIFIER = ' mobile/ios/Runner.xcodeproj/project.pbxproj | sed 's/.*= \(.*\);/\1/')
version=$(sed -n 's/^version: \([0-9.]*\).*/\1/p' mobile/pubspec.yaml)
build=$(git rev-list --count HEAD)
if [ -n "$(git status --porcelain -- mobile internal/mquic)" ]; then
	echo "注意：mobile 或 internal/mquic 有未提交的改动，会一起打进包里" >&2
fi
if ! wrangler whoami >/dev/null 2>&1; then
	echo "wrangler 没有登录：先运行 npx wrangler login" >&2
	exit 1
fi

out=build/ios-adhoc
rm -rf "$out" mobile/build/ios/ipa
mkdir -p "$out"
(cd mobile && fvm flutter build ipa --release --build-number="$build" --export-options-plist=ios/ExportOptions-adhoc.plist)
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
a.install { display: inline-block; margin: 28px 0; padding: 12px 40px; border-radius: 22px; background: #e8743b; color: #fff; font-size: 17px; text-decoration: none; }
.note { color: #86868b; font-size: 13px; line-height: 1.6; text-align: left; max-width: 360px; margin: 0 auto; }
@media (prefers-color-scheme: dark) { body { background: #000; color: #f5f5f7; } }
</style>
</head>
<body>
<img src="icon-512.png" alt="">
<h1>Posse</h1>
<div class="meta">${version}（${build}） · $(date '+%Y-%m-%d %H:%M')</div>
<a class="install" href="itms-services://?action=download-manifest&amp;url=$base/manifest.plist">安装</a>
<div class="note">
请用 Safari 打开这个页面。点安装后回到主屏幕看进度。<br>
只有登记过的 iPhone 能装，其他设备会提示无法安装。
</div>
</body>
</html>
EOF

put() { # put <键> <文件> <类型> [缓存]
	wrangler r2 object put "$bucket/$prefix/$1" --file "$2" --content-type "$3" ${4:+--cache-control "$4"} --remote
}
# 先传包和图标，最后传清单和安装页
put "Posse-$version-$build.ipa" "$out/Posse.ipa" application/octet-stream
put icon-57.png "$out/icon-57.png" image/png no-cache
put icon-512.png "$out/icon-512.png" image/png no-cache
put manifest.plist "$out/manifest.plist" application/xml no-cache
put index.html "$out/index.html" "text/html; charset=utf-8" no-cache

echo "已发布 ${version}（${build}）：$base/index.html"
