import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/transcript.dart' show chatWho;
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'models.dart';

/// 确认后关闭一个会话或终端，返回是否关掉了。对话记录还在电脑的会话历史里，关掉的只是终端和里面的程序。
Future<bool> closeSession(
  BuildContext context,
  HiveAppModel model,
  SessionTab tab,
) async {
  final who = tab.agent == null ? null : chatWho('assistant', tab.agent);
  final ok = await showHiveDialog<bool>(
    context: context,
    builder: (dialog) => AlertDialog(
      title: Text(who == null ? '关闭这个终端？' : '关闭这个会话？'),
      content: Text(
        who == null
            ? '「${cleanTitle(tab.title)}」里正在运行的程序会被结束。'
            : '$who 会被结束，还没做完的事会中断。对话记录留在电脑的会话历史里，之后可以在桌面端恢复。',
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(dialog, false),
          child: const Text('取消'),
        ),
        FilledButton(
          style: FilledButton.styleFrom(
            backgroundColor: HiveColors.of(dialog).danger,
          ),
          onPressed: () => Navigator.pop(dialog, true),
          child: const Text('关闭'),
        ),
      ],
    ),
  );
  if (ok != true) return false;
  final rpc = model.rpc;
  if (rpc == null) return false;
  try {
    await rpc.call('terminal.close', {'terminal': tab.terminalId});
  } catch (error) {
    if (context.mounted) {
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(friendlyError(error))));
    }
    return false;
  }
  await model.refreshWorktrees();
  return true;
}

/// 会话页、终端页右上角的"更多"：从底部滑上来的面板，和电脑列表的操作面板一样。
/// [switchLabel] 是切到另一种看法（对话或终端），为空不显示；[desk] 不为空时有"电脑布局"开关，
/// 拨动立刻生效、面板不收。[media] 时有"图片与视频"。返回 'switch'、'media' 或 'close'，点外面收起时为 null。
Future<String?> showSessionMore(
  BuildContext context, {
  required SessionTab tab,
  String? switchLabel,
  IconData switchIcon = Icons.swap_horiz_rounded,
  bool? desk,
  ValueChanged<bool>? onDesk,
  bool media = false,
}) {
  // 放在 builder 外面：拨开关会改设置、整个 App 重建，面板的 builder 跟着重跑，放在里面就被打回初值
  var on = desk ?? false;
  return showModalBottomSheet<String>(
    context: context,
    showDragHandle: true,
    builder: (sheet) {
      final c = HiveColors.of(sheet);
      return SafeArea(
        top: false,
        child: Section(
          inset: 48,
          children: [
            if (switchLabel != null)
              RowTile(
                chevron: false,
                leading: Icon(switchIcon, size: 22, color: c.accent),
                title: Text(switchLabel),
                onTap: () => Navigator.of(sheet).pop('switch'),
              ),
            if (media)
              RowTile(
                chevron: false,
                leading: Icon(
                  Icons.photo_library_outlined,
                  size: 22,
                  color: c.accent,
                ),
                title: const Text('图片与视频'),
                subtitle: const Text('agent 截的图、生成的图片和视频，粘贴过的图片'),
                onTap: () => Navigator.of(sheet).pop('media'),
              ),
            if (desk != null)
              StatefulBuilder(
                builder: (context, setSheet) {
                  void flip(bool value) {
                    setSheet(() => on = value);
                    onDesk?.call(value);
                  }

                  return RowTile(
                    chevron: false,
                    leading: Icon(
                      Icons.desktop_mac_outlined,
                      size: 22,
                      color: c.accent,
                    ),
                    title: const Text('电脑布局'),
                    subtitle: const Text('缩到屏幕宽度，双指放大后可以滑动'),
                    trailing: HiveSwitch(value: on, onChanged: flip),
                    onTap: () => flip(!on),
                  );
                },
              ),
            RowTile(
              chevron: false,
              leading: Icon(Icons.close_rounded, size: 22, color: c.danger),
              title: Text(
                tab.agent == null ? '关闭终端' : '关闭会话',
                style: TextStyle(color: c.danger),
              ),
              onTap: () => Navigator.of(sheet).pop('close'),
            ),
          ],
        ),
      );
    },
  );
}
