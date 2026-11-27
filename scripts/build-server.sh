#!/bin/sh
# 构建 Mac 无头服务端压缩包（供 SSH 远程自动推送安装，无需目标 Mac 安装 Posse.app）：
#
#   scripts/build-server.sh                 打当前本机架构（darwin/arm64 或 darwin/amd64）
#   scripts/build-server.sh --all           同时打 darwin/arm64 和 darwin/amd64
#   scripts/build-server.sh --arch arm64    指定架构
#   scripts/build-server.sh --adhoc         强制使用 ad-hoc 签名（无证书开发测试用）
#
# 产物输出到 build/server/posse-server-<版本>-darwin-<架构>.tar.gz（另附 .sha256）。
set -eu
cd "$(dirname "$0")/.."
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

. scripts/ts-tags.sh

archs=""
force_adhoc=0
out_dir="build/server"

while [ $# -gt 0 ]; do
	case "$1" in
	--all)
		archs="arm64 amd64"
		shift
		;;
	--arch)
		archs="$archs $2"
		shift 2
		;;
	--adhoc)
		force_adhoc=1
		shift
		;;
	--out)
		out_dir="$2"
		shift 2
		;;
	-h|--help)
		sed -n '2,10p' "$0"
		exit 0
		;;
	*)
		echo "未知参数: $1" >&2
		exit 2
		;;
	esac
done

if [ -z "$archs" ]; then
	case "$(uname -m)" in
	arm64|aarch64) archs="arm64" ;;
	x86_64|amd64) archs="amd64" ;;
	*)
		echo "不支持的本机架构: $(uname -m)" >&2
		exit 1
		;;
	esac
fi

version=$(plutil -extract version raw mygo.json)
commit=$(git rev-parse --short=7 HEAD 2>/dev/null || echo "dev")
ts_tags=$(posse_ts_tags_darwin)
tags="${ts_tags},headless"
identity=$(plutil -extract macos.signingIdentity raw mygo.json 2>/dev/null || true)

use_dev_id=0
if [ "$force_adhoc" -eq 0 ] && [ -n "$identity" ]; then
	if security find-identity -v -p codesigning 2>/dev/null | grep -qi "$identity"; then
		use_dev_id=1
	else
		echo "提示: 本机钥匙串未找到签名证书 $identity，回退 ad-hoc 签名（如需跨升级保留 TCC 授权请配置 Developer ID 证书）" >&2
	fi
fi

mkdir -p "$out_dir"

find_or_fetch_dylib() {
	arch="$1"
	dest="$2"
	manifest="third_party/mygo/plugins/terminal/mygo-plugin.json"
	sha=$(plutil -extract "libraries.0.files.darwin-${arch}.sha256" raw "$manifest")
	url=$(plutil -extract "libraries.0.files.darwin-${arch}.url" raw "$manifest")
	name=$(plutil -extract "libraries.0.files.darwin-${arch}.name" raw "$manifest")
	cache_file="$HOME/Library/Caches/mygo/natives/${sha}/${name}"

	# 始终用 mygo-plugin.json 里的原始 SHA-256 核验未重签原件，避免误用损坏缓存或旧 App 副本
	if [ -f "$cache_file" ]; then
		got=$(shasum -a 256 "$cache_file" | awk '{print $1}')
		if [ "$got" = "$sha" ]; then
			cp "$cache_file" "$dest"
			return 0
		fi
		echo "警告: 本地缓存 ${cache_file} 校验和不匹配，重新下载..." >&2
		rm -f "$cache_file"
	fi
	mkdir -p "$(dirname "$cache_file")"
	tmp_dl="${cache_file}.tmp.$$"
	echo "下载 darwin-${arch} 的 ${name}..." >&2
	curl -fsSL -o "$tmp_dl" "$url"
	got=$(shasum -a 256 "$tmp_dl" | awk '{print $1}')
	if [ "$got" != "$sha" ]; then
		rm -f "$tmp_dl"
		echo "SHA-256 校验失败: got $got, want $sha" >&2
		exit 1
	fi
	mv -f "$tmp_dl" "$cache_file"
	cp "$cache_file" "$dest"
}

for arch in $archs; do
	stage=$(mktemp -d "${TMPDIR:-/tmp}/posse-server-stage.XXXXXX")
	trap 'rm -rf "$stage"' EXIT

	echo "构建 headless posse (darwin/$arch, version=$version, commit=$commit)..."
	CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build \
		-tags "$tags" \
		-ldflags "-s -w -X posse/internal/daemon.Commit=$commit" \
		-o "$stage/posse" \
		./cmd/posse

	find_or_fetch_dylib "$arch" "$stage/libghostty-vt.dylib"

	if [ "$use_dev_id" -eq 1 ]; then
		codesign --force --sign "$identity" --timestamp "$stage/libghostty-vt.dylib"
		codesign --force --sign "$identity" --options runtime --timestamp --identifier com.dengbo.posse.server "$stage/posse"
	else
		codesign --force --sign - "$stage/libghostty-vt.dylib"
		codesign --force --sign - --identifier com.dengbo.posse.server "$stage/posse"
	fi

	# 签后逐项核验签名与目标架构，避免打包损坏或错架构产物
	codesign --verify --strict "$stage/libghostty-vt.dylib"
	codesign --verify --strict "$stage/posse"
	want_lipo="$arch"
	[ "$arch" = "amd64" ] && want_lipo="x86_64"
	for f in "$stage/posse" "$stage/libghostty-vt.dylib"; do
		got_arch=$(lipo -archs "$f")
		if [ "$got_arch" != "$want_lipo" ]; then
			echo "架构校验失败: $f = $got_arch, want $want_lipo" >&2
			exit 1
		fi
	done

	posse_sha=$(shasum -a 256 "$stage/posse" | awk '{print $1}')
	dylib_sha=$(shasum -a 256 "$stage/libghostty-vt.dylib" | awk '{print $1}')
	cat >"$stage/manifest.json" <<EOF
{
  "version": "$version",
  "commit": "$commit",
  "os": "darwin",
  "arch": "$arch",
  "edition": "headless",
  "sha256": {
    "posse": "$posse_sha",
    "libghostty-vt.dylib": "$dylib_sha"
  }
}
EOF

	archive="$out_dir/posse-server-${version}-darwin-${arch}.tar.gz"
	tmp_archive="${archive}.tmp.$$"
	COPYFILE_DISABLE=1 tar -czf "$tmp_archive" -C "$stage" posse libghostty-vt.dylib manifest.json
	mv -f "$tmp_archive" "$archive"
	shasum -a 256 "$archive" | awk '{print $1}' >"${archive}.sha256"
	rm -rf "$stage"
	trap - EXIT

	echo "已生成: $archive ($(du -h "$archive" | awk '{print $1}'))"
done
