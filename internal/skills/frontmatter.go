package skills

import (
	"strconv"
	"strings"
)

// Meta 是 SKILL.md 开头 YAML 头部里技能管理用得到的几项。
type Meta struct {
	Name        string
	Description string
	// ModelOff 是 disable-model-invocation: true：模型不会自己调用，只能用户用斜杠命令调。
	ModelOff bool
	// UserHidden 是 user-invocable: false：不出现在斜杠命令里，只由模型调用。
	UserHidden bool
	// Posse 是 Posse 自带、由 Posse 装的技能（metadata 里有它的标记，见 posse.go）。
	Posse bool
}

// parseMeta 读 SKILL.md 的头部。只认顶层的 键: 值，值可以带引号、写成 | 或 > 块，或者接着缩进折行；
// 嵌套的键和列表跳过。没有头部时返回零值。
func parseMeta(src string) Meta {
	src = strings.TrimPrefix(src, "\ufeff")
	rest, ok := strings.CutPrefix(src, "---\n")
	if !ok {
		rest, ok = strings.CutPrefix(src, "---\r\n")
	}
	if !ok {
		return Meta{}
	}
	lines := strings.Split(rest, "\n")
	fields := map[string]string{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if line == "---" || line == "..." {
			break
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, kv := strings.Cut(line, ":")
		if !kv || key == "" || strings.ContainsAny(key, " \t\"'") {
			continue
		}
		value = strings.TrimSpace(value)
		// 后面缩进的行属于这一项
		var cont []string
		for i+1 < len(lines) {
			next := strings.TrimRight(lines[i+1], "\r")
			if next != "" && next[0] != ' ' && next[0] != '\t' {
				break
			}
			cont = append(cont, next)
			i++
		}
		fields[key] = yamlValue(value, cont)
	}
	return Meta{
		Name:        fields["name"],
		Description: fields["description"],
		ModelOff:    fields["disable-model-invocation"] == "true",
		UserHidden:  fields["user-invocable"] == "false",
		Posse:       isPosseFile(src),
	}
}

// yamlValue 拼出一项的值：| 保留换行，> 和普通折行把各行用空格接起来。
func yamlValue(head string, cont []string) string {
	if strings.HasPrefix(head, "|") || strings.HasPrefix(head, ">") {
		keep := head[0] == '|'
		indent := -1
		var out []string
		for _, l := range cont {
			t := strings.TrimLeft(l, " \t")
			if t == "" {
				out = append(out, "")
				continue
			}
			if indent < 0 {
				indent = len(l) - len(t)
			}
			if len(l)-len(t) > indent {
				t = l[indent:]
			}
			out = append(out, t)
		}
		sep := " "
		if keep {
			sep = "\n"
		}
		return strings.TrimSpace(strings.Join(out, sep))
	}
	parts := []string{head}
	for _, l := range cont {
		if t := strings.TrimSpace(l); t != "" {
			parts = append(parts, t)
		}
	}
	return unquote(strings.TrimSpace(strings.Join(parts, " ")))
}

// unquote 去掉 YAML 值的引号；双引号里的转义照 Go 的规则解开，解不开的原样留着。
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}
