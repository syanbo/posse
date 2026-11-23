package media

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestZCodeArtifactMedia(t *testing.T) {
	const sid = "sess_01234567-89ab-4cde-8f01-23456789abcd"
	const id = "-tool-result-01234567-89ab-4cde-8f01-23456789abcd"
	storage := t.TempDir()
	t.Setenv("ZCODE_STORAGE_DIR", storage)
	dir := filepath.Join(storage, "cli", "artifacts", sid)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	clipboard := filepath.Join(dir, "prompt-attachment-1-fixture"+id+".txt")
	encoded, original := pngBase64(t, 8, 5)
	if err := os.WriteFile(clipboard, []byte("data:image/png;base64,"+encoded), 0600); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(dir, "screenshot"+id+".png"), 4, 2)
	writePNG(t, filepath.Join(dir, "unrelated.png"), 4, 2)
	if err := os.Symlink(clipboard, filepath.Join(dir, "prompt-attachment-linked"+id+".txt")); err != nil {
		t.Fatal(err)
	}
	s := NewScanner("zcode", sid, "")
	pasted, made, err := s.ScanContext(t.Context())
	if err != nil || !s.Found() || len(pasted) != 1 || len(made) != 1 {
		t.Fatalf("unexpected media index: %v, %v, %v", pasted, made, err)
	}
	if pasted[0].Mime() != "image/png" || pasted[0].Tag != TagPasted {
		t.Fatalf("unexpected clipboard metadata: %+v", pasted[0])
	}
	data, err := ReadInlineImage(t.Context(), pasted[0])
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("did not recover clipboard image: %v", err)
	}
	thumb, err := Decode(pasted[0], 32)
	if err != nil || thumb.Bounds().Dx() != 8 || thumb.Bounds().Dy() != 5 {
		t.Fatalf("could not preview clipboard artifact: %v", err)
	}
	other := NewScanner("zcode", "sess_01234567-89ab-4cde-8f01-23456789abce", "")
	if p, m := other.Scan(); other.Found() || len(p)+len(m) != 0 {
		t.Fatal("media leaked between sessions")
	}
}

func TestZCodeForkMediaAndInlineVideo(t *testing.T) {
	const source = "sess_01234567-89ab-4cde-8f01-23456789abcd"
	const fork = "sess_01234567-89ab-4cde-8f01-23456789abce"
	const videoID = "tool-result-01234567-89ab-4cde-8f01-23456789abcd"
	tmp := t.TempDir()
	t.Setenv("ZCODE_STORAGE_DIR", tmp)
	database := filepath.Join(tmp, "zcode.db")
	t.Setenv("ZCODE_SESSION_DB_PATH", database)
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE part (session_id TEXT, data TEXT)`); err != nil {
		t.Fatal(err)
	}
	part := `{"type":"tool","state":{"attachments":[{"url":"zcode-artifact://` + source + "/" + videoID + `"}]}}`
	if _, err := db.Exec(`INSERT INTO part VALUES (?, ?)`, fork, part); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "cli", "artifacts", source)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("0123456789")
	if err := os.WriteFile(filepath.Join(dir, "video-"+videoID+".txt"), []byte("data:video/mp4;base64,"+base64.StdEncoding.EncodeToString(data)), 0600); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(dir, "unreferenced-tool-result-01234567-89ab-4cde-8f01-23456789abce.png"), 1, 1)
	s := NewScanner("zcode", fork, "")
	_, made, err := s.ScanContext(t.Context())
	if err != nil || !s.Found() || len(made) != 1 || !made[0].Video || filepath.Ext(made[0].Label) != ".mp4" {
		t.Fatalf("fork media = %v, %v", made, err)
	}
	v, err := OpenInlineVideo(made[0])
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	buf := make([]byte, 5)
	if n, err := v.ReadAt(buf, 7); n != 3 || err != io.EOF || !bytes.Equal(buf[:n], data[7:]) || v.Size() != int64(len(data)) {
		t.Fatalf("decoded range = %q, %v", buf[:n], err)
	}
	path, err := WriteInlineVideo(t.Context(), made[0], tmp)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, data) || filepath.Ext(path) != ".mp4" {
		t.Fatalf("decoded file = %q, %v", raw, err)
	}
}
