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
# 裁剪内置 Tailscale 构建标签：从 go env GOFLAGS 合并 BuildTagsDarwin，mygo build 再与 mygo_noinspector 合并
. scripts/ts-tags.sh
posse_apply_ts_tags

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
pubkey=$(plutil -extract updates.publicKey raw mygo.json)
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
	if git rev-parse -q --verify "refs/tags/v$version" >/dev/null 2>&1; then
		if [ "$(git rev-parse "refs/tags/v$version^{commit}")" != "$(git rev-parse HEAD)" ]; then
			echo "当前 HEAD 与标签 v$version 不一致，禁止用不同代码补发同一版本" >&2
			exit 1
		fi
	fi
fi
if ! grep -q "^## $version\$" CHANGELOG.md; then
	echo "CHANGELOG.md 里没有 \"## $version\" 一节：它是更新时显示的更新内容" >&2
	exit 1
fi
# Apple 芯片和 Intel 各一个包、各一份清单（桌面端 + 无头服务端分别校验，清单最后传）。
# 线上清单已是这个版本的平台在重试时跳过，不重新打包覆盖清单签过的文件，只补没发完的平台。
archs=
server_archs=
manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT
for arch in arm64 amd64; do
	# 只有 404 算没发布过：网络出错时当成没发布，会把已发布的平台重新打包、覆盖清单签过的文件
	code=$(curl -sS -o "$manifest" -w '%{http_code}' "$url/update-darwin-$arch.json" || true)
	case $code in
	200) published=$(plutil -extract version raw "$manifest") ;;
	404) published= ;;
	*)
		echo "读不到线上的 update-darwin-$arch.json（HTTP ${code}）" >&2
		exit 1
		;;
	esac
	if [ -n "$published" ]; then
		newest=$(printf '%s\n%s\n' "$published" "$version" | sort -V | tail -1)
		if [ "$newest" != "$version" ] || { [ "$published" = "$version" ] && [ -n "$bump" ]; }; then
			echo "darwin-$arch 线上已经是 ${published}，$version 要比它新" >&2
			exit 1
		fi
		if [ "$published" = "$version" ]; then
			echo "darwin-$arch 桌面版已经发布了 ${version}，跳过"
		else
			archs="$archs $arch"
		fi
	else
		archs="$archs $arch"
	fi

	scode=$(curl -sS -o "$manifest" -w '%{http_code}' "$url/server/server-darwin-$arch.json" || true)
	case $scode in
	200)
		verified=$(go run ./tools/servermanifest --verify --manifest "$manifest" --os darwin --arch "$arch" --base-url "$url/server" --public-key "$pubkey") || {
			echo "线上 server/server-darwin-$arch.json 签名或格式无效，拒绝跳过" >&2
			exit 1
		}
		spublished=$(printf '%s\n' "$verified" | awk -F '\t' '{print $1}')
		sarchive_url=$(printf '%s\n' "$verified" | awk -F '\t' '{print $3}')
		;;
	404)
		spublished=
		sarchive_url=
		;;
	*)
		echo "读不到线上的 server/server-darwin-$arch.json（HTTP ${scode}）" >&2
		exit 1
		;;
	esac
	if [ -n "$spublished" ]; then
		snewest=$(printf '%s\n%s\n' "$spublished" "$version" | sort -V | tail -1)
		if [ "$snewest" != "$version" ] || { [ "$spublished" = "$version" ] && [ -n "$bump" ]; }; then
			echo "无头服务端 darwin-$arch 线上已经是 ${spublished}，$version 要比它新" >&2
			exit 1
		fi
		if [ "$spublished" = "$version" ]; then
			# 清单已验签且同版本，再确认其指向的归档对象在线上可达
			if curl -fsSI "$sarchive_url" >/dev/null 2>&1; then
				echo "无头服务端 darwin-$arch 已经发布了 ${version}，跳过"
			else
				echo "无头服务端 darwin-$arch 清单已为 ${version} 但归档不可访问（$sarchive_url），重新补发" >&2
				server_archs="$server_archs $arch"
			fi
		else
			server_archs="$server_archs $arch"
		fi
	else
		server_archs="$server_archs $arch"
	fi
done
if [ -z "$archs" ] && [ -z "$server_archs" ]; then
	echo "$version 两个平台桌面版与无头服务端都已发布" >&2
	exit 1
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
put() { # put <键> <文件> <类型> [缓存]
	wrangler r2 object put "$bucket/$1" --file "$2" --content-type "$3" ${4:+--cache-control "$4"} --remote
}

if [ -n "$archs" ]; then
	# 只清这次要重打的平台：重试时已发布平台的 dmg 还要留给 scripts/github-release.sh
	for arch in $archs; do
		rm -rf "$out/darwin-$arch"
	done
	# 更新清单的地址按平台写进包里，已装的 arm64 版照旧读 update-darwin-arm64.json
	platforms=$(for arch in $archs; do printf 'darwin/%s,' "$arch"; done)
	MYGO_UPDATER_PRIVATE_KEY=$(cat "$key") go tool mygo build -platform "${platforms%,}" -o "$out"

	# 先传清单指向的包，最后传清单：客户端看到新清单时，它要下载的东西一定已经在了
	for arch in $archs; do
		dir=$out/darwin-$arch
		for f in "$dir"/*.tar.gz; do
			put "$(basename "$f")" "$f" application/gzip
		done
		for f in "$dir"/*; do
			case $f in *.tar.gz | *.json | *.dmg | *.app | *.sh) continue ;; esac
			put "$(basename "$f")" "$f" application/octet-stream
		done
	done
	for arch in $archs; do
		dmg=$(ls "$out/darwin-$arch"/*.dmg)
		put "Posse-$version-$arch.dmg" "$dmg" application/x-apple-diskimage
		case $arch in
		arm64) put Posse.dmg "$dmg" application/x-apple-diskimage no-cache ;;
		amd64) put Posse-Intel.dmg "$dmg" application/x-apple-diskimage no-cache ;;
		esac
	done
	# install.sh 只由 mygo.json 生成，各平台的一样；它按本机架构读清单
	set -- $archs
	put install.sh "$out/darwin-$1/install.sh" application/octet-stream
	for arch in $archs; do
		put "update-darwin-$arch.json" "$out/darwin-$arch/update-darwin-$arch.json" application/json no-cache
	done
fi

if [ -n "$server_archs" ]; then
	commit=$(git rev-parse --short=7 HEAD)
	server_out="$out/server"
	mkdir -p "$server_out"
	server_failed=0
	publish_server_arch() {
		arch="$1"
		work=$(mktemp -d "${TMPDIR:-/tmp}/posse-release-server.XXXXXX")
		archive_name="posse-server-$version-darwin-$arch.tar.gz"
		archive="$work/$archive_name"
		smanifest="$work/server-darwin-$arch.json"
		if scripts/build-server.sh --require-signing --arch "$arch" --version "$version" --commit "$commit" --out "$work" &&
			MYGO_UPDATER_PRIVATE_KEY=$(cat "$key") go run ./tools/servermanifest \
				--archive "$archive" \
				--os darwin \
				--arch "$arch" \
				--version "$version" \
				--commit "$commit" \
				--url "$url/server/$archive_name" \
				--public-key "$pubkey" \
				--out "$smanifest" &&
			put "server/$archive_name" "$archive" application/gzip &&
			put "server/server-darwin-$arch.json" "$smanifest" application/json no-cache; then
			cp -f "$archive" "${archive}.sha256" "$smanifest" "$server_out/"
			rm -rf "$work"
			return 0
		fi
		rm -rf "$work"
		return 1
	}
	for arch in $server_archs; do
		if ! publish_server_arch "$arch"; then
			echo "无头服务端 darwin-$arch 发布失败，继续尝试其余架构" >&2
			server_failed=1
		fi
	done
	if [ "$server_failed" -ne 0 ]; then
		echo "部分无头服务端架构发布失败，请修复后不带参数重跑 scripts/release.sh 补发" >&2
		exit 1
	fi
fi

echo "已发布 ${version}：$url/Posse.dmg（Intel：$url/Posse-Intel.dmg）"
echo "推送提交和标签，再发 GitHub Release：git push && git push origin v$version && scripts/github-release.sh"
