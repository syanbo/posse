#!/bin/sh
# 量一个进程的内存：每秒取一次 footprint，共 N 次，按类别归成显存、Untagged、Malloc、其余，
# 打印并追加一行到 CSV（中位数那一次的拆分，加上总量的峰值）。内存验收看 phys_footprint（见 AGENTS.md）。
#
#   scripts/memprobe.sh <pid> <场景名> [次数，默认 10]
#
# CSV 默认写 /tmp/posse-mem/results.csv，MEMPROBE_OUT 可改；列是 MB。
set -eu
[ $# -ge 2 ] || { echo "用法: $0 <pid> <场景名> [次数]" >&2; exit 2; }
pid=$1 label=$2 n=${3:-10}
out=${MEMPROBE_OUT:-/tmp/posse-mem/results.csv}
mkdir -p "$(dirname "$out")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

i=0
while [ "$i" -lt "$n" ]; do
	footprint -p "$pid" -f bytes -j "$tmp/$i.json" >/dev/null 2>&1 || { echo "footprint 失败（pid $pid 还在吗）" >&2; exit 1; }
	i=$((i + 1))
	[ "$i" -lt "$n" ] && sleep 1
done

python3 -I - "$tmp" "$n" "$label" "$pid" "$out" <<'EOF'
import csv, json, os, statistics, sys
tmp, n, label, pid, out = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5]
MB = 1 << 20

def group(name):
    if name.startswith("IOSurface") or name.startswith("IOAccelerator") or "graphics" in name:
        return "gpu"
    if name == "Untagged":
        return "untagged"
    if name.startswith("Malloc"):
        return "malloc"
    return "other"

samples = []
for i in range(n):
    p = json.load(open(os.path.join(tmp, f"{i}.json")))["processes"][0]
    parts = {"gpu": 0, "untagged": 0, "malloc": 0, "other": 0}
    for name, v in p["categories"].items():
        parts[group(name)] += v.get("dirty", 0) + v.get("swapped", 0)
    samples.append((p["footprint"], parts))

totals = [t for t, _ in samples]
med = statistics.median_low(totals)
parts = next(p for t, p in samples if t == med)
row = [label, pid, f"{med / MB:.1f}", f"{max(totals) / MB:.1f}"] + [f"{parts[k] / MB:.1f}" for k in ("gpu", "untagged", "malloc", "other")]
new = not os.path.exists(out)
with open(out, "a", newline="") as f:
    w = csv.writer(f)
    if new:
        w.writerow(["scene", "pid", "median_mb", "max_mb", "gpu_mb", "untagged_mb", "malloc_mb", "other_mb"])
    w.writerow(row)
print("{:<28} 中位 {:>7} MB  峰值 {:>7} MB  | 显存 {:>6}  Untagged {:>6}  Malloc {:>6}  其余 {:>6}".format(label, *row[2:]))
EOF
