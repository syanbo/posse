package procmem

import (
	"bytes"
	"math"
	"strconv"
	"strings"
)

// parseSmapsRollup 从 Linux /proc/<pid>/smaps_rollup 解析 Pss（字节），缺失时退回 Rss。
func parseSmapsRollup(data []byte) uint64 {
	var pss, rss uint64
	for _, line := range strings.Split(string(data), "\n") {
		if k, rest, ok := strings.Cut(line, ":"); ok {
			switch strings.TrimSpace(k) {
			case "Pss":
				pss = parseProcKB(rest)
			case "Rss":
				rss = parseProcKB(rest)
			}
		}
	}
	if pss > 0 {
		return pss
	}
	return rss
}

// parseProcStatusRSS 从 Linux /proc/<pid>/status 解析 VmRSS（字节）。
func parseProcStatusRSS(data []byte) uint64 {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return parseProcKB(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return 0
}

func parseProcKB(raw string) uint64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	if len(fields) >= 2 && !strings.EqualFold(fields[1], "kb") {
		return 0
	}
	v, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || v > math.MaxUint64/1024 {
		return 0
	}
	return v * 1024
}

// parseProcChildren 解析 Linux /proc/<pid>/task/*/children 里以空白分隔的 PID 列表。
func parseProcChildren(data []byte) []int {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil
	}
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

// parseProcCmdline 按 NUL 分隔解析 Linux /proc/<pid>/cmdline，仅剥离末尾单个结束 NUL 以保留空尾部参数。
func parseProcCmdline(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	data = bytes.TrimSuffix(data, []byte{0})
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out
}

// parseProcExeLink 规范化 Linux /proc/<pid>/exe 的 readlink 目标（去掉内核追加的 " (deleted)" 后缀）。
func parseProcExeLink(target string) string {
	return strings.TrimSuffix(target, " (deleted)")
}
