package history

import (
	"path/filepath"
	"strings"
)

// WriteTargets 从一条 shell 命令里猜出它会写到哪些路径，给痕迹账本用：输出重定向、mkdir、touch、
// cp/mv 这类的目标、tee、各程序的输出选项。猜不全也会有多余的，调用方只认真的存在、又在工作区外的路径。
// 命令里先 cd 到绝对路径的，后面的相对路径按它拼上；没有 cd 的相对路径原样返回，由调用方按 agent 的目录处理。
// 不展开变量，~ 和 $HOME 原样留给调用方。
func WriteTargets(cmd string) []string {
	var out []string
	writeTargets(shLex(cmd), "", 0, &out)
	return out
}

func writeTargets(toks []shToken, dir string, depth int, out *[]string) {
	add := func(p string) {
		if p == "" || p == "-" || strings.HasPrefix(p, "/dev/") {
			return
		}
		if dir != "" && !filepath.IsAbs(p) && !homeRelative(p) {
			p = filepath.Join(dir, p)
		}
		*out = append(*out, p)
	}
	var words []string
	flushCmd := func() {
		if len(words) > 0 {
			if d, ok := cdTarget(words); ok {
				dir = d
			} else if script, ok := shellScript(words); ok && depth < 3 {
				writeTargets(shLex(script), dir, depth+1, out)
			} else {
				for _, p := range commandTargets(words) {
					add(p)
				}
			}
		}
		words = words[:0]
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.sub != "":
			if depth < 3 {
				writeTargets(shLex(t.sub), dir, depth+1, out)
			}
		case t.op == "redir":
			if i+1 < len(toks) && toks[i+1].word != "" {
				if !t.in {
					add(toks[i+1].word)
				}
				i++
			}
		case t.op != "":
			flushCmd()
		default:
			words = append(words, t.word)
		}
	}
	flushCmd()
}

func homeRelative(p string) bool {
	return p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "$HOME") || strings.HasPrefix(p, "${HOME}")
}

// cdTarget 认出 cd 到绝对路径（或 ~ 下）的命令。
func cdTarget(words []string) (string, bool) {
	if len(words) == 2 && words[0] == "cd" && (filepath.IsAbs(words[1]) || homeRelative(words[1])) {
		return words[1], true
	}
	return "", false
}

// shellScript 认出 sh/bash/zsh -c '脚本'。
func shellScript(words []string) (string, bool) {
	words = stripWrappers(words)
	if len(words) < 3 {
		return "", false
	}
	switch filepath.Base(words[0]) {
	case "sh", "bash", "zsh", "dash":
	default:
		return "", false
	}
	for i := 1; i+1 < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "c") {
			return words[i+1], true
		}
		if !strings.HasPrefix(w, "-") {
			break
		}
	}
	return "", false
}

// stripWrappers 去掉开头的变量赋值和 sudo、env、nohup 这类包装程序（连同它们的选项）。
func stripWrappers(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		if assignmentRe.MatchString(w) {
			words = words[1:]
			continue
		}
		opts, ok := cliWrappers[w]
		if !ok {
			return words
		}
		words = words[1:]
		for len(words) > 0 && strings.HasPrefix(words[0], "-") {
			if opts[words[0]] && len(words) > 1 {
				words = words[1:]
			}
			words = words[1:]
		}
		if (w == "timeout" || w == "gtimeout") && len(words) > 0 && durationRe.MatchString(words[0]) {
			words = words[1:]
		}
	}
	return words
}

// outputFlags 是各程序指定输出路径的选项（后面跟一个路径）。
var outputFlags = map[string][]string{
	"curl":       {"-o", "--output", "--output-dir"},
	"wget":       {"-O", "--output-document", "-P", "--directory-prefix"},
	"go":         {"-o"},
	"gcc":        {"-o"},
	"clang":      {"-o"},
	"cc":         {"-o"},
	"swiftc":     {"-o"},
	"rustc":      {"-o"},
	"pandoc":     {"-o", "--output"},
	"yt-dlp":     {"-o", "--output", "-P", "--paths"},
	"unzip":      {"-d"},
	"tar":        {"-C", "--directory"},
	"sips":       {"--out", "-o"},
	"xcodebuild": {"-archivePath", "-exportPath", "-resultBundlePath"},
}

// lastArgWrites 是最后一个参数是目标的程序（cp 源... 目标）。
var lastArgWrites = map[string]bool{
	"cp": true, "mv": true, "rsync": true, "ditto": true, "install": true, "ln": true, "scp": true,
	"ffmpeg": true, "magick": true, "convert": true, "screencapture": true,
}

// allArgWrites 是每个非选项参数都是目标的程序。
var allArgWrites = map[string]bool{"mkdir": true, "touch": true, "tee": true}

func commandTargets(words []string) []string {
	words = stripWrappers(words)
	if len(words) == 0 {
		return nil
	}
	prog := filepath.Base(words[0])
	args := words[1:]
	var out []string
	var positional []string
	flags := outputFlags[prog]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			name, val, hasVal := strings.Cut(a, "=")
			for _, f := range flags {
				if name != f {
					continue
				}
				if hasVal {
					out = append(out, val)
				} else if i+1 < len(args) {
					out = append(out, args[i+1])
					i++
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	switch {
	case allArgWrites[prog]:
		out = append(out, positional...)
	case lastArgWrites[prog] && len(positional) >= 1:
		// cp、mv 至少要有源和目标；ffmpeg、screencapture 的输出就是最后一个
		if len(positional) >= 2 || prog == "screencapture" || prog == "ffmpeg" || prog == "magick" || prog == "convert" {
			out = append(out, positional[len(positional)-1])
		}
	case prog == "adb":
		// adb [-s 设备] pull 源 目标
		for i, a := range positional {
			if a == "pull" && len(positional)-i >= 3 {
				out = append(out, positional[len(positional)-1])
			}
		}
	case prog == "zip" && len(positional) >= 1:
		out = append(out, positional[0])
	}
	return out
}
