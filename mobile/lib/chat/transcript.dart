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

/// 连续的工具调用折成一行。桌面历史详情默认也这样，避免每条工具占一整块。
List<TranscriptEntry> foldTranscript(List<ChatMessage> messages) {
  final out = <TranscriptEntry>[];
  final run = <ChatBlock>[];

  void flush() {
    if (run.isEmpty) return;
    out.add(TranscriptEntry.tools(List<ChatBlock>.of(run)));
    run.clear();
  }

  for (final message in messages) {
    if (message.blocks.isEmpty) continue;
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

String? chatTimeLabel(int? timestamp) {
  if (timestamp == null || timestamp <= 0) return null;
  final time = DateTime.fromMillisecondsSinceEpoch(timestamp).toLocal();
  final hour = time.hour.toString().padLeft(2, '0');
  final minute = time.minute.toString().padLeft(2, '0');
  return '${time.month}月${time.day}日 $hour:$minute';
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

String chatWho(String role, String? agent) {
  switch (role) {
    case 'user':
      return '你';
    case 'tool':
      return '工具';
    case 'reasoning':
      return '思考';
    case 'system':
      return '系统';
    default:
      return agentLabel(agent);
  }
}
