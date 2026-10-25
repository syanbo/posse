import 'dart:convert';
import 'dart:typed_data';
import 'dart:ui';

/// Mac 上的一块屏（电脑的 desk.Display）。位置和大小是全局坐标里的点，主屏左上角是原点。
class DeskDisplay {
  const DeskDisplay({
    required this.id,
    required this.name,
    required this.rect,
    required this.pixelWidth,
    required this.pixelHeight,
    required this.main,
  });

  final int id;
  final String name;
  final Rect rect;
  final int pixelWidth;
  final int pixelHeight;
  final bool main;

  static DeskDisplay? parse(Object? raw) {
    if (raw is! Map) return null;
    double n(Object? v) => v is num ? v.toDouble() : 0;
    int i(Object? v) => v is num ? v.toInt() : 0;
    final id = i(raw['id']);
    if (id == 0) return null;
    final name = raw['name'];
    return DeskDisplay(
      id: id,
      name: name is String ? name : '',
      rect: Rect.fromLTWH(n(raw['x']), n(raw['y']), n(raw['w']), n(raw['h'])),
      pixelWidth: i(raw['pw']),
      pixelHeight: i(raw['ph']),
      main: raw['main'] == true,
    );
  }
}

/// Mac 上一个可以单独看的窗口（电脑的 desk.Window）。宽高是点数。
class DeskWindow {
  const DeskWindow({
    required this.id,
    required this.app,
    this.title = '',
    this.width = 0,
    this.height = 0,
    this.icon,
  });

  final int id;
  final String app;
  final String title;
  final double width;
  final double height;

  /// App 图标（PNG），电脑没给时为 null。
  final Uint8List? icon;

  /// 列表里的名字：有窗口标题用标题，没有用 App 名。
  String get label => title.isNotEmpty ? title : app;

  static DeskWindow? parse(
    Object? raw, [
    Map<String, Uint8List> icons = const {},
  ]) {
    if (raw is! Map) return null;
    final id = raw['id'];
    final app = raw['app'];
    if (id is! num || id <= 0 || app is! String) return null;
    final title = raw['title'];
    double n(Object? v) => v is num ? v.toDouble() : 0;
    return DeskWindow(
      id: id.toInt(),
      app: app,
      title: title is String ? title : '',
      width: n(raw['w']),
      height: n(raw['h']),
      icon: icons[raw['icon']],
    );
  }

  /// desk.windows 的回复：当前空间里看得见的窗口，最前的在前面。图标按 App 给一份（`icons`，键到 base64 的 PNG），
  /// 每个窗口用 `icon` 指过去。
  static List<DeskWindow> parseList(Map<String, Object?> reply) {
    final icons = <String, Uint8List>{};
    if (reply['icons'] case final Map raw) {
      for (final MapEntry(:key, :value) in raw.entries) {
        if (key is! String || value is! String) continue;
        try {
          icons[key] = base64Decode(value);
        } on FormatException {
          // 坏的就不画，用默认图标
        }
      }
    }
    final raw = reply['windows'];
    return [
      if (raw is List)
        for (final w in raw) ?DeskWindow.parse(w, icons),
    ];
  }
}

/// desk.subscribe 流里的 displays 事件：所有的屏和正在看的那块。只看一个窗口时 [window] 是它，
/// [current] 是它所在的屏；[gone] 是正在看的窗口关掉了（或最小化、切走了），回到了整块屏。
class DeskDisplays {
  const DeskDisplays(this.list, this.current, {this.window, this.gone = false});

  static const none = DeskDisplays([], 0);

  final List<DeskDisplay> list;
  final int current;
  final DeskWindow? window;
  final bool gone;

  bool get several => list.length > 1;

  static DeskDisplays parse(Map<String, Object?> frame) {
    final raw = frame['displays'];
    final list = [
      if (raw is List)
        for (final d in raw) ?DeskDisplay.parse(d),
    ];
    final current = frame['current'];
    return DeskDisplays(
      list,
      current is num ? current.toInt() : 0,
      window: DeskWindow.parse(frame['window']),
      gone: frame['gone'] == true,
    );
  }
}

/// 屏的名字：系统给的，没有时主屏写「主显示器」，其余按排布从左到右编号。
String deskDisplayName(DeskDisplays displays, DeskDisplay d) {
  if (d.name.isNotEmpty) return d.name;
  if (d.main) return '主显示器';
  final order = [...displays.list]
    ..sort((a, b) => a.rect.left.compareTo(b.rect.left));
  return '显示器 ${order.indexOf(d) + 1}';
}

/// 排布图：把所有屏按真实的相对位置和大小缩进 [box]，居中，返回每块屏在图里的矩形。
Map<int, Rect> deskDisplayLayout(List<DeskDisplay> list, Size box) {
  if (list.isEmpty || box.isEmpty) return const {};
  var bounds = list.first.rect;
  for (final d in list.skip(1)) {
    bounds = bounds.expandToInclude(d.rect);
  }
  if (bounds.isEmpty) return const {};
  final scale = [
    box.width / bounds.width,
    box.height / bounds.height,
  ].reduce((a, b) => a < b ? a : b);
  final dx = (box.width - bounds.width * scale) / 2;
  final dy = (box.height - bounds.height * scale) / 2;
  return {
    for (final d in list)
      d.id: Rect.fromLTWH(
        dx + (d.rect.left - bounds.left) * scale,
        dy + (d.rect.top - bounds.top) * scale,
        d.rect.width * scale,
        d.rect.height * scale,
      ),
  };
}
