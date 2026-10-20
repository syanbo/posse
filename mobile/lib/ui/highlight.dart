import 'dart:isolate';

import 'package:flutter/material.dart';
import 'package:re_highlight/languages/bash.dart';
import 'package:re_highlight/languages/c.dart';
import 'package:re_highlight/languages/cmake.dart';
import 'package:re_highlight/languages/cpp.dart';
import 'package:re_highlight/languages/csharp.dart';
import 'package:re_highlight/languages/css.dart';
import 'package:re_highlight/languages/dart.dart';
import 'package:re_highlight/languages/diff.dart';
import 'package:re_highlight/languages/dockerfile.dart';
import 'package:re_highlight/languages/go.dart';
import 'package:re_highlight/languages/gradle.dart';
import 'package:re_highlight/languages/graphql.dart';
import 'package:re_highlight/languages/groovy.dart';
import 'package:re_highlight/languages/ini.dart';
import 'package:re_highlight/languages/java.dart';
import 'package:re_highlight/languages/javascript.dart';
import 'package:re_highlight/languages/json.dart';
import 'package:re_highlight/languages/kotlin.dart';
import 'package:re_highlight/languages/less.dart';
import 'package:re_highlight/languages/lua.dart';
import 'package:re_highlight/languages/makefile.dart';
import 'package:re_highlight/languages/markdown.dart';
import 'package:re_highlight/languages/nginx.dart';
import 'package:re_highlight/languages/objectivec.dart';
import 'package:re_highlight/languages/php.dart';
import 'package:re_highlight/languages/properties.dart';
import 'package:re_highlight/languages/protobuf.dart';
import 'package:re_highlight/languages/python.dart';
import 'package:re_highlight/languages/ruby.dart';
import 'package:re_highlight/languages/rust.dart';
import 'package:re_highlight/languages/scss.dart';
import 'package:re_highlight/languages/shell.dart';
import 'package:re_highlight/languages/sql.dart';
import 'package:re_highlight/languages/swift.dart';
import 'package:re_highlight/languages/typescript.dart';
import 'package:re_highlight/languages/vue.dart';
import 'package:re_highlight/languages/xml.dart';
import 'package:re_highlight/languages/yaml.dart';
import 'package:re_highlight/re_highlight.dart';

import 'theme.dart';

/// 代码里一段文字的种类，配色和桌面编辑器（mygo 的 GitHub 配色）一一对应。
/// [plain] 是明确不着色（字符串里的插值），和"沿用外层"的 null 区分开。
enum Tok {
  plain,
  keyword,
  type,
  function,
  property,
  constant,
  string,
  regexp,
  tag,
  comment,
  heading,
  inserted,
  deleted,
  strong,
  emphasis,
}

/// 一行里的一段：文字和种类，种类为空是普通文字。
typedef Seg = (String, Tok?);

/// 着色结果按行切开，行数和 `text.split('\n')` 一致。
typedef HlLines = List<List<Seg>>;

/// 超过这么大的内容不着色：手机上一次排版几十万个片段会卡。
const highlightLimit = 256 * 1024;

/// 超过这么大的内容放到后台 isolate 里着色，先显示不带颜色的文字。
const _isolateFrom = 16 * 1024;

/// 每个 isolate 各有一份（顶层变量按 isolate 初始化）。
final Highlight _hl = Highlight()
  ..registerLanguages({
    'bash': langBash,
    'c': langC,
    'cmake': langCmake,
    'cpp': langCpp,
    'csharp': langCsharp,
    'css': langCss,
    'dart': langDart,
    'diff': langDiff,
    'dockerfile': langDockerfile,
    'go': langGo,
    'gradle': langGradle,
    'graphql': langGraphql,
    'groovy': langGroovy,
    'ini': langIni,
    'java': langJava,
    'javascript': langJavascript,
    'json': langJson,
    'kotlin': langKotlin,
    'less': langLess,
    'lua': langLua,
    'makefile': langMakefile,
    'markdown': langMarkdown,
    'nginx': langNginx,
    'objectivec': langObjectivec,
    'php': langPhp,
    'properties': langProperties,
    'protobuf': langProtobuf,
    'python': langPython,
    'ruby': langRuby,
    'rust': langRust,
    'scss': langScss,
    'shell': langShell,
    'sql': langSql,
    'swift': langSwift,
    'typescript': langTypescript,
    'vue': langVue,
    'xml': langXml,
    'yaml': langYaml,
  });

/// 按文件名认语言，认不出返回 null（不着色，不做自动猜测）。
String? languageForPath(String path) {
  final name = path.substring(path.lastIndexOf('/') + 1);
  final byName = _byName[name];
  if (byName != null) return byName;
  final dot = name.lastIndexOf('.');
  if (dot < 0) return null;
  return _byExt[name.substring(dot + 1).toLowerCase()];
}

const _byName = {
  'Dockerfile': 'dockerfile',
  'Containerfile': 'dockerfile',
  'Makefile': 'makefile',
  'makefile': 'makefile',
  'GNUmakefile': 'makefile',
  'CMakeLists.txt': 'cmake',
  'Podfile': 'ruby',
  'Gemfile': 'ruby',
  'Rakefile': 'ruby',
  'Fastfile': 'ruby',
  'Appfile': 'ruby',
  '.bashrc': 'bash',
  '.zshrc': 'bash',
  '.zshenv': 'bash',
  '.zprofile': 'bash',
  '.profile': 'bash',
  '.envrc': 'bash',
  '.gitconfig': 'ini',
  '.editorconfig': 'ini',
  '.npmrc': 'ini',
  'go.mod': 'go',
  'Cargo.lock': 'ini',
};

const _byExt = {
  'dart': 'dart',
  'go': 'go',
  'js': 'javascript',
  'mjs': 'javascript',
  'cjs': 'javascript',
  'jsx': 'javascript',
  'ts': 'typescript',
  'mts': 'typescript',
  'cts': 'typescript',
  'tsx': 'typescript',
  'json': 'json',
  'jsonc': 'json',
  'json5': 'json',
  'arb': 'json',
  'py': 'python',
  'pyi': 'python',
  'rs': 'rust',
  'c': 'c',
  'h': 'c',
  'cc': 'cpp',
  'cpp': 'cpp',
  'cxx': 'cpp',
  'hpp': 'cpp',
  'hh': 'cpp',
  'm': 'objectivec',
  'mm': 'objectivec',
  'java': 'java',
  'kt': 'kotlin',
  'kts': 'kotlin',
  'swift': 'swift',
  'cs': 'csharp',
  'sh': 'bash',
  'bash': 'bash',
  'zsh': 'bash',
  'command': 'bash',
  'yaml': 'yaml',
  'yml': 'yaml',
  'toml': 'ini',
  'ini': 'ini',
  'cfg': 'ini',
  'conf': 'ini',
  'properties': 'properties',
  'sql': 'sql',
  'rb': 'ruby',
  'gemspec': 'ruby',
  'lua': 'lua',
  'php': 'php',
  'css': 'css',
  'scss': 'scss',
  'less': 'less',
  'html': 'xml',
  'htm': 'xml',
  'xml': 'xml',
  'svg': 'xml',
  'plist': 'xml',
  'xib': 'xml',
  'storyboard': 'xml',
  'entitlements': 'xml',
  'vue': 'vue',
  'md': 'markdown',
  'markdown': 'markdown',
  'diff': 'diff',
  'patch': 'diff',
  'dockerfile': 'dockerfile',
  'mk': 'makefile',
  'proto': 'protobuf',
  'gradle': 'gradle',
  'groovy': 'groovy',
  'graphql': 'graphql',
  'gql': 'graphql',
  'cmake': 'cmake',
};

/// Markdown 代码块围栏上写的语言（ts、sh、yml……），认得出才着色。
String? languageForFence(String info) {
  final name = info.trim().split(RegExp(r'\s')).first.toLowerCase();
  if (name.isEmpty) return null;
  final mapped = _fenceAlias[name] ?? name;
  return _hl.getLanguage(mapped) == null ? null : mapped;
}

const _fenceAlias = {
  'console': 'bash',
  'zsh': 'bash',
  'sh': 'bash',
  'html': 'xml',
  'svg': 'xml',
  'toml': 'ini',
  'jsonc': 'json',
  'tsx': 'typescript',
  'jsx': 'javascript',
  'objc': 'objectivec',
};

/// 同步着色并按行切开。language 认不出、内容过大时返回 null。
HlLines? highlightSync(String code, String? language) {
  if (language == null || code.length > highlightLimit) return null;
  if (_hl.getLanguage(language) == null) return null;
  try {
    final result = _hl.highlight(code: code, language: language);
    final out = _LineRenderer();
    result.render(out);
    return out.finish();
  } catch (_) {
    return null;
  }
}

final _cache = <(String, String), HlLines>{};

/// 着色，结果按语言和内容缓存（最多 12 份）；大的放到后台 isolate。
Future<HlLines?> highlightLines(String code, String? language) async {
  if (language == null || code.length > highlightLimit) return null;
  final key = (language, code);
  final hit = _cache[key];
  if (hit != null) return hit;
  final lines = code.length < _isolateFrom
      ? highlightSync(code, language)
      : await Isolate.run(() => highlightSync(code, language));
  if (lines == null) return null;
  if (_cache.length >= 12) _cache.remove(_cache.keys.first);
  _cache[key] = lines;
  return lines;
}

/// 已经算过的结果，没有返回 null。小块代码第一次也直接同步算，免得闪一下没颜色的。
HlLines? highlightCached(String code, String? language) {
  if (language == null) return null;
  final hit = _cache[(language, code)];
  if (hit != null || code.length >= _isolateFrom) return hit;
  final lines = highlightSync(code, language);
  if (lines != null) {
    if (_cache.length >= 12) _cache.remove(_cache.keys.first);
    _cache[(language, code)] = lines;
  }
  return lines;
}

/// 把 highlight.js 的语法树摊成逐行的片段：内层没有对应颜色的沿用外层。
class _LineRenderer implements HighlightRenderer {
  final _lines = <List<Seg>>[[]];
  final _stack = <Tok?>[];

  Tok? get _top => _stack.isEmpty ? null : _stack.last;

  @override
  void addText(String text) {
    final tok = _top == Tok.plain ? null : _top;
    final parts = text.split('\n');
    for (var i = 0; i < parts.length; i++) {
      if (i > 0) _lines.add([]);
      if (parts[i].isNotEmpty) _lines.last.add((parts[i], tok));
    }
  }

  @override
  void openNode(DataNode node) {
    final scope = node.scope;
    _stack.add(scope == null ? _top : tokForScope(scope) ?? _top);
  }

  @override
  void closeNode(DataNode node) {
    if (_stack.isNotEmpty) _stack.removeLast();
  }

  HlLines finish() => _lines;
}

/// highlight.js 的 scope（如 title.function、meta keyword）对应到哪种颜色。
@visibleForTesting
Tok? tokForScope(String scope) {
  if (scope.startsWith('language:')) return null;
  final s = scope.split(' ').first;
  if (s.startsWith('title.class')) return Tok.type;
  if (s.startsWith('title')) return Tok.function;
  return switch (s.split('.').first) {
    'keyword' || 'operator' || 'doctag' => Tok.keyword,
    'built_in' || 'type' || 'class' => Tok.type,
    'function' || 'meta' => Tok.function,
    'attr' ||
    'attribute' ||
    'property' ||
    'variable' ||
    'template-variable' ||
    'selector-attr' ||
    'selector-pseudo' ||
    'selector-class' ||
    'selector-id' => Tok.property,
    'literal' || 'number' || 'symbol' || 'bullet' => Tok.constant,
    'string' || 'char' || 'code' || 'link' => Tok.string,
    'regexp' => Tok.regexp,
    'name' || 'selector-tag' => Tok.tag,
    'comment' || 'quote' => Tok.comment,
    'section' => Tok.heading,
    'addition' => Tok.inserted,
    'deletion' => Tok.deleted,
    'strong' => Tok.strong,
    'emphasis' => Tok.emphasis,
    'subst' => Tok.plain,
    _ => null,
  };
}

/// 配色：桌面 mygo 编辑器的 GitHub 深浅两套。
TextStyle? tokStyle(Tok? tok, bool dark) {
  if (tok == null || tok == Tok.plain) return null;
  Color hex(int v) => Color(0xFF000000 | v);
  final color = switch (tok) {
    Tok.keyword => dark ? 0xff7b72 : 0xcf222e,
    Tok.type => dark ? 0xffa657 : 0x953800,
    Tok.function => dark ? 0xd2a8ff : 0x8250df,
    Tok.property || Tok.constant || Tok.heading => dark ? 0x79c0ff : 0x0550ae,
    Tok.string => dark ? 0xa5d6ff : 0x0a3069,
    Tok.regexp || Tok.tag || Tok.inserted => dark ? 0x7ee787 : 0x116329,
    Tok.comment => dark ? 0x8b949e : 0x6e7781,
    Tok.deleted => dark ? 0xffa198 : 0x82071e,
    _ => null,
  };
  return TextStyle(
    color: color == null ? null : hex(color),
    fontWeight: tok == Tok.heading || tok == Tok.strong
        ? FontWeight.w600
        : null,
    fontStyle: tok == Tok.emphasis ? FontStyle.italic : null,
  );
}

/// 一行的片段换成 TextSpan。
List<TextSpan> lineSpans(List<Seg> line, HiveColors c) => [
  for (final (text, tok) in line)
    TextSpan(text: text, style: tokStyle(tok, c.dark)),
];

/// 整段代码的 TextSpan：行之间补回换行。
TextSpan codeSpan(HlLines lines, HiveColors c) => TextSpan(
  children: [
    for (var i = 0; i < lines.length; i++) ...[
      if (i > 0) const TextSpan(text: '\n'),
      ...lineSpans(lines[i], c),
    ],
  ],
);

/// 先显示 [plain]（不带颜色），着好色再换上；[builder] 拿到的 lines 为空表示还没好或不着色。
class Highlighted extends StatefulWidget {
  const Highlighted({
    super.key,
    required this.code,
    required this.language,
    required this.builder,
  });

  final String code;
  final String? language;
  final Widget Function(BuildContext context, HlLines? lines) builder;

  @override
  State<Highlighted> createState() => _HighlightedState();
}

class _HighlightedState extends State<Highlighted> {
  HlLines? _lines;

  @override
  void initState() {
    super.initState();
    _start();
  }

  @override
  void didUpdateWidget(Highlighted old) {
    super.didUpdateWidget(old);
    if (old.code != widget.code || old.language != widget.language) _start();
  }

  void _start() {
    final code = widget.code, language = widget.language;
    _lines = highlightCached(code, language);
    if (_lines != null || language == null) return;
    highlightLines(code, language).then((lines) {
      if (!mounted || widget.code != code || widget.language != language) {
        return;
      }
      setState(() => _lines = lines);
    });
  }

  @override
  Widget build(BuildContext context) => widget.builder(context, _lines);
}
