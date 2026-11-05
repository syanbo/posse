#!/bin/sh
# 把工作区当前代码装进 build/dev/PosseDev.app，测屏幕录制、辅助功能这类按签名认的授权。
# 包 ID 和日常的 Posse 不同、证书相同：系统设置里给「Posse Dev」授权一次，之后每次重装都有效，
# 也不会和日常的 Posse.app 混成同一个 App。
# 新包在 PosseDev.app.new 里装好、签好再整个换上：正在运行的程序文件被原地改写，系统会按签名失效把进程杀掉。
# 只有一份：在 worktree 里跑也装到主仓库的 build/dev，换包前退出正在运行的 Posse Dev。--open 装完打开它。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

main=$(cd "$(git rev-parse --git-common-dir)/.." && pwd)
# 基础包按本机架构取：Intel Mac 上是 darwin-amd64
arch=$(go env GOARCH)
base=$main/build/darwin-$arch/Posse.app
dir=$main/build/dev
dev=$dir/PosseDev.app
new=$dir/PosseDev.app.new
old=$dir/PosseDev.app.old

if [ ! -d "$base" ]; then
	echo "缺 $base（图标、libghostty 从它复制）：先跑 go tool mygo build -platform darwin/$arch -skip-dmg" >&2
	exit 1
fi
identity=$(plutil -extract macos.signingIdentity raw mygo.json)

mkdir -p "$dir"
# 几个 agent 同时装时排队，免得互相删掉装了一半的包
# shlock 记着持锁的 pid，脚本被杀后留下的锁下次自动作废
lock=$dir/.lock
until shlock -f "$lock" -p $$; do sleep 1; done
trap 'rm -f "$lock"' EXIT
trap 'exit 1' INT TERM
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

# 退出所有 Posse Dev 窗口（哪个目录装的都算）。不按包 ID 发 quit：系统登记里可能还是删掉了的旧路径，发不到。
# SIGTERM 和菜单退出一样，有未保存的文件会停在询问上：等 10 秒还没退就不换包，免得 --open 叫起来的还是旧窗口
windows='/PosseDev\.app/Contents/MacOS/Posse$'
pkill -TERM -f "$windows" || true
i=0
while pgrep -f "$windows" >/dev/null; do
	if [ $i -ge 50 ]; then
		rm -rf "$new"
		echo "Posse Dev 没有退出（可能有未保存的文件），没换包：处理后重跑" >&2
		exit 1
	fi
	sleep 0.2
	i=$((i + 1))
done
if [ -d "$dev" ]; then
	mv "$dev" "$old"
fi
mv "$new" "$dev"
rm -rf "$old"
if [ "${1-}" = --open ]; then
	open "$dev"
fi
echo "$dev"
