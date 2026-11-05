#!/bin/sh
# 把一个已发布的 Mac 版发成 GitHub Release：说明取 CHANGELOG.md 的这一节，附上两个平台的 dmg。
# 在 scripts/release.sh 发布、推送提交和标签之后运行；重复运行只更新说明、覆盖附件。
#
#   scripts/github-release.sh          mygo.json 里现在的版本
#   scripts/github-release.sh 0.2.0
set -eu
cd "$(dirname "$0")/.."

version=${1:-$(plutil -extract version raw mygo.json)}
tag=v$version
out=build/release/$version

if ! git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null; then
	echo "远端没有标签 $tag：先 git push && git push origin $tag" >&2
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# 附件改名成和下载站一样的 Posse-<版本>-<架构>.dmg
for arch in arm64 amd64; do
	dmg=$(ls "$out/darwin-$arch"/*.dmg 2>/dev/null || true)
	if [ -z "$dmg" ]; then
		echo "缺 $out/darwin-$arch 的 dmg：要在发布这一版的机器上运行" >&2
		exit 1
	fi
	cp "$dmg" "$work/Posse-$version-$arch.dmg"
done

notes=$work/notes.md
{
	echo "Apple 芯片下载 \`Posse-$version-arm64.dmg\`，Intel 芯片下载 \`Posse-$version-amd64.dmg\`。"
	echo "Download \`Posse-$version-arm64.dmg\` for Apple silicon or \`Posse-$version-amd64.dmg\` for Intel."
	echo
	awk -v head="## $version" '$0 == head { on = 1; next } on && /^## / { exit } on' CHANGELOG.md
} >"$notes"
if [ "$(grep -c . "$notes")" -le 2 ]; then
	echo "CHANGELOG.md 里没有 \"## $version\" 一节" >&2
	exit 1
fi

if gh release view "$tag" >/dev/null 2>&1; then
	gh release edit "$tag" --title "Posse $version" --notes-file "$notes"
	gh release upload "$tag" "$work"/*.dmg --clobber
else
	gh release create "$tag" --verify-tag --title "Posse $version" --notes-file "$notes" "$work"/*.dmg
fi
gh release view "$tag" --json url -q .url
