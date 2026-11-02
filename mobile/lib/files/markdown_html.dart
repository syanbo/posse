import 'package:html/dom.dart' as dom;
import 'package:html/parser.dart' as html;
import 'package:markdown/markdown.dart' as md;

/// 文件预览照 GitHub 解释 Markdown 里的 HTML（同桌面 `internal/app/markdown_html.go`）：README 常用
/// `<p align="center">` 居中标题和徽章、`<img>` 控制图片大小、`<table>` 做图文并排、`<details>` 折叠长内容。
/// `<script>`、`<style>` 这类连内容一起丢掉，认不出的标签只留里面的内容。
///
/// 结果仍是 markdown 包的语法树，交给 flutter_markdown_plus 画。它不认的块（对齐、HTML 表格、折叠）
/// 写成没有子节点的 `x-align`、`x-grid`、`x-details`，属性 `ref` 指向 [HtmlDoc.blocks] 里的内容：
/// 子节点留在树上的话，它会先把它们当行内元素走一遍。
class HtmlDoc {
  HtmlDoc(List<md.Node> nodes) {
    this.nodes = _linkImages(_blockList(nodes), null);
  }

  late final List<md.Node> nodes;
  final blocks = <HtmlBlock>[];

  md.Element _ref(String tag, HtmlBlock b) {
    _linkImages(b.blocks, null);
    _linkImages(b.summary, null);
    for (final row in b.grid) {
      for (final c in row) {
        _linkImages(c.blocks, null);
      }
    }
    blocks.add(b);
    return md.Element.empty(tag)..attributes['ref'] = '${blocks.length - 1}';
  }

  /// 块的列表（文档根、引用、列表项里）：HTML 块连同标签还没关上时后面的 Markdown 块一起解析，
  /// CommonMark 的 HTML 块到空行就结束，`<td>` 和 `</td>` 之间空了行写的 Markdown 要挂回所在的格子。
  List<md.Node> _blockList(List<md.Node> nodes) {
    final out = <md.Node>[];
    var i = 0;
    while (i < nodes.length) {
      final n = nodes[i];
      if (!_isHtmlBlock(n)) {
        out.addAll(_block(n));
        i++;
        continue;
      }
      final src = StringBuffer();
      final embedded = <List<md.Node>>[];
      final open = <String>[];
      for (; i < nodes.length; i++) {
        final k = nodes[i];
        if (_isHtmlBlock(k)) {
          final text = (k as md.Text).text;
          src.writeln(text);
          _trackTags(text, open);
        } else {
          src.write('<x-md i="${embedded.length}"></x-md>');
          embedded.add(_block(k));
        }
        if (open.isEmpty) {
          i++;
          break;
        }
      }
      final frag = html.parseFragment(src.toString());
      out.addAll(_HtmlConv(this, embedded).blocks(frag.nodes, null));
    }
    return out;
  }

  /// 一个 Markdown 块：容器的子节点接着转，段落、标题这类的行内 HTML 换成元素。
  List<md.Node> _block(md.Node n) {
    if (n is! md.Element) return [n];
    final kids = n.children;
    if (kids == null) return [n];
    switch (n.tag) {
      case 'pre':
        return [n];
      case 'blockquote':
        return [_copy(n, _blockList(kids))];
      case 'ul':
      case 'ol':
      case 'table':
      case 'thead':
      case 'tbody':
      case 'tr':
        return [
          _copy(n, [for (final k in kids) ..._block(k)]),
        ];
      case 'li':
        // 紧凑列表的项直接放行内内容，后面可能跟着子列表
        final out = <md.Node>[];
        var run = <md.Node>[];
        void flush() {
          if (run.isNotEmpty) out.addAll(_inline(run));
          run = [];
        }
        for (final k in kids) {
          if (k is md.Element && _mdBlockTags.contains(k.tag)) {
            flush();
            out.addAll(
              k.tag == 'p' || k.tag.startsWith('h')
                  ? _block(k)
                  : _blockList([k]),
            );
          } else {
            run.add(k);
          }
        }
        flush();
        return [_copy(n, out)];
    }
    return [_copy(n, _inline(kids))];
  }

  /// 行内内容：markdown 包把行内 HTML 标签当文字，和前后的字并成一个文字节点，这里切出标签配对成元素。
  List<md.Node> _inline(List<md.Node> nodes) {
    final root = md.Element('#', []);
    final stack = <(String, md.Element)>[('#', root)];
    var drop = 0;
    void add(md.Node n) {
      if (drop == 0) stack.last.$2.children!.add(n);
    }

    void tag(RegExpMatch m) {
      if (m[0]!.startsWith('<!')) return;
      final tag = m[2]!.toLowerCase();
      if (m[1] == '/') {
        if (_dropTags.contains(tag)) {
          if (drop > 0) drop--;
          return;
        }
        if (drop > 0) return;
        final at = stack.lastIndexWhere((e) => e.$1 == tag);
        if (at > 0) stack.removeRange(at, stack.length);
        return;
      }
      if (_dropTags.contains(tag)) {
        if (!_voidTags.contains(tag) && m[4] != '/') drop++;
        return;
      }
      // 丢掉的内容里的标签不开也不关外面的元素
      if (drop > 0) return;
      final attrs = _attrs(m[3] ?? '');
      switch (tag) {
        case 'img':
          final img = _img(attrs);
          if (img != null) add(img);
          return;
        case 'br':
          add(md.Element.empty('br'));
          return;
      }
      if (_voidTags.contains(tag) || m[4] == '/') return;
      final mapped = _inlineTag(tag, attrs);
      if (mapped == null) {
        // 认不出的标签只留内容；记下来是为了它的结束标签不去关外面的元素
        stack.add((tag, stack.last.$2));
        return;
      }
      add(mapped);
      stack.add((tag, mapped));
    }

    for (final n in nodes) {
      if (n is md.Element) {
        final kids = n.children;
        add(kids == null || n.tag == 'code' ? n : _copy(n, _inline(kids)));
        continue;
      }
      if (n is! md.Text) {
        add(n);
        continue;
      }
      var at = 0;
      for (final m in _inlineTagPattern.allMatches(n.text)) {
        if (m.start > at) add(md.Text(n.text.substring(at, m.start)));
        tag(m);
        at = m.end;
      }
      if (at == 0) {
        add(n);
      } else if (at < n.text.length) {
        add(md.Text(n.text.substring(at)));
      }
    }
    return root.children!;
  }
}

/// HTML 片段转成块：行内的内容攒成段落，遇到块级标签先收掉段落。
class _HtmlConv {
  _HtmlConv(this.doc, this.embedded);

  final HtmlDoc doc;
  final List<List<md.Node>> embedded;

  List<md.Node> blocks(List<dom.Node> nodes, String? align) {
    final b = _Blocks(this, align);
    for (final n in nodes) {
      b.node(n);
    }
    b.flush();
    return b.out;
  }

  /// 标签里的行内内容（标题、`<summary>`），里面的块拼成一段，用换行隔开。
  List<md.Node> inlines(List<dom.Node> nodes) {
    final out = <md.Node>[];
    for (final b in blocks(nodes, null)) {
      if (out.isNotEmpty) out.add(md.Element.empty('br'));
      if (b is md.Element && b.children != null) {
        out.addAll(b.children!);
      } else {
        out.add(b);
      }
    }
    return out;
  }

  md.Node table(dom.Element t) {
    final rows = <List<dom.Element>>[];
    void walk(dom.Element e) {
      for (final k in e.children) {
        switch (k.localName) {
          case 'thead':
          case 'tbody':
          case 'tfoot':
            walk(k);
          case 'tr':
            rows.add([
              for (final c in k.children)
                if (c.localName == 'td' || c.localName == 'th') c,
            ]);
        }
      }
    }

    walk(t);
    final grid = <List<HtmlCell>>[];
    // Markdown 表格每行的格数要一样（Flutter 的 Table 不收参差的行），参差的照 HTML 表格画
    var simple = rows.isNotEmpty;
    for (final (ri, row) in rows.indexed) {
      if (row.length != rows[0].length) simple = false;
      final cells = <HtmlCell>[];
      for (final c in row) {
        final header = c.localName == 'th';
        final cell = HtmlCell(
          blocks: blocks(c.nodes, _alignOf(c, null)),
          header: header,
          align: _alignOf(c, null),
          valign: c.attributes['valign']?.toLowerCase(),
          width: HtmlLen.parse(c.attributes['width']),
        );
        final bs = cell.blocks;
        if (bs.length > 1 ||
            bs.length == 1 &&
                !(bs[0] is md.Element && (bs[0] as md.Element).tag == 'p') ||
            _hasImage(bs) ||
            ri == 0 && !header ||
            c.attributes.containsKey('width')) {
          simple = false;
        }
        cells.add(cell);
      }
      grid.add(cells);
    }
    if (!simple) return doc._ref('x-grid', HtmlBlock(grid: grid));
    // 格子里都只有一段文字、第一行是 <th> 的照 Markdown 表格画
    md.Element row(List<HtmlCell> cells, String tag) => md.Element('tr', [
      for (final c in cells)
        md.Element(tag, [
          if (c.blocks.isNotEmpty) ...(c.blocks[0] as md.Element).children!,
        ])..attributes.addAll({if (c.align != null) 'align': c.align!}),
    ]);
    return md.Element('table', [
      md.Element('thead', [row(grid[0], 'th')]),
      if (grid.length > 1)
        md.Element('tbody', [for (final r in grid.skip(1)) row(r, 'td')]),
    ]);
  }
}

class _Blocks {
  _Blocks(this.conv, this.align);

  final _HtmlConv conv;
  final String? align;
  final out = <md.Node>[];

  /// 正在攒的段落；栈顶是当前开着的行内元素。
  md.Element _para = md.Element('p', []);
  final _stack = <md.Element>[];

  md.Element get _top => _stack.isEmpty ? _para : _stack.last;

  void _add(md.Node n) => _top.children!.add(n);

  /// 段落里最后一段字（判断要不要留开头的空格）。
  bool _atLineStart() {
    md.Node? last;
    void find(md.Element e) {
      final kids = e.children!;
      if (kids.isEmpty) return;
      last = kids.last;
      if (last is md.Element && (last as md.Element).children != null) {
        find(last as md.Element);
      }
    }

    find(_para);
    final l = last;
    if (l == null) return true;
    if (l is md.Element) return l.tag == 'br';
    return l is md.Text && l.text.endsWith('\n');
  }

  void flush() {
    final p = _para;
    _para = md.Element('p', []);
    _stack.clear();
    if (_trim(p)) out.add(p);
  }

  void block(List<md.Node> bs) {
    flush();
    out.addAll(bs);
  }

  void node(dom.Node n) {
    if (n is dom.Text) {
      var text = n.data.replaceAll(_space, ' ');
      if (text == ' ' && _atLineStart()) return;
      _add(md.Text(text));
      return;
    }
    if (n is! dom.Element) return;
    final tag = n.localName ?? '';
    if (_dropTags.contains(tag)) return;
    final mapped = _inlineTag(tag, n.attributes);
    if (mapped != null) {
      _add(mapped);
      _stack.add(mapped);
      for (final k in n.nodes) {
        node(k);
      }
      _stack.remove(mapped);
      return;
    }
    switch (tag) {
      case 'x-md':
        block(conv.embedded[int.parse(n.attributes['i']!)]);
      case 'img':
        final img = _img(n.attributes);
        if (img != null) _add(img);
      case 'br':
        _add(md.Element.empty('br'));
      case 'h1' || 'h2' || 'h3' || 'h4' || 'h5' || 'h6':
        block([
          _aligned(_alignOf(n, null), md.Element(tag, conv.inlines(n.nodes))),
        ]);
      case 'p' ||
          'div' ||
          'center' ||
          'section' ||
          'article' ||
          'header' ||
          'footer' ||
          'main' ||
          'nav' ||
          'aside' ||
          'figure' ||
          'figcaption' ||
          'address' ||
          'hgroup' ||
          'dl' ||
          'dt' ||
          'dd' ||
          'summary':
        final a = tag == 'center' ? 'center' : _alignOf(n, null);
        final bs = conv.blocks(n.nodes, null);
        block(a == null || a == align ? bs : [_alignBlocks(a, bs)]);
      case 'ul' || 'ol':
        final list = md.Element(tag, [
          for (final k in n.children)
            if (k.localName == 'li')
              md.Element('li', conv.blocks(k.nodes, null)),
        ]);
        final start = int.tryParse(n.attributes['start'] ?? '');
        if (tag == 'ol' && start != null) {
          list.attributes['start'] = '$start';
        }
        block([list]);
      case 'li':
        block([
          md.Element('ul', [md.Element('li', conv.blocks(n.nodes, null))]),
        ]);
      case 'blockquote':
        block([md.Element('blockquote', conv.blocks(n.nodes, null))]);
      case 'hr':
        block([md.Element.empty('hr')]);
      case 'pre':
        var lang = '';
        final code = n.querySelector('code');
        for (final cls in (code?.className ?? '').split(' ')) {
          if (cls.startsWith('language-')) lang = cls.substring(9);
        }
        final text = n.text.replaceAll(RegExp(r'^\n+|\n+$'), '');
        block([
          md.Element('pre', [
            md.Element('code', [md.Text(text)])
              ..attributes.addAll({if (lang != '') 'class': 'language-$lang'}),
          ]),
        ]);
      case 'table':
        block([conv.table(n)]);
      case 'details':
        dom.Element? summary;
        final rest = <dom.Node>[];
        for (final k in n.nodes) {
          if (summary == null && k is dom.Element && k.localName == 'summary') {
            summary = k;
          } else {
            rest.add(k);
          }
        }
        block([
          conv.doc._ref(
            'x-details',
            HtmlBlock(
              summary: summary == null ? const [] : conv.inlines(summary.nodes),
              blocks: conv.blocks(rest, null),
              open: n.attributes.containsKey('open'),
            ),
          ),
        ]);
      default:
        // 认不出的标签（<span>、<sup>、<picture>、<font> 等）只留里面的内容；
        // <picture> 的 <source> 按深浅色、屏幕宽度挑图，这里不挑，只取里面的 <img>
        for (final k in n.nodes) {
          node(k);
        }
    }
  }

  md.Node _aligned(String? a, md.Node b) =>
      a == null || a == align ? b : _alignBlocks(a, [b]);

  md.Node _alignBlocks(String a, List<md.Node> bs) =>
      conv.doc._ref('x-align', HtmlBlock(blocks: bs, align: a));
}

/// HTML 里写的长度：像素，或 [pct] 为真时的百分比。
class HtmlLen {
  const HtmlLen(this.value, {this.pct = false});

  final double value;
  final bool pct;

  static HtmlLen? parse(String? s) {
    if (s == null) return null;
    var t = s.trim();
    final pct = t.endsWith('%');
    if (pct) t = t.substring(0, t.length - 1);
    if (t.endsWith('px')) t = t.substring(0, t.length - 2);
    final v = double.tryParse(t.trim());
    if (v == null || v <= 0) return null;
    return HtmlLen(v, pct: pct);
  }
}

/// 对齐、HTML 表格、折叠的内容。
class HtmlBlock {
  HtmlBlock({
    this.blocks = const [],
    this.align,
    this.grid = const [],
    this.summary = const [],
    this.open = false,
  });

  final List<md.Node> blocks;

  /// x-align：left、center、right。
  final String? align;

  /// x-grid 的行。
  final List<List<HtmlCell>> grid;

  /// x-details 的 `<summary>`，没写时为空、由显示的一侧按界面语言补上；[blocks] 是折起来的内容。
  final List<md.Node> summary;
  final bool open;
}

class HtmlCell {
  HtmlCell({
    required this.blocks,
    required this.header,
    this.align,
    this.valign,
    this.width,
  });

  final List<md.Node> blocks;
  final bool header;
  final String? align;
  final String? valign;
  final HtmlLen? width;
}

const _mdBlockTags = {
  'p',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'ul',
  'ol',
  'pre',
  'blockquote',
  'hr',
  'table',
};

const _voidTags = {
  'img',
  'br',
  'hr',
  'source',
  'input',
  'meta',
  'link',
  'wbr',
  'col',
  'area',
  'embed',
  'track',
  'param',
  'base',
};

/// 连内容一起不显示。
const _dropTags = {
  'script',
  'style',
  'template',
  'head',
  'title',
  'noscript',
  'iframe',
  'object',
  'video',
  'audio',
  'canvas',
  'svg',
  'math',
  'form',
  'button',
  'select',
  'textarea',
  'input',
};

final _space = RegExp(r'[ \t\n\r\f]+');

/// 文字里的行内 HTML：开始、结束标签或注释。
final _inlineTagPattern = RegExp(
  r'<(/?)([a-zA-Z][a-zA-Z0-9-]*)((?:\s[^>]*?)?)\s*(/?)>|<!--[\s\S]*?-->',
);

final _attrPattern = RegExp(
  r'''([^\s"'>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?''',
);

Map<String, String> _attrs(String s) => {
  for (final m in _attrPattern.allMatches(s))
    m[1]!.toLowerCase(): _unescape(m[2] ?? m[3] ?? m[4] ?? ''),
};

String _unescape(String s) =>
    s.contains('&') ? (html.parseFragment(s).text ?? s) : s;

/// HTML 块：markdown 包把它放成块一级的文字节点。
bool _isHtmlBlock(md.Node n) =>
    n is md.Text && n.text.trimLeft().startsWith('<');

/// 跟着 HTML 块里的标签开合，判断后面的 Markdown 块是不是还在某个标签里面。
void _trackTags(String src, List<String> open) {
  final noComments = src.replaceAll(RegExp(r'<!--[\s\S]*?-->'), '');
  for (final m in RegExp(
    r'<(/?)([a-zA-Z][a-zA-Z0-9-]*)[^>]*?(/?)>',
  ).allMatches(noComments)) {
    final tag = m[2]!.toLowerCase();
    if (m[1] == '/') {
      final at = open.lastIndexOf(tag);
      if (at >= 0) open.removeRange(at, open.length);
    } else if (m[3] != '/' && !_voidTags.contains(tag)) {
      // 省略结束标签的 <p>、<li> 不等，免得之后整篇都算在它里面
      if (tag != 'p' && tag != 'li') open.add(tag);
    }
  }
}

String? _alignOf(dom.Element e, String? inherit) =>
    switch (e.attributes['align']?.trim().toLowerCase()) {
      'center' || 'middle' => 'center',
      'right' => 'right',
      'left' => 'left',
      _ => inherit,
    };

/// 行内标签换成 flutter_markdown_plus 认得的元素；不改样式的标签为 null。
md.Element? _inlineTag(String tag, Map<Object, String> attrs) {
  final mapped = switch (tag) {
    'b' || 'strong' => 'strong',
    'i' || 'em' || 'cite' || 'var' || 'dfn' => 'em',
    's' || 'del' || 'strike' => 'del',
    'code' || 'kbd' || 'samp' || 'tt' => 'code',
    'a' => attrs['href'] == null ? null : 'a',
    _ => null,
  };
  if (mapped == null) return null;
  final e = md.Element(mapped, []);
  if (mapped == 'a') e.attributes['href'] = attrs['href']!;
  return e;
}

md.Element? _img(Map<Object, String> attrs) {
  final src = attrs['src'];
  if (src == null || src.isEmpty) return null;
  return md.Element.empty('img')
    ..attributes.addAll({
      'src': src,
      'alt': attrs['alt'] ?? '',
      if (attrs['title'] != null) 'title': attrs['title']!,
      if (attrs['width'] != null) 'width': attrs['width']!,
      if (attrs['height'] != null) 'height': attrs['height']!,
    });
}

md.Element _copy(md.Element e, List<md.Node> kids) => md.Element(e.tag, kids)
  ..attributes.addAll(e.attributes)
  ..generatedId = e.generatedId;

/// 去掉段落首尾和换行两边的空白，只剩空白时为假。
bool _trim(md.Element p) {
  final kids = p.children!;
  bool blank(md.Node n) =>
      n is md.Text && n.text.trim().isEmpty ||
      n is md.Element && n.children != null && n.children!.every(blank);
  while (kids.isNotEmpty && blank(kids.first)) {
    kids.removeAt(0);
  }
  while (kids.isNotEmpty && blank(kids.last)) {
    kids.removeLast();
  }
  if (kids.isEmpty) return false;
  if (kids.first case md.Text t) {
    kids[0] = md.Text(t.text.trimLeft());
  }
  if (kids.last case md.Text t) {
    kids[kids.length - 1] = md.Text(t.text.trimRight());
  }
  return true;
}

bool _hasImage(List<md.Node> nodes) => nodes.any(
  (n) =>
      n is md.Element &&
      (n.tag == 'img' || n.children != null && _hasImage(n.children!)),
);

/// 包在链接里的图片记下链接：flutter_markdown_plus 用自定义的图片控件时不替它接点击。
List<md.Node> _linkImages(List<md.Node> nodes, String? href) {
  for (final n in nodes) {
    if (n is! md.Element) continue;
    if (n.tag == 'img' && href != null) n.attributes['href'] = href;
    final kids = n.children;
    if (kids != null) {
      _linkImages(kids, n.tag == 'a' ? n.attributes['href'] : href);
    }
  }
  return nodes;
}
