import 'package:flutter/widgets.dart';
import 'package:flutter_svg/flutter_svg.dart';

/// 桌面侧栏的描边图标（`internal/app/icons.go`），形状照抄，两端的项目和工作区看着一样。
enum LineIcon {
  folder(
    '<path d="M3.5 7.5a2 2 0 0 1 2-2h3.6l2 2h7.4a2 2 0 0 1 2 2v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  ),
  branch(
    '<circle cx="7" cy="6" r="2.25"/><circle cx="7" cy="18" r="2.25"/><circle cx="17" cy="7.5" r="2.25"/><path d="M7 8.25v7.5M17 9.75c0 3.75-3.25 5.25-7.5 6"/>',
  ),
  home(
    '<path d="M4 10.4a1.5 1.5 0 0 1 .55-1.16l6.5-5.3a1.5 1.5 0 0 1 1.9 0l6.5 5.3a1.5 1.5 0 0 1 .55 1.16V18a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 18z"/><path d="M9.75 19.5v-4.75a1 1 0 0 1 1-1h2.5a1 1 0 0 1 1 1v4.75"/>',
  );

  const LineIcon(this.shapes);

  final String shapes;

  String get svg =>
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">$shapes</svg>';
}

class LineIconView extends StatelessWidget {
  const LineIconView(
    this.icon, {
    super.key,
    required this.size,
    required this.color,
  });

  final LineIcon icon;
  final double size;
  final Color color;

  @override
  Widget build(BuildContext context) => SvgPicture.string(
    icon.svg,
    width: size,
    height: size,
    theme: SvgTheme(currentColor: color),
  );
}
