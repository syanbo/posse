class ChatBlock {
  const ChatBlock({
    required this.type,
    this.text,
    this.name,
    this.input,
    this.callId,
    this.output,
    this.isError = false,
    this.path,
    this.url,
  });

  final String type;
  final String? text;
  final String? name;
  final Object? input;
  final String? callId;
  final String? output;
  final bool isError;
  final String? path;
  final String? url;

  factory ChatBlock.fromJson(Map<String, Object?> json) {
    return ChatBlock(
      type: json['type'] as String? ?? 'text',
      text: json['text'] as String?,
      name: json['name'] as String?,
      input: json['input'],
      callId: json['callId'] as String?,
      output: json['output'] as String?,
      isError: json['isError'] == true,
      path: json['path'] as String?,
      url: json['url'] as String?,
    );
  }
}

class ChatMessage {
  const ChatMessage({
    required this.id,
    required this.role,
    required this.blocks,
    required this.timestamp,
    required this.source,
    this.transcriptOffset,
    this.parentId,
  });

  final String id;
  final String role;
  final List<ChatBlock> blocks;
  final int? timestamp;
  final String source;
  final int? transcriptOffset;
  final String? parentId;

  String get text => blocks
      .where((block) => block.type == 'text')
      .map((block) => block.text ?? '')
      .join('\n');

  factory ChatMessage.fromJson(Map<String, Object?> json) {
    final blocks = <ChatBlock>[];
    final raw = json['blocks'];
    if (raw is List) {
      for (final block in raw) {
        if (block is Map) {
          blocks.add(ChatBlock.fromJson(block.cast<String, Object?>()));
        }
      }
    }
    return ChatMessage(
      id: json['id'] as String? ?? '',
      role: json['role'] as String? ?? 'assistant',
      blocks: blocks,
      timestamp: json['timestamp'] is num
          ? (json['timestamp'] as num).toInt()
          : null,
      source: json['source'] as String? ?? 'transcript',
      transcriptOffset: json['transcriptOffset'] is num
          ? (json['transcriptOffset'] as num).toInt()
          : null,
      parentId: json['parentId'] as String?,
    );
  }
}

List<ChatMessage> parseMessages(Object? raw) {
  if (raw is! List) return const [];
  final out = <ChatMessage>[];
  for (final item in raw) {
    if (item is! Map) continue;
    final message = ChatMessage.fromJson(item.cast<String, Object?>());
    if (message.id.isNotEmpty) out.add(message);
  }
  return out;
}

const sourcePriority = {'transcript': 3, 'hook': 2, 'scrape': 1};

int sourceRank(String source) => sourcePriority[source] ?? 0;

String semanticRowId(ChatMessage message) {
  const suffix = ':reasoning';
  if (message.role == 'reasoning' && message.id.endsWith(suffix)) {
    return message.id.substring(0, message.id.length - suffix.length);
  }
  return message.id;
}

bool shareTranscriptRow(ChatMessage first, ChatMessage second) {
  if (first.transcriptOffset != null && second.transcriptOffset != null) {
    return first.transcriptOffset == second.transcriptOffset;
  }
  return first.role == 'reasoning' &&
      second.role == 'assistant' &&
      semanticRowId(first) == second.id;
}
