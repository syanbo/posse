import 'dart:ui';

/// 解析 SVG path 的 d 属性。只为画 agent 图标，支持 M L H V C S Q T A Z 及其相对写法。
Path parseSvgPath(String d) {
  final path = Path()..fillType = PathFillType.evenOdd;
  final tokens = _tokens(d);
  var i = 0;
  var cmd = '';
  var x = 0.0, y = 0.0; // 当前点
  var sx = 0.0, sy = 0.0; // 子路径起点
  var cx = 0.0, cy = 0.0; // 上一条曲线的控制点，给 S、T 反射用
  var last = '';

  bool isCmd(String t) => RegExp(r'^[A-Za-z]$').hasMatch(t);
  double n() => double.parse(tokens[i++]);
  bool flag() => tokens[i++] != '0';

  while (i < tokens.length) {
    if (isCmd(tokens[i])) {
      cmd = tokens[i++];
    } else if (cmd == 'M') {
      cmd = 'L'; // M 后面接着的坐标对按 L 处理
    } else if (cmd == 'm') {
      cmd = 'l';
    }
    final rel = cmd == cmd.toLowerCase();
    final ox = rel ? x : 0.0, oy = rel ? y : 0.0;
    switch (cmd.toUpperCase()) {
      case 'M':
        x = ox + n();
        y = oy + n();
        sx = x;
        sy = y;
        path.moveTo(x, y);
      case 'L':
        x = ox + n();
        y = oy + n();
        path.lineTo(x, y);
      case 'H':
        x = ox + n();
        path.lineTo(x, y);
      case 'V':
        y = (rel ? y : 0.0) + n();
        path.lineTo(x, y);
      case 'C':
        final x1 = ox + n(), y1 = oy + n();
        cx = ox + n();
        cy = oy + n();
        x = ox + n();
        y = oy + n();
        path.cubicTo(x1, y1, cx, cy, x, y);
      case 'S':
        final reflect = 'CcSs'.contains(last);
        final x1 = reflect ? 2 * x - cx : x, y1 = reflect ? 2 * y - cy : y;
        cx = ox + n();
        cy = oy + n();
        x = ox + n();
        y = oy + n();
        path.cubicTo(x1, y1, cx, cy, x, y);
      case 'Q':
        cx = ox + n();
        cy = oy + n();
        x = ox + n();
        y = oy + n();
        path.quadraticBezierTo(cx, cy, x, y);
      case 'T':
        final reflect = 'QqTt'.contains(last);
        cx = reflect ? 2 * x - cx : x;
        cy = reflect ? 2 * y - cy : y;
        x = ox + n();
        y = oy + n();
        path.quadraticBezierTo(cx, cy, x, y);
      case 'A':
        final rx = n(), ry = n(), rot = n();
        final large = flag(), sweep = flag();
        x = ox + n();
        y = oy + n();
        path.arcToPoint(
          Offset(x, y),
          radius: Radius.elliptical(rx, ry),
          rotation: rot,
          largeArc: large,
          clockwise: sweep,
        );
      case 'Z':
        path.close();
        x = sx;
        y = sy;
    }
    last = cmd;
  }
  return path;
}

/// 拆成命令和数字。数字可以紧贴着写：-.5.5 是 -0.5 和 0.5；弧线的两个标志位也可能和后面的数连在一起。
List<String> _tokens(String d) {
  final out = <String>[];
  var arcIndex = -1; // 在 A 命令的参数里时，表示是第几个参数
  final re = RegExp(r'[A-Za-z]|-?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?');
  var pos = 0;
  while (pos < d.length) {
    final ch = d[pos];
    if (' ,\n\t\r'.contains(ch)) {
      pos++;
      continue;
    }
    if (RegExp(r'[A-Za-z]').hasMatch(ch)) {
      out.add(ch);
      arcIndex = ch == 'A' || ch == 'a' ? 0 : -1;
      pos++;
      continue;
    }
    // 弧线第 4、5 个参数是单个 0/1 标志位
    if (arcIndex == 3 || arcIndex == 4) {
      out.add(ch);
      pos++;
      arcIndex++;
      continue;
    }
    final m = re.matchAsPrefix(d, pos);
    if (m == null) {
      pos++;
      continue;
    }
    out.add(m.group(0)!);
    pos = m.end;
    if (arcIndex >= 0) arcIndex = (arcIndex + 1) % 7;
  }
  return out;
}
