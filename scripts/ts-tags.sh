#!/bin/sh
# 从 internal/tslink/tslink.go 读取 Mac 端裁剪构建标签（BuildTagsDarwin），
# 严格按 Go cmd/internal/quoted.Split + work/tagsFlag.Set 两层语法合并进实际生效的 GOFLAGS（含 GOENV），
# 原样保留非标签参数的原始 token（空白、引号、通配符等不被 shell 改写）。
# 被 scripts/dev-app.sh、scripts/release.sh 及源码构建说明共用，单一数据源避免漂移。

posse_ts_tags_darwin() {
	src="${1:-internal/tslink/tslink.go}"
	tags=$(sed -n 's/^[[:space:]]*BuildTagsDarwin[[:space:]]*=[[:space:]]*"\([^"]*\)"/\1/p' "$src")
	if [ -z "$tags" ]; then
		echo "读不到 $src 里的 BuildTagsDarwin" >&2
		return 1
	fi
	printf '%s\n' "$tags"
}

posse_ts_tags_linux() {
	src="${1:-internal/tslink/tslink.go}"
	tags=$(sed -n 's/^[[:space:]]*BuildTagsLinux[[:space:]]*=[[:space:]]*"\([^"]*\)"/\1/p' "$src")
	if [ -z "$tags" ]; then
		echo "读不到 $src 里的 BuildTagsLinux" >&2
		return 1
	fi
	printf '%s\n' "$tags"
}

# posse_merge_goflags_tags <已有 GOFLAGS> <要追加的 tags CSV>
# 严格遵循 Go cmd/internal/quoted.Split 与 cmd/go/internal/work.tagsFlag.Set 两层语法：
# 1. GOFLAGS 外层只把「token 开头」的 '...' 或 "..." 当作引号包裹（中间出现的引号按普通字符保留）；
# 2. 非 -tags=/--tags= 的 token 原样保留原始切片，不做 shell 分词或通配符展开；
# 3. 独立无值的 -tags 不吞掉后续参数；
# 4. -tags=/--tags= 的值若包含空格或单引号，按 quoted.Split 解析，否则按逗号拆项，
#    与追加标签去重合并为合法的 -tags=<csv>（若包含带空白或引号的标签则按 quoted.Join 整体包裹）。
posse_merge_goflags_tags() {
	POSSE_GOFLAGS_RAW="${1:-}" POSSE_GOFLAGS_ADD="${2:-}" awk '
function is_space(ch) {
	return ch == " " || ch == "\t" || ch == "\r" || ch == "\n"
}
# 与 Go cmd/internal/quoted.Split 完全一致：仅处理 token 起始处的单/双引号，内部不转义。
function quoted_split(s, vals, raws,    len, pos, n, start, q, i, ch) {
	len = length(s)
	pos = 1
	n = 0
	while (pos <= len) {
		while (pos <= len && is_space(substr(s, pos, 1))) pos++
		if (pos > len) break
		start = pos
		ch = substr(s, pos, 1)
		if (ch == sq || ch == dq) {
			q = ch
			pos++
			i = pos
			while (i <= len && substr(s, i, 1) != q) i++
			if (i > len) {
				print "posse_merge_goflags_tags: unterminated " q " string in GOFLAGS" > "/dev/stderr"
				exit 1
			}
			n++
			vals[n] = substr(s, pos, i - pos)
			raws[n] = substr(s, start, i - start + 1)
			pos = i + 1
		} else {
			i = pos
			while (i <= len && !is_space(substr(s, i, 1))) i++
			n++
			vals[n] = substr(s, pos, i - pos)
			raws[n] = vals[n]
			pos = i
		}
	}
	return n
}
function append_tag(t,    i, ch) {
	if (t == "") return
	# 拒绝含空白、单引号或逗号的异常标签值：合并成 CSV 时会触发 Go 的旧式空格/单引号解析分支或拆出额外标签，
	# 宁可报错停止构建，绝不静默丢掉 ts_omit_logtail。
	if (index(t, ",") > 0 || index(t, sq) > 0) {
		print "posse_merge_goflags_tags: unsupported tag value cannot be merged safely: " t > "/dev/stderr"
		exit 1
	}
	for (i = 1; i <= length(t); i++) {
		ch = substr(t, i, 1)
		if (is_space(ch)) {
			print "posse_merge_goflags_tags: unsupported tag with whitespace cannot be merged safely: " t > "/dev/stderr"
			exit 1
		}
	}
	if (!(t in seen_tag)) {
		seen_tag[t] = 1
		merged_tags = (merged_tags == "" ? t : merged_tags "," t)
	}
}
# 与 Go cmd/go/internal/work.tagsFlag.Set 完全一致：
# 含空格或单引号时走 quoted.Split（不二次按逗号拆分）；否则按逗号拆分。
function add_tags_value(val,    m, j, k, subvals, subraws, parts, np) {
	if (val == "") return
	if (index(val, " ") > 0 || index(val, sq) > 0) {
		m = quoted_split(val, subvals, subraws)
		for (j = 1; j <= m; j++) {
			append_tag(subvals[j])
		}
	} else {
		np = split(val, parts, ",")
		for (k = 1; k <= np; k++) append_tag(parts[k])
	}
}
function quote_if_needed(a,    has_sq, has_dq, need_q, i, ch) {
	has_sq = index(a, sq) > 0
	has_dq = index(a, dq) > 0
	need_q = 0
	for (i = 1; i <= length(a); i++) {
		ch = substr(a, i, 1)
		if (is_space(ch)) {
			need_q = 1
			break
		}
	}
	if (!need_q && substr(a, 1, 1) != sq && substr(a, 1, 1) != dq) return a
	if (!has_sq) return sq a sq
	if (!has_dq) return dq a dq
	print "posse_merge_goflags_tags: merged -tags cannot be quoted: " a > "/dev/stderr"
	exit 1
}
BEGIN {
	sq = sprintf("%c", 39)
	dq = sprintf("%c", 34)
	raw = ENVIRON["POSSE_GOFLAGS_RAW"]
	add = ENVIRON["POSSE_GOFLAGS_ADD"]

	ntok = quoted_split(raw, tok_val, tok_raw)
	out = ""
	merged_tags = ""
	for (i = 1; i <= ntok; i++) {
		t = tok_val[i]
		if (substr(t, 1, 6) == "-tags=") {
			add_tags_value(substr(t, 7))
		} else if (substr(t, 1, 7) == "--tags=") {
			add_tags_value(substr(t, 8))
		} else {
			out = (out == "" ? tok_raw[i] : out " " tok_raw[i])
		}
	}
	add_tags_value(add)
	if (merged_tags != "") {
		tag_arg = quote_if_needed("-tags=" merged_tags)
		out = (out == "" ? tag_arg : out " " tag_arg)
	}
	print out
}
'
}

# posse_apply_ts_tags [tslink.go 路径]
# 读取当前生效的 `go env GOFLAGS`（包含 shell GOFLAGS 与 GOENV 持久化配置），
# 合并 BuildTagsDarwin 后导出到 GOFLAGS。
posse_apply_ts_tags() {
	tags=$(posse_ts_tags_darwin "${1:-internal/tslink/tslink.go}") || return 1
	current=$(go env GOFLAGS) || return 1
	merged=$(posse_merge_goflags_tags "$current" "$tags") || return 1
	export GOFLAGS="$merged"
}
