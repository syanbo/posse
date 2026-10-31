import 'package:flutter/widgets.dart';
import 'package:flutter_svg/flutter_svg.dart';

/// 桌面端的描边图标（`internal/app/icons.go`），形状照抄。和首页项目行、文件树并排的地方用它们，
/// 不用实心的 Material Rounded：放在一起轻重不一。
enum LineIcon {
  folder(
    '<path d="M3.5 7.5a2 2 0 0 1 2-2h3.6l2 2h7.4a2 2 0 0 1 2 2v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  ),
  branch(
    '<circle cx="7" cy="6" r="2.25"/><circle cx="7" cy="18" r="2.25"/><circle cx="17" cy="7.5" r="2.25"/><path d="M7 8.25v7.5M17 9.75c0 3.75-3.25 5.25-7.5 6"/>',
  ),
  folderOpen(
    '<path d="M3.5 16.5v-9a2 2 0 0 1 2-2h3.6l2 2h6.4a2 2 0 0 1 2 2v1.25"/><path d="m3.5 16.5 2.4-5a1.5 1.5 0 0 1 1.35-.85h12.5a.9.9 0 0 1 .82 1.27l-2.3 5.3a2 2 0 0 1-1.83 1.28H5.5a2 2 0 0 1-2-2z"/>',
  ),
  file(
    '<path d="M6.75 3.75h6.5l4 4v11.5c0 .55-.45 1-1 1h-9.5c-.55 0-1-.45-1-1V4.75c0-.55.45-1 1-1z"/><path d="M13.25 3.75v4h4"/>',
  ),
  // 原图形占满 20，缩到和别的图标一样大；线宽放大抵消缩放
  settings(
    '<g transform="translate(12 12) scale(0.82) translate(-12 -12)" stroke-width="1.83"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></g>',
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
