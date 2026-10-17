import 'prompt.dart';

/// 手机上还没决定的 PermissionRequest。
///
/// 对话流上的 permission 帧可能比标签轮询更早到。标签若还停在挂起之前
/// （不是 waiting，提示也是空的），不能把这张卡片清掉。见过 waiting 之后
/// 再离开，才算这一轮结束。
class LivePermission {
  const LivePermission({
    this.requestId,
    this.tool = '',
    this.input,
    this.sawWaiting = false,
    this.settled = false,
  });

  final String? requestId;
  final String tool;
  final Object? input;
  final bool sawWaiting;
  final bool settled;

  LivePermission receive({
    required String requestId,
    required String tool,
    required Object? input,
    required bool alreadyWaiting,
  }) {
    return LivePermission(
      requestId: requestId,
      tool: tool,
      input: input,
      sawWaiting: sawWaiting || alreadyWaiting,
    );
  }

  LivePermission observe({required bool waiting}) {
    if (waiting) {
      return LivePermission(
        requestId: requestId,
        tool: tool,
        input: input,
        sawWaiting: true,
        settled: settled,
      );
    }
    if (sawWaiting) return const LivePermission();
    return this;
  }

  LivePermission settle() {
    return LivePermission(sawWaiting: sawWaiting, settled: true);
  }
}

/// 和桌面审批摘要同一组字段，截到 200 个字符，不加省略号。
String permissionSummary(Object? input) {
  if (input is! Map) return '';
  for (final key in ['command', 'file_path', 'path', 'url', 'pattern']) {
    final value = input[key];
    if (value is! String || value.isEmpty) continue;
    final runes = value.runes.toList();
    if (runes.length <= 200) return value;
    return String.fromCharCodes(runes.take(200));
  }
  return '';
}

String? permissionBehavior(String key) => switch (key) {
  allowKey => 'allow',
  denyKey => 'deny',
  _ => null,
};
