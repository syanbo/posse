#!/bin/sh
# 讨论页的内存采样：只读 PosseDev 窗口和它的常驻进程的 phys_footprint，不碰界面、不碰日常的 Posse。
# 操作界面（切页、往前翻页、打开长讨论）由人或界面自动化来做；每个阶段做完按回车（或等 --wait 秒），
# 脚本对两个进程各跑一次 scripts/memprobe.sh（每秒一次、取中位数），结果追加到 CSV。
#
#   scripts/discmem.sh [--wait 秒] [--samples N] 阶段名...
#   例：scripts/discmem.sh idle-workspace disc-open page-back-5 page-back-20 switch-away switch-back
#
# CSV 默认 /tmp/posse-mem/discussions.csv（MEMPROBE_OUT 可改）。比较同一窗口进程各阶段的中位数：
# 打开讨论只取最新一页，往前翻页是按需读的，翻完切走再切回应当回落到同一量级，不该随翻过的页数一直涨。
set -eu
wait="" samples=10
while [ $# -gt 0 ]; do
	case $1 in
	--wait) wait=$2; shift 2 ;;
	--samples) samples=$2; shift 2 ;;
	*) break ;;
	esac
done
[ $# -ge 1 ] || { echo "用法: $0 [--wait 秒] [--samples N] 阶段名..." >&2; exit 2; }
cd "$(dirname "$0")/.."
export MEMPROBE_OUT=${MEMPROBE_OUT:-/tmp/posse-mem/discussions.csv}

win=$(pgrep -f "PosseDev.app/Contents/MacOS/Posse$" | head -1 || true)
[ -n "$win" ] || { echo "没找到 PosseDev 窗口进程（先用 scripts/dev-app.sh --open 打开）" >&2; exit 1; }
dpid=$(cat "$HOME/.posse-dev/possed.pid" 2>/dev/null || true)
for p in $win $dpid; do
	# 只采开发包：日常的 Posse.app 和它的常驻进程不碰
	case $(ps -o command= -p "$p") in
	*com.dengbo.posse.dev* | *PosseDev.app*) ;;
	*) echo "pid ${p} 不是 PosseDev 的进程，拒绝采样" >&2; exit 1 ;;
	esac
done
echo "窗口 pid ${win}，常驻进程 pid ${dpid:-?}；结果写 ${MEMPROBE_OUT}"
for phase in "$@"; do
	if [ -n "$wait" ]; then
		echo "阶段 ${phase}：等 ${wait} 秒"
		sleep "$wait"
	else
		printf "阶段 %s：做好这一步后按回车 " "$phase"
		read -r _
	fi
	kill -0 "$win" 2>/dev/null || { echo "窗口进程 ${win} 不在了：同一进程的对比中断" >&2; exit 1; }
	sh scripts/memprobe.sh "$win" "window:$phase" "$samples"
	if [ -n "$dpid" ]; then sh scripts/memprobe.sh "$dpid" "daemon:$phase" "$samples"; fi
done
