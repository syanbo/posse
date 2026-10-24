package desk

import "testing"

func TestPickExcluded(t *testing.T) {
	hit, missing := pickExcluded(
		[]string{"com.apple.finder", "com.apple.iphonesimulator", "com.apple.iphonesimulator", "other"},
		[]string{"com.apple.iphonesimulator", "com.apple.iphonesimulator", "com.missing"},
	)
	if len(hit) != 2 || hit[0] != 1 || hit[1] != 2 {
		t.Fatalf("hit %v", hit)
	}
	if len(missing) != 1 || missing[0] != "com.missing" {
		t.Fatalf("missing %v", missing)
	}
	hit, missing = pickExcluded(nil, []string{"com.apple.iphonesimulator"})
	if len(hit) != 0 || len(missing) != 1 {
		t.Fatalf("empty list hit %v missing %v", hit, missing)
	}
}
