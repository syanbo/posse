import 'package:flutter/material.dart';

import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'displays.dart';

/// 选看哪块屏：上面按真实排布画出各块屏（点哪块切哪块），下面是名字和分辨率。返回选中的屏，取消为 null。
Future<int?> pickDeskDisplay(BuildContext context, DeskDisplays displays) {
  return showModalBottomSheet<int>(
    context: context,
    showDragHandle: true,
    // 看桌面常是横屏，高度放不下排布图加列表时能滚
    isScrollControlled: true,
    builder: (sheet) {
      final c = HiveColors.of(sheet);
      void pick(int id) => Navigator.of(sheet).pop(id);
      return SafeArea(
        top: false,
        child: SingleChildScrollView(
          child: Section(
            inset: 48,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
                child: SizedBox(
                  height: 120,
                  child: DeskDisplayMap(displays: displays, onPick: pick),
                ),
              ),
              for (final (i, d) in displays.list.indexed)
                RowTile(
                  key: Key('desk-display-${d.id}'),
                  leading: SizedBox(
                    width: 22,
                    child: Text(
                      '${i + 1}',
                      textAlign: TextAlign.center,
                      style: TextStyle(
                        color: d.id == displays.current ? c.accent : c.muted,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ),
                  title: Text(deskDisplayName(displays, d)),
                  subtitle: Text(
                    '${d.pixelWidth} × ${d.pixelHeight}${d.main ? ' · 主屏' : ''}',
                  ),
                  trailing: d.id == displays.current
                      ? Icon(Icons.check_rounded, size: 20, color: c.accent)
                      : null,
                  chevron: false,
                  onTap: () => pick(d.id),
                ),
            ],
          ),
        ),
      );
    },
  );
}

/// 排布图：每块屏按相对位置和大小画成一块，正在看的那块高亮，块上写序号（和下面列表的顺序一样）。
class DeskDisplayMap extends StatelessWidget {
  const DeskDisplayMap({
    super.key,
    required this.displays,
    required this.onPick,
  });

  final DeskDisplays displays;
  final ValueChanged<int> onPick;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    return LayoutBuilder(
      builder: (context, box) {
        final rects = deskDisplayLayout(displays.list, box.biggest);
        return Stack(
          children: [
            for (final (i, d) in displays.list.indexed)
              if (rects[d.id] case final r?)
                Positioned.fromRect(
                  // 屏之间留一道缝，紧挨着的两块分得清
                  rect: r.deflate(2),
                  child: GestureDetector(
                    key: Key('desk-display-map-${d.id}'),
                    onTap: () => onPick(d.id),
                    child: DecoratedBox(
                      decoration: BoxDecoration(
                        color: d.id == displays.current
                            ? c.accent.withValues(alpha: 0.22)
                            : c.wash,
                        border: Border.all(
                          color: d.id == displays.current ? c.accent : c.line,
                          width: d.id == displays.current ? 1.5 : 1,
                        ),
                        borderRadius: BorderRadius.circular(Rd.button / 2),
                      ),
                      child: Center(
                        child: Text(
                          '${i + 1}',
                          style: TextStyle(
                            color: d.id == displays.current
                                ? c.accent
                                : c.muted,
                            fontWeight: FontWeight.w600,
                          ),
                        ),
                      ),
                    ),
                  ),
                ),
          ],
        );
      },
    );
  }
}
