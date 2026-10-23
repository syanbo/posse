package files

import "testing"

func TestTypeOf(t *testing.T) {
	tests := []struct {
		from, text, name string
		line             int
		want             string
	}{
		{"a.ts", "const picker = new MediaPicker(opts);\npicker.openAlbum();", "picker", 1, "MediaPicker"},
		{"a.ts", "let picker: MediaPicker | null = null;\npicker.openAlbum();", "picker", 1, "MediaPicker"},
		{"a.ts", "const picker = MediaPicker.create();\npicker.openAlbum();", "picker", 1, "MediaPicker"},
		{"a.ts", "function f(picker: MediaPicker) {\n  picker.openAlbum();\n}", "picker", 1, "MediaPicker"},
		{"a.ts", "const x = 1;\n// picker = new Old()\npicker.openAlbum();", "picker", 2, ""},
		{"a.go", "p := NewPicker(cfg)\np.Open()", "p", 1, "Picker"},
		{"a.go", "p, err := media.NewPicker(cfg)\np.Open()", "p", 1, "Picker"},
		{"a.go", "var p *Picker\np.Open()", "p", 1, "Picker"},
		{"a.go", "p := &Picker{}\np.Open()", "p", 1, "Picker"},
		{"a.go", "func f(p *Picker) {\n\tp.Open()\n}", "p", 1, "Picker"},
		{"A.java", "MediaPicker picker = new MediaPicker();\npicker.openAlbum();", "picker", 1, "MediaPicker"},
		{"A.java", "void f(MediaPicker picker) {\n  picker.openAlbum();\n}", "picker", 1, "MediaPicker"},
		{"A.kt", "val picker = MediaPicker.getInstance()\npicker.openAlbum()", "picker", 1, "MediaPicker"},
		{"A.kt", "val picker: MediaPicker = get()\npicker.openAlbum()", "picker", 1, "MediaPicker"},
		{"a.dart", "final picker = MediaPicker();\npicker.openAlbum();", "picker", 1, "MediaPicker"},
		{"a.py", "picker = MediaPicker(opts)\npicker.open_album()", "picker", 1, "MediaPicker"},
		{"a.py", "picker: MediaPicker = get()\npicker.open_album()", "picker", 1, "MediaPicker"},
		{"a.cpp", "MediaPicker *picker = make();\npicker->open();", "picker", 1, "MediaPicker"},
		{"a.ts", "const Picker = new MediaPicker();\nPicker.open();", "Picker", 1, ""}, // 大写开头当成类型本身
		{"a.ts", "const n = 1;\nn.toFixed();", "n", 1, ""},
	}
	for _, tt := range tests {
		if got := TypeOf(tt.from, tt.text, tt.name, tt.line); got != tt.want {
			t.Errorf("TypeOf(%q, %q, %d) = %q, want %q", tt.from, tt.name, tt.line, got, tt.want)
		}
	}
}
