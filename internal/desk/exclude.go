package desk

// pickExcluded 按 bundle id 从正在运行的应用里挑出要排除的下标。
// want 里没出现的 id 放进 missing，同一个 id 写了多次只报一次。
func pickExcluded(ids, want []string) (hit []int, missing []string) {
	wantSet := make(map[string]struct{}, len(want))
	for _, id := range want {
		if id != "" {
			wantSet[id] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(wantSet))
	for i, id := range ids {
		if _, ok := wantSet[id]; !ok {
			continue
		}
		hit = append(hit, i)
		seen[id] = struct{}{}
	}
	reported := make(map[string]struct{}, len(want))
	for _, id := range want {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if _, ok := reported[id]; ok {
			continue
		}
		reported[id] = struct{}{}
		missing = append(missing, id)
	}
	return hit, missing
}
