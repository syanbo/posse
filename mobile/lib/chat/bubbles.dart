import 'dart:convert';

import 'package:flutter/material.dart';

import 'markdown_view.dart';
import 'models.dart';
import 'transcript.dart';
import '../ui/theme.dart';
import 'chat_style.dart';
import 'echo.dart';
import '../ui/widgets.dart';

/// 自己的话：靠右、强调色淡底。
Widget _mine(BuildContext context, Widget body) {
  return Align(
    alignment: Alignment.centerRight,
    child: Container(
      constraints: BoxConstraints(
        maxWidth: MediaQuery.sizeOf(context).width * 0.8,
      ),
      margin: const EdgeInsets.fromLTRB(48, 4, 16, 10),
      padding: const EdgeInsets.fromLTRB(16, 10, 16, 12),
      decoration: BoxDecoration(
        color: chatTint(context),
        borderRadius: BorderRadius.circular(10),
      ),
      child: body,
    ),
  );
}

/// 刚发出去、agent 还没写进会话文件的话。样子和自己的话一样，时间处写发送中或已发送。
class EchoBubble extends StatelessWidget {
  const EchoBubble({super.key, required this.echo});

  final LocalEcho echo;

  @override
  Widget build(BuildContext context) {
    final muted = chatMuted(context);
    return Opacity(
      opacity: echo.sent ? 1 : 0.6,
      child: _mine(
        context,
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(
                  '你',
                  style: TextStyle(
                    fontSize: 12,
                    fontWeight: FontWeight.w500,
                    color: chatAccent(context),
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  echo.sent ? '已发送' : '发送中…',
                  style: TextStyle(fontSize: 11, color: muted),
                ),
              ],
            ),
            const SizedBox(height: 6),
            if (echo.images > 0)
              Text(
                '${echo.images} 张图片',
                style: TextStyle(fontSize: 13, color: muted),
              ),
            if (echo.text.isNotEmpty)
              Text(
                echo.text,
                style: const TextStyle(fontSize: 15, height: 1.55),
              ),
          ],
        ),
      ),
    );
  }
}

/// 对话最后一行：agent 正在干活。照 Claude Code，转动的星号；有终端底部的状态行
/// （"Thinking… (12s · ↓ 1.2k tokens)"）就照着写，看得出它还在动，否则写正在用的工具。
class WorkingRow extends StatelessWidget {
  const WorkingRow({super.key, this.status, this.tool});

  final String? status;
  final String? tool;

  @override
  Widget build(BuildContext context) {
    final c = HiveColors.of(context);
    final status = this.status;
    final tool = this.tool;
    final text = status != null && status.isNotEmpty
        ? status
        : tool == null || tool.isEmpty
        ? '工作中…'
        : '正在用 $tool…';
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 12),
      child: Row(
        children: [
          WorkingStar(size: 14, color: c.accent),
          const SizedBox(width: 8),
          Flexible(
            child: Text(
              text,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: 13,
                color: c.muted,
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class MessageBubble extends StatelessWidget {
  const MessageBubble({
    super.key,
    required this.message,
    required this.blocks,
    required this.agent,
  });

  final ChatMessage message;
  final List<ChatBlock> blocks;
  final String? agent;

  @override
  Widget build(BuildContext context) {
    final mine = message.role == 'user';
    final muted = chatMuted(context);
    final who = chatWho(message.role, agent);
    final time = chatTimeLabel(message.timestamp);
    final body = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Text(
              who,
              style: TextStyle(
                fontSize: 12,
                fontWeight: FontWeight.w500,
                color: mine ? chatAccent(context) : muted,
              ),
            ),
            if (time != null) ...[
              const SizedBox(width: 8),
              Text(
                time,
                style: TextStyle(
                  fontSize: 11,
                  color: muted,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ],
        ),
        const SizedBox(height: 6),
        for (final block in blocks) _block(context, message.role, block),
      ],
    );
    // 回复直接铺在底上。只有自己的话靠右、强调色淡底。
    if (!mine) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(16, 4, 16, 10),
        child: body,
      );
    }
    return _mine(context, body);
  }

  Widget _block(BuildContext context, String role, ChatBlock block) {
    final muted = chatMuted(context);
    if (block.type == 'image-ref') {
      return Text(
        block.path ?? block.url ?? '图片',
        style: TextStyle(fontSize: 13, color: muted),
      );
    }
    final text = block.text ?? '';
    if (role == 'user') {
      return Text(text, style: const TextStyle(fontSize: 15, height: 1.55));
    }
    final style = role == 'reasoning'
        ? TextStyle(fontStyle: FontStyle.italic, color: muted, height: 1.55)
        : role == 'system'
        ? TextStyle(fontSize: 13, color: muted, height: 1.5)
        : const TextStyle(fontSize: 15, height: 1.55);
    return MarkdownView(text, style: style);
  }
}

class ToolRun extends StatefulWidget {
  const ToolRun({super.key, required this.blocks, this.initiallyOpen = false});

  final List<ChatBlock> blocks;
  final bool initiallyOpen;

  @override
  State<ToolRun> createState() => _ToolRunState();
}

class _ToolRunState extends State<ToolRun> {
  late var _open = widget.initiallyOpen;

  @override
  Widget build(BuildContext context) {
    final muted = chatMuted(context);
    final count = toolRunCount(widget.blocks);
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () => setState(() => _open = !_open),
            child: Container(
              padding: const EdgeInsets.fromLTRB(8, 5, 8, 5),
              decoration: BoxDecoration(
                color: HiveColors.of(context).fill,
                borderRadius: BorderRadius.circular(Rd.button),
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.build_outlined, size: 14, color: muted),
                  const SizedBox(width: 6),
                  Text(
                    '工具调用 $count 次',
                    style: TextStyle(fontSize: 12.5, color: muted),
                  ),
                  const SizedBox(width: 2),
                  Icon(
                    _open
                        ? Icons.expand_less_rounded
                        : Icons.expand_more_rounded,
                    size: 16,
                    color: muted,
                  ),
                ],
              ),
            ),
          ),
          if (_open)
            for (final block in widget.blocks) _toolBody(context, block, muted),
        ],
      ),
    );
  }

  Widget _toolBody(BuildContext context, ChatBlock block, Color muted) {
    final title = block.isError
        ? '工具失败'
        : (block.name?.isNotEmpty ?? false)
        ? block.name!
        : '工具';
    final detail = block.type == 'tool-result'
        ? (block.output ?? '')
        : _preview(block.input);
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(top: 6),
      padding: const EdgeInsets.fromLTRB(10, 8, 10, 8),
      decoration: BoxDecoration(
        color: HiveColors.of(context).code,
        borderRadius: BorderRadius.circular(Rd.button),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            title,
            style: TextStyle(
              fontSize: 12,
              fontWeight: FontWeight.w500,
              color: block.isError
                  ? Theme.of(context).colorScheme.error
                  : muted,
            ),
          ),
          if (detail.isNotEmpty)
            Text(
              detail,
              maxLines: 8,
              overflow: TextOverflow.ellipsis,
              style: mono(12, height: 1.45, color: muted),
            ),
        ],
      ),
    );
  }
}

String _preview(Object? input) {
  if (input == null) return '';
  if (input is String) return input;
  try {
    return const JsonEncoder.withIndent('  ').convert(input);
  } catch (_) {
    return '$input';
  }
}

String toolKey(List<ChatBlock> blocks, int index) {
  final ids = blocks
      .map((block) => block.callId)
      .whereType<String>()
      .where((id) => id.isNotEmpty)
      .join(',');
  if (ids.isNotEmpty) return ids;
  return 'tools-$index-${blocks.length}';
}
