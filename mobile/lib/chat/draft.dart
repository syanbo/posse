import 'package:flutter/material.dart';

import '../l10n/l10n.dart';
import '../ui/theme.dart';
import 'chat_style.dart';
import 'transcript.dart';

/// 电脑推来的草稿（posse.draft.v1）：agent 正在写、会话文件里还没有的那段回复，
/// 是从终端屏幕上取的纯文本，Markdown 符号已经被 TUI 吃掉了。正式消息到了就撤掉。
class DraftBubble extends StatelessWidget {
  const DraftBubble({super.key, required this.text, this.agent});

  final String text;
  final String? agent;

  @override
  Widget build(BuildContext context) {
    final muted = chatMuted(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                chatWho(context.l10n, 'assistant', agent),
                style: TextStyle(
                  fontSize: Fs.small,
                  fontWeight: Fw.medium,
                  color: muted,
                ),
              ),
              const SizedBox(width: 8),
              Text(
                context.l10n.chatStreaming,
                style: TextStyle(fontSize: Fs.tiny, color: muted),
              ),
            ],
          ),
          const SizedBox(height: 6),
          for (final part in draftParts(text))
            if (part.mono)
              // 框线表格和代码行照终端里的样子排，不折行，太宽左右滑
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 4),
                child: SingleChildScrollView(
                  scrollDirection: Axis.horizontal,
                  child: Text(
                    part.text,
                    softWrap: false,
                    style: mono(Fs.small, height: Lh.code),
                  ),
                ),
              )
            else
              // 段距同 MarkdownView 的段落，换成正式消息时高度差得少
              Padding(
                padding: const EdgeInsets.only(bottom: 6),
                child: Text(
                  part.text,
                  style: const TextStyle(fontSize: Fs.body, height: Lh.read),
                ),
              ),
        ],
      ),
    );
  }
}

final _boxChars = RegExp('[│┌┐└┘├┤┬┴┼━─]');

/// 把草稿按行分成正文段和等宽段：以空白开头（代码的缩进、Codex 表格的行）或带框线的行用等宽字体。
/// 正文按空行再拆成一段一段。
List<({bool mono, String text})> draftParts(String text) {
  final parts = <({bool mono, String text})>[];
  final lines = <String>[];
  bool? mono;
  void flush() {
    while (lines.isNotEmpty && lines.last.trim().isEmpty) {
      lines.removeLast();
    }
    if (lines.isNotEmpty) parts.add((mono: mono!, text: lines.join('\n')));
    lines.clear();
  }

  for (final line in text.split('\n')) {
    if (line.trim().isEmpty) {
      if (mono == false) {
        flush();
      } else if (lines.isNotEmpty) {
        lines.add('');
      }
      continue;
    }
    final m = line.startsWith(' ') || _boxChars.hasMatch(line);
    if (m != mono) {
      flush();
      mono = m;
    }
    lines.add(line);
  }
  flush();
  return parts;
}

/// 这一帧带来了助手的正文：草稿换成正式消息的时候。
bool frameHasReply(Map<String, Object?> frame) {
  final messages = frame['messages'];
  if (messages is! List) return false;
  for (final m in messages) {
    if (m is! Map || m['role'] != 'assistant') continue;
    final blocks = m['blocks'];
    if (blocks is! List) continue;
    for (final b in blocks) {
      if (b is Map && b['type'] == 'text') {
        final t = b['text'];
        if (t is String && t.trim().isNotEmpty) return true;
      }
    }
  }
  return false;
}
