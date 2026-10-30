#!/usr/bin/env bash
# 把 Pele（Obsidian 开源的 Mermaid 渲染器，不需要浏览器）打包成 internal/mermaid/pele.js.gz：
# 一个 ES2017 脚本，由 QuickJS 运行，设置全局变量 Pele，开头带 Pele 的许可。需要 Node 的 npx。
# 用法：scripts/pele.sh [版本，默认 1.9.0]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:-1.9.0}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$TMP"
npm init -y >/dev/null
npm install --silent --no-audit --no-fund "pele@$VERSION"
{
	printf '/*! Pele %s, https://github.com/obsidianmd/pele\n' "$VERSION"
	sed 's/^/ * /; s/ *$//' node_modules/pele/LICENSE
	printf ' */\n'
} >banner.js
npx --yes esbuild@0.25.0 node_modules/pele/dist/pele.js --bundle --format=iife --global-name=Pele \
	--target=es2017 --minify --legal-comments=none --outfile=pele.js
cat banner.js pele.js | gzip -9n >"$ROOT/internal/mermaid/pele.js.gz"
echo "Pele $VERSION in internal/mermaid/pele.js.gz ($(wc -c <"$ROOT/internal/mermaid/pele.js.gz" | tr -d ' ') bytes)"
