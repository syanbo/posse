#!/bin/sh
# 发布 Mac 版：打包、公证，把更新包、dmg 和清单传到 R2，装在「应用程序」里的 Posse 会自己更新
# （方案见 docs/plans/mac-update.md）。
#
#   scripts/release.sh 0.2.0   先在 CHANGELOG.md 写好 "## 0.2.0" 一节（可以不提交），脚本把版本号
#                              写进 mygo.json 和 internal/daemon/daemon.go，连同 CHANGELOG.md 提交、打标签，再发布
#   scripts/release.sh         发布 mygo.json 里现在的版本（上次打包或上传失败后重试）
#
# 打包输出到 build/release/<版本>，不碰日常在用的 build/darwin-arm64/Posse.app。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

bucket=posse-updates
# 更新包的签名私钥只在发布时交给 mygo：mygo.json 里不写它，日常打包就不做更新包和增量包（要下载旧版逐个比较，慢）
key="$HOME/Library/Application Support/mygo/update-keys/mygo-update.key"
if [ ! -f "$key" ]; then
	echo "找不到更新包的签名私钥：$key" >&2
	exit 1
fi
wrangler() { npx --yes wrangler@4 "$@"; }

current=$(plutil -extract version raw mygo.json)
url=$(plutil -extract updates.url raw mygo.json)
bump=${1:-}

if [ -n "$bump" ]; then
	if ! echo "$bump" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
		echo "版本号要写成 x.y.z：$bump" >&2
		exit 1
	fi
	if [ "$(printf '%s\n%s\n' "$current" "$bump" | sort -V | tail -1)" != "$bump" ] || [ "$bump" = "$current" ]; then
		echo "$bump 要比现在的 $current 新" >&2
		exit 1
	fi
	# 只允许 CHANGELOG.md 没提交：它和版本号放在同一个提交里
	if [ -n "$(git status --porcelain | grep -v ' CHANGELOG.md$' || true)" ]; then
		echo "除了 CHANGELOG.md 还有未提交的改动，先提交再发布" >&2
		exit 1
	fi
	version=$bump
else
	if [ -n "$(git status --porcelain)" ]; then
		echo "工作区有未提交的改动，先提交再发布" >&2
		exit 1
	fi
	version=$current
	if ! grep -q "^const Version = \"$version\"\$" internal/daemon/daemon.go; then
		echo "internal/daemon/daemon.go 的 Version 和 mygo.json 的 $version 不一致" >&2
		exit 1
	fi
fi
if ! grep -q "^## $version\$" CHANGELOG.md; then
	echo "CHANGELOG.md 里没有 \"## $version\" 一节：它是更新时显示的更新内容" >&2
	exit 1
fi
published=$(curl -fsS "$url/update-darwin-arm64.json" 2>/dev/null | plutil -extract version raw - 2>/dev/null || true)
if [ -n "$published" ]; then
	newest=$(printf '%s\n%s\n' "$published" "$version" | sort -V | tail -1)
	if [ "$published" = "$version" ] || [ "$newest" != "$version" ]; then
		echo "线上已经是 ${published}，$version 要比它新" >&2
		exit 1
	fi
fi
# 打包、公证要一分钟，先确认能上传
if ! wrangler whoami >/dev/null 2>&1; then
	echo "wrangler 没有登录：先运行 npx wrangler login" >&2
	exit 1
fi

if [ -n "$bump" ]; then
	sed -i '' "s/^  \"version\": \"$current\",\$/  \"version\": \"$version\",/" mygo.json
	sed -i '' "s/^const Version = \"$current\"\$/const Version = \"$version\"/" internal/daemon/daemon.go
	if [ "$(plutil -extract version raw mygo.json)" != "$version" ] || ! grep -q "^const Version = \"$version\"\$" internal/daemon/daemon.go; then
		echo "改版本号失败，检查 mygo.json 和 internal/daemon/daemon.go" >&2
		exit 1
	fi
	git add mygo.json internal/daemon/daemon.go CHANGELOG.md
	git commit -q -m "chore(release): 发布 $version"
	git tag "v$version"
	echo "已提交并打标签 v$version"
fi

out=build/release/$version
rm -rf "$out"
MYGO_UPDATER_PRIVATE_KEY=$(cat "$key") go tool mygo build -platform darwin/arm64 -o "$out"
dir=$out/darwin-arm64

put() { # put <键> <文件> <类型> [缓存]
	wrangler r2 object put "$bucket/$1" --file "$2" --content-type "$3" ${4:+--cache-control "$4"} --remote
}

# 先传清单指向的包，最后传清单：客户端看到新清单时，它要下载的东西一定已经在了
for f in "$dir"/*.tar.gz; do
	put "$(basename "$f")" "$f" application/gzip
done
for f in "$dir"/*; do
	case $f in *.tar.gz | *.json | *.dmg | *.app) continue ;; esac
	put "$(basename "$f")" "$f" application/octet-stream
done
dmg=$(ls "$dir"/*.dmg)
put "Posse-$version-arm64.dmg" "$dmg" application/x-apple-diskimage
put Posse.dmg "$dmg" application/x-apple-diskimage no-cache
put update-darwin-arm64.json "$dir/update-darwin-arm64.json" application/json no-cache

echo "已发布 ${version}：$url/Posse.dmg"
echo "推送提交和标签：git push && git push origin v$version"
