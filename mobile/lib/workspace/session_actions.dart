import 'package:flutter/material.dart';

import '../app_state.dart';
import '../chat/transcript.dart' show chatWho;
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'models.dart';

/// 确认后关闭一个会话或终端，返回是否关掉了。对话记录还在电脑的会话历史里，关掉的只是终端和里面的程序。
Future<bool> closeSession(
  BuildContext context,
  PosseAppModel model,
  SessionTab tab,
) async {
  final l = context.l10n;
  final who = tab.agent == null ? null : chatWho(l, 'assistant', tab.agent);
  final ok = await showPosseDialog<bool>(
    context: context,
    builder: (dialog) => AlertDialog(
      title: Text(
        who == null
            ? l.sessionCloseTerminalConfirm
            : l.sessionCloseSessionConfirm,
      ),
      content: Text(
        who == null
            ? l.sessionCloseTerminalBody(cleanTitle(tab.title))
            : l.sessionCloseSessionBody(who),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(dialog, false),
          child: Text(l.commonCancel),
        ),
        FilledButton(
          style: FilledButton.styleFrom(
            backgroundColor: PosseColors.of(dialog).danger,
          ),
          onPressed: () => Navigator.pop(dialog, true),
          child: Text(l.commonClose),
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
          .showSnackBar(SnackBar(content: Text(friendlyError(error, l))));
    }
    return false;
  }
  await model.refreshWorktrees();
  return true;
}

/// 会话页、终端页右上角的"更多"：从底部滑上来的面板，和电脑列表的操作面板一样。
/// [switchLabel] 是切到另一种看法（对话或终端），为空不显示；[desk] 不为空时有"电脑布局"开关，
/// 拨动立刻生效、面板不收。[media] 时有"图片与视频"，[tools] 时有"技能与工具"。
/// 返回 'switch'、'media'、'tools' 或 'close'，点外面收起时为 null。
Future<String?> showSessionMore(
  BuildContext context, {
  required SessionTab tab,
  String? switchLabel,
  IconData switchIcon = Icons.swap_horiz_rounded,
  bool? desk,
  ValueChanged<bool>? onDesk,
  bool media = false,
  bool tools = false,
}) {
  // 放在 builder 外面：拨开关会改设置、整个 App 重建，面板的 builder 跟着重跑，放在里面就被打回初值
  var on = desk ?? false;
  return showModalBottomSheet<String>(
    context: context,
    showDragHandle: true,
    builder: (sheet) {
      final c = PosseColors.of(sheet);
      final l = sheet.l10n;
      return SafeArea(
        top: false,
        child: Section(
          inset: 48,
          children: [
            if (switchLabel != null)
              RowTile(
                chevron: false,
                leading: Icon(switchIcon, size: Ic.button, color: c.accent),
                title: Text(switchLabel),
                onTap: () => Navigator.of(sheet).pop('switch'),
              ),
            if (media)
              RowTile(
                chevron: false,
                leading: Icon(
                  Icons.photo_library_rounded,
                  size: Ic.button,
                  color: c.accent,
                ),
                title: Text(l.sessionMedia),
                subtitle: Text(l.sessionMediaNote),
                onTap: () => Navigator.of(sheet).pop('media'),
              ),
            if (tools)
              RowTile(
                chevron: false,
                leading: Icon(
                  Icons.extension_rounded,
                  size: Ic.button,
                  color: c.accent,
                ),
                title: Text(l.sessionTools),
                subtitle: Text(l.sessionToolsNote),
                onTap: () => Navigator.of(sheet).pop('tools'),
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
                      Icons.desktop_mac_rounded,
                      size: Ic.button,
                      color: c.accent,
                    ),
                    title: Text(l.sessionDeskLayout),
                    subtitle: Text(l.sessionDeskLayoutNote),
                    trailing: PosseSwitch(value: on, onChanged: flip),
                    onTap: () => flip(!on),
                  );
                },
              ),
            RowTile(
              chevron: false,
              leading: Icon(
                Icons.close_rounded,
                size: Ic.button,
                color: c.danger,
              ),
              title: Text(
                tab.agent == null
                    ? l.sessionCloseTerminal
                    : l.sessionCloseSession,
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
