package gitx

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func ValidBlameLine(line BlameLine, hashLength int) bool {
	if !ValidBlobOID(line.Hash) || len(line.Hash) != hashLength || line.Time.Year() < 0 || line.Time.Year() > 9999 {
		return false
	}
	for _, s := range []string{line.Author, line.Summary, line.Text} {
		if !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\n") {
			return false
		}
	}
	return true
}

func blameTime(s string) (time.Time, error) {
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, ErrBlameOutput
	}
	t := time.Unix(sec, 0)
	if t.Year() < 0 || t.Year() > 9999 {
		return time.Time{}, ErrBlameOutput
	}
	return t, nil
}
func blameTimezone(s string) bool {
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	h, _ := strconv.Atoi(s[1:3])
	m, _ := strconv.Atoi(s[3:])
	return h <= 23 && m <= 59
}
func blameFilename(s string) bool {
	if strings.HasPrefix(s, "\"") {
		var err error
		s, err = strconv.Unquote(s)
		if err != nil {
			return false
		}
	}
	return ValidBlameFile(s)
}

// line-porcelain 每行带完整元数据；不使用可能缺项的跨行缓存。
func parseBlameChecked(raw []byte, hashLength int) ([]BlameLine, error) {
	result := []BlameLine{}
	if len(raw) == 0 {
		return result, nil
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 || raw[len(raw)-1] != '\n' {
		return nil, ErrBlameOutput
	}
	records := strings.Split(string(raw[:len(raw)-1]), "\n")
	left, source := 0, 0
	groupHash := ""
	for i := 0; i < len(records); {
		fields := strings.Fields(records[i])
		i++
		if len(fields) != 3 && len(fields) != 4 {
			return nil, ErrBlameOutput
		}
		if !ValidBlobOID(fields[0]) || len(fields[0]) != hashLength {
			return nil, ErrBlameOutput
		}
		orig, e1 := strconv.Atoi(fields[1])
		final, e2 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || orig < 1 || final != len(result)+1 {
			return nil, ErrBlameOutput
		}
		if left == 0 {
			if len(fields) != 4 {
				return nil, ErrBlameOutput
			}
			count, err := strconv.Atoi(fields[3])
			if err != nil || count < 1 || count > BlameReadLineLimit-len(result) {
				return nil, ErrBlameOutput
			}
			left, source, groupHash = count, orig, fields[0]
		} else if len(fields) != 3 || orig != source || fields[0] != groupHash {
			return nil, ErrBlameOutput
		}
		meta := map[string]string{}
		text := ""
		found := false
		for i < len(records) {
			value := records[i]
			i++
			if strings.HasPrefix(value, "\t") {
				text = value[1:]
				found = true
				break
			}
			key, body, hasValue := strings.Cut(value, " ")
			if _, exists := meta[key]; exists {
				return nil, ErrBlameOutput
			}
			switch key {
			case "author", "author-mail", "author-time", "author-tz", "committer", "committer-mail", "committer-time", "committer-tz", "summary", "filename", "previous":
				if !hasValue {
					return nil, ErrBlameOutput
				}
			case "boundary", "ignored", "unblamable":
				if hasValue {
					return nil, ErrBlameOutput
				}
			default:
				return nil, ErrBlameOutput
			}
			meta[key] = body
		}
		if !found {
			return nil, ErrBlameOutput
		}
		for _, key := range []string{"author", "author-mail", "author-time", "author-tz", "committer", "committer-mail", "committer-time", "committer-tz", "summary", "filename"} {
			if _, ok := meta[key]; !ok {
				return nil, ErrBlameOutput
			}
		}
		for _, key := range []string{"author-mail", "committer-mail"} {
			v := meta[key]
			if !strings.HasPrefix(v, "<") || !strings.HasSuffix(v, ">") {
				return nil, ErrBlameOutput
			}
		}
		if !blameTimezone(meta["author-tz"]) || !blameTimezone(meta["committer-tz"]) || !blameFilename(meta["filename"]) {
			return nil, ErrBlameOutput
		}
		if previous, ok := meta["previous"]; ok {
			hash, name, ok := strings.Cut(previous, " ")
			if !ok || !ValidBlobOID(hash) || len(hash) != hashLength || !blameFilename(name) {
				return nil, ErrBlameOutput
			}
		}
		stamp, err := blameTime(meta["author-time"])
		if err != nil {
			return nil, err
		}
		if _, err := blameTime(meta["committer-time"]); err != nil {
			return nil, err
		}
		line := BlameLine{Hash: fields[0], Author: meta["author"], Time: stamp, Summary: meta["summary"], Text: text}
		if !ValidBlameLine(line, hashLength) {
			return nil, ErrBlameOutput
		}
		result = append(result, line)
		left--
		source++
	}
	if left != 0 {
		return nil, ErrBlameOutput
	}
	return result, nil
}
