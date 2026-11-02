import '../l10n/l10n.dart';
import 'models.dart';

bool isToolBlock(ChatBlock block) =>
    block.type == 'tool-call' || block.type == 'tool-result';

/// 对话里的一行：一条消息，或折成一行的连续工具调用。
class TranscriptEntry {
  const TranscriptEntry.message(this.message, this.blocks) : tools = false;
  const TranscriptEntry.tools(this.blocks) : message = null, tools = true;

  final ChatMessage? message;
  final List<ChatBlock> blocks;
  final bool tools;
}

/// Claude 执行排队的斜杠命令时，会话文件里先写一条命令原文，紧接着又写一条命令外壳，
/// 电脑那边把外壳也换成命令原文，两条只差几毫秒。同一句话两秒内又出现就只留第一条。
const _repeatWindowMs = 2000;

bool _repeatsUser(ChatMessage? last, ChatMessage message) {
  if (last == null || message.role != 'user') return false;
  final a = last.timestamp, b = message.timestamp;
  if (a == null || b == null || (b - a).abs() > _repeatWindowMs) return false;
  final text = message.text.trim();
  return text.isNotEmpty &&
      text == last.text.trim() &&
      message.blocks.every((block) => block.type == 'text');
}

/// 连续的工具调用折成一行。桌面历史详情默认也这样，避免每条工具占一整块。
List<TranscriptEntry> foldTranscript(List<ChatMessage> messages) {
  final out = <TranscriptEntry>[];
  final run = <ChatBlock>[];
  ChatMessage? lastUser;

  void flush() {
    if (run.isEmpty) return;
    out.add(TranscriptEntry.tools(List<ChatBlock>.of(run)));
    run.clear();
  }

  for (final message in messages) {
    if (message.blocks.isEmpty) continue;
    if (_repeatsUser(lastUser, message)) continue;
    if (message.role == 'user') lastUser = message;
    if (message.role == 'tool' || message.blocks.every(isToolBlock)) {
      run.addAll(message.blocks);
      continue;
    }
    var i = 0;
    while (i < message.blocks.length) {
      final tool = isToolBlock(message.blocks[i]);
      final start = i;
      while (i < message.blocks.length &&
          isToolBlock(message.blocks[i]) == tool) {
        i++;
      }
      final slice = message.blocks.sublist(start, i);
      if (tool) {
        run.addAll(slice);
      } else {
        flush();
        out.add(TranscriptEntry.message(message, slice));
      }
    }
  }
  flush();
  return out;
}

int toolRunCount(List<ChatBlock> blocks) {
  final calls = blocks.where((block) => block.type == 'tool-call').length;
  if (calls > 0) return calls;
  return blocks.length;
}

String? chatTimeLabel(AppLocalizations l, int? timestamp, {required bool h24}) {
  if (timestamp == null || timestamp <= 0) return null;
  return monthDayTime(
    l,
    DateTime.fromMillisecondsSinceEpoch(timestamp),
    h24: h24,
  );
}

String agentLabel(String? agent) {
  switch (agent) {
    case 'claude':
      return 'Claude';
    case 'codex':
      return 'Codex';
    case 'grok':
      return 'Grok';
    case 'cursor':
      return 'Cursor';
    default:
      if (agent == null || agent.isEmpty) return 'Agent';
      return agent;
  }
}

String chatWho(AppLocalizations l, String role, String? agent) {
  switch (role) {
    case 'user':
      return l.chatRoleYou;
    case 'tool':
      return l.chatRoleTool;
    case 'reasoning':
      return l.chatRoleReasoning;
    case 'system':
      return l.chatRoleSystem;
    default:
      return agentLabel(agent);
  }
}
