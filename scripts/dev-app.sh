#!/bin/sh
# 把工作区当前代码装进 build/dev/HiveDev.app，测屏幕录制、辅助功能这类按签名认的授权。
# 包 ID 和日常的 Hive 不同、证书相同：系统设置里给「Hive Dev」授权一次，之后每次重装都有效，
# 也不会和日常的 Hive.app 混成同一个 App。
# 新包在 HiveDev.app.new 里装好、签好再整个换上：正在运行的程序文件被原地改写，系统会按签名失效把进程杀掉。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

base=build/darwin-arm64/Hive.app
dir=build/dev
dev=$dir/HiveDev.app
new=$dir/HiveDev.app.new
old=$dir/HiveDev.app.old

if [ ! -d "$base" ]; then
	echo "缺 $base（图标、libghostty 从它复制）：先跑 go tool mygo build -platform darwin/arm64 -skip-dmg" >&2
	exit 1
fi
identity=$(plutil -extract macos.signingIdentity raw mygo.json)

mkdir -p "$dir"
rm -rf "$new" "$old"
ditto "$base" "$new"
# 删掉复制来的程序再编译，新文件和日常包里的不是同一个
rm -f "$new/Contents/MacOS/Hive"
go build -o "$new/Contents/MacOS/Hive" ./cmd/hive

plist=$new/Contents/Info.plist
plutil -replace CFBundleIdentifier -string com.dengbo.hive.dev "$plist"
plutil -replace CFBundleName -string HiveDev "$plist"
plutil -replace CFBundleDisplayName -string "Hive Dev" "$plist"

codesign --force --sign "$identity" --options runtime --timestamp=none "$new"
codesign --verify --strict "$new"

if [ -d "$dev" ]; then
	mv "$dev" "$old"
fi
mv "$new" "$dev"
rm -rf "$old"
echo "$dev"
