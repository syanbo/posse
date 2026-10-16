package procmem

// TreeFootprint 是 pid 和它所有子孙进程的 phys_footprint 之和（字节）；children 来自 Children。
func TreeFootprint(pid int, children map[int][]int) uint64 {
	var sum uint64
	seen := map[int]bool{}
	stack := []int{pid}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		sum += Footprint(p)
		stack = append(stack, children[p]...)
	}
	return sum
}
