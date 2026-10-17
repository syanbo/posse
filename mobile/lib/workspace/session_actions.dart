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
  final ok = await showDialog<bool>(
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
