#!/bin/sh
# 发布安卓安装包：打 arm64 的 APK，连同下载页和 latest.json 传到 R2，
# 手机浏览器打开 https://posse.shaoyan.uk/android/index.html 下载安装；装过的 App 读 latest.json 在首页提示更新，应用内下载、交给系统安装器。
#
#   scripts/android-release.sh 0.1.4   先在 mobile/CHANGELOG.md 写好 "## 0.1.4" 一节（可以不提交），脚本把版本号写进
#                                      mobile/pubspec.yaml，连同 CHANGELOG 提交成 chore(release): 发布安卓 0.1.4，再打包上传
#   scripts/android-release.sh         CHANGELOG 最上面一节的版本和 pubspec.yaml 不同时发这一版（同上）；
#                                      相同时不改版本（比如 iOS 刚发过这一版），按现在的版本打包上传
#
# 版本号和 CHANGELOG 和 iOS 共用。构建号取提交数，写成 versionCode，App 也按它判断有没有新版本。
# 发布证书在 ~/Library/Application Support/Posse/android/（第一次运行时生成），丢了已装的 App 就不能覆盖更新，
# 只能卸载重装、重新配对。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

bucket=posse-updates
prefix=android
base=$(plutil -extract updates.url raw mygo.json)/$prefix
wrangler() { npx --yes wrangler@4 "$@"; }
changelog=mobile/CHANGELOG.md
keys="$HOME/Library/Application Support/Posse/android"
if ! wrangler whoami >/dev/null 2>&1; then
	echo "wrangler 没有登录：先运行 npx wrangler login" >&2
	exit 1
fi

if [ ! -f "$keys/key.properties" ]; then
	mkdir -p "$keys"
	chmod 700 "$keys"
	pass=$(openssl rand -hex 24)
	keytool -genkeypair -keystore "$keys/release.jks" -storetype PKCS12 -alias posse -keyalg RSA -keysize 4096 \
		-validity 36500 -storepass "$pass" -keypass "$pass" -dname "CN=Posse" >/dev/null
	printf 'storeFile=release.jks\nstorePassword=%s\nkeyAlias=posse\nkeyPassword=%s\n' "$pass" "$pass" >"$keys/key.properties"
	chmod 600 "$keys/key.properties" "$keys/release.jks"
	echo "已生成发布证书：$keys（备份它，丢了已装的 App 收不到更新）" >&2
fi

if [ $# -eq 0 ]; then
	latest=$(sed -n 's/^## \([0-9][0-9.]*\)$/\1/p' "$changelog" | head -n 1)
	if [ -n "$latest" ] && ! grep -q "^version: $latest+" mobile/pubspec.yaml; then
		set -- "$latest"
	fi
fi
if [ $# -gt 0 ]; then
	version=$1
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
	git commit -q -m "chore(release): 发布安卓 $version" -- mobile/pubspec.yaml "$changelog"
fi

build=$(git rev-list --count HEAD)
if [ -n "$(git status --porcelain -- mobile internal/mquic)" ]; then
	echo "注意：mobile 或 internal/mquic 有未提交的改动，会一起打进包里" >&2
fi
notes=$(awk -v v="## $version" '
	$0 == v { on = 1; next }
	on && /^## / { exit }
	on && /^- / { if (item != "") print item; item = substr($0, 3); next }
	on && /^[ \t]+[^ \t]/ && item != "" { sub(/^[ \t]+/, ""); item = item $0; next }
	END { if (item != "") print item }
' "$changelog")

out=build/android-release
rm -rf "$out"
mkdir -p "$out"
# 只出 arm64：现在的安卓手机都是，三种架构合在一个包里大一倍多
(cd mobile && fvm flutter build apk --release --target-platform android-arm64 --build-name="$version" --build-number="$build" \
	--dart-define=POSSE_VERSION="$version" --dart-define=POSSE_BUILD="$build")
apk=mobile/build/app/outputs/flutter-apk/app-release.apk
# 装不上的包不发：没用发布证书签（证书没读到时 gradle 会退回调试证书）的拒绝
sdk=$(sed -n 's/^sdk.dir=//p' mobile/android/local.properties)
if ! "$(ls -d "$sdk"/build-tools/* | sort -V | tail -n 1)/apksigner" verify --print-certs "$apk" | grep -q "CN=Posse"; then
	echo "$apk 不是用发布证书签的" >&2
	exit 1
fi
cp "$apk" "$out/Posse.apk"
cp mobile/assets/icon.png "$out/icon-512.png"
file="Posse-$version-$build.apk"

size=$(stat -f%z "$out/Posse.apk" | awk '{ printf "%.1f MB", $1 / 1048576 }')
cat >"$out/index.html" <<EOF
<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>安装 Posse</title>
<style>
body { font-family: sans-serif; margin: 0; padding: 48px 24px; text-align: center; color: #1d1d1f; }
img { width: 96px; height: 96px; border-radius: 22px; }
h1 { font-size: 24px; margin: 16px 0 4px; }
.meta { color: #86868b; font-size: 14px; }
a.install { display: inline-block; margin: 28px 0; padding: 12px 40px; border-radius: 22px; background: #e8743b; color: #fff; font-size: 17px; text-decoration: none; }
ul.notes { text-align: left; max-width: 360px; margin: 0 auto 8px; padding-left: 20px; font-size: 15px; line-height: 1.6; }
.note { color: #86868b; font-size: 13px; line-height: 1.6; text-align: left; max-width: 360px; margin: 0 auto; }
@media (prefers-color-scheme: dark) { body { background: #000; color: #f5f5f7; } }
</style>
</head>
<body>
<img src="icon-512.png" alt="">
<h1>Posse</h1>
<div class="meta">${version}（${build}） · ${size} · $(date -r "$out/Posse.apk" '+%Y-%m-%d %H:%M')</div>
<a class="install" href="$file">下载安装</a>
<ul class="notes">
$(printf '%s\n' "$notes" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's|.*|<li>&</li>|')
</ul>
<div class="note">
下载后打开安装包，按提示允许浏览器安装应用。装好以后 App 里会提示新版本，不用再来这里。
</div>
</body>
</html>
EOF

printf '%s\n' "$notes" | jq -R . | jq -s \
	--arg version "$version" --argjson build "$build" --arg size "$size" --arg install "$base/$file" \
	'{version: $version, build: $build, size: $size, notes: map(select(. != "")), install: $install}' >"$out/latest.json"

put() { # put <键> <文件> <类型> [缓存]
	wrangler r2 object put "$bucket/$prefix/$1" --file "$2" --content-type "$3" ${4:+--cache-control "$4"} --remote
}
# 先传包和图标，最后传下载页和清单
put "$file" "$out/Posse.apk" application/vnd.android.package-archive
put icon-512.png "$out/icon-512.png" image/png no-cache
put index.html "$out/index.html" "text/html; charset=utf-8" no-cache
put latest.json "$out/latest.json" application/json no-cache

echo "已发布 ${version}（${build}）：$base/index.html"
