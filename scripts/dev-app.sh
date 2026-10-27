#!/bin/sh
# 把工作区当前代码装进 build/dev/PosseDev.app，测屏幕录制、辅助功能这类按签名认的授权。
# 包 ID 和日常的 Posse 不同、证书相同：系统设置里给「Posse Dev」授权一次，之后每次重装都有效，
# 也不会和日常的 Posse.app 混成同一个 App。
# 新包在 PosseDev.app.new 里装好、签好再整个换上：正在运行的程序文件被原地改写，系统会按签名失效把进程杀掉。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

base=build/darwin-arm64/Posse.app
dir=build/dev
dev=$dir/PosseDev.app
new=$dir/PosseDev.app.new
old=$dir/PosseDev.app.old

if [ ! -d "$base" ]; then
	echo "缺 $base（图标、libghostty 从它复制）：先跑 go tool mygo build -platform darwin/arm64 -skip-dmg" >&2
	exit 1
fi
identity=$(plutil -extract macos.signingIdentity raw mygo.json)

mkdir -p "$dir"
rm -rf "$new" "$old"
ditto "$base" "$new"
# 删掉复制来的程序再编译，新文件和日常包里的不是同一个
rm -f "$new/Contents/MacOS/Posse"
go build -o "$new/Contents/MacOS/Posse" ./cmd/posse

plist=$new/Contents/Info.plist
plutil -replace CFBundleIdentifier -string com.dengbo.posse.dev "$plist"
plutil -replace CFBundleName -string PosseDev "$plist"
plutil -replace CFBundleDisplayName -string "Posse Dev" "$plist"

# 复制来的 dylib 是日常包打包时签的，证书换过团队后强化运行时不让加载，要一起重签
for lib in "$new"/Contents/Resources/*.dylib; do
	codesign --force --sign "$identity" --options runtime --timestamp=none "$lib"
done
codesign --force --sign "$identity" --options runtime --timestamp=none "$new"
codesign --verify --strict "$new"

if [ -d "$dev" ]; then
	mv "$dev" "$old"
fi
mv "$new" "$dev"
rm -rf "$old"
echo "$dev"
