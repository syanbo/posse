import 'dart:convert';

import '../terminal/frames.dart';

/// 用户在设置里加的终端快捷键。三种：一段文字（可以带回车）、Ctrl/Alt 加一个键、常用的特殊键。
class CustomKey {
  const CustomKey({
    required this.kind,
    required this.label,
    required this.value,
    this.enter = false,
  });

  /// text、ctrl、alt 或 special。
  final String kind;
  final String label;

  /// text 是要输入的文字，ctrl/alt 是一个字符，special 是 [specialKeys] 里的名字。
  final String value;

  /// 只对 text 有效：文字之后另发一个回车，TUI 才不会把回车当成粘贴内容。
  final bool enter;

  static const kinds = ['text', 'ctrl', 'alt', 'special'];

  /// 写进 PTY 的字节；配置不完整时为空，按了也不发。
  List<int> get bytes => switch (kind) {
    'text' => utf8.encode(value),
    'ctrl' => _ctrl(value),
    'alt' => value.isEmpty ? const [] : [0x1b, ...utf8.encode(value)],
    'special' => specialKeys[value] ?? const [],
    _ => const [],
  };

  /// 快捷键栏上显示的字：没起名时按内容生成。
  String get shown {
    if (label.trim().isNotEmpty) return label.trim();
    return switch (kind) {
      'ctrl' => '^${value.toUpperCase()}',
      'alt' => '⌥${value.toUpperCase()}',
      'special' => value,
      _ => value.length > 12 ? '${value.substring(0, 12)}…' : value,
    };
  }

  bool get valid => bytes.isNotEmpty;

  Map<String, Object?> toJson() => {
    'kind': kind,
    'label': label,
    'value': value,
    if (enter) 'enter': true,
  };

  static CustomKey? fromJson(Object? json) {
    if (json is! Map) return null;
    final kind = json['kind'];
    final value = json['value'];
    if (kind is! String || !kinds.contains(kind) || value is! String) {
      return null;
    }
    final key = CustomKey(
      kind: kind,
      label: json['label'] is String ? json['label'] as String : '',
      value: value,
      enter: json['enter'] == true,
    );
    return key.valid ? key : null;
  }
}

/// Ctrl 加字母和 @[\]^_ 这几个符号是 C0 控制字符；? 是 DEL。其他字符没有对应的控制码。
List<int> _ctrl(String value) {
  if (value.length != 1) return const [];
  final c = value.toUpperCase().codeUnitAt(0);
  if (c == 0x3f) return const [0x7f];
  if (c >= 0x40 && c <= 0x5f) return [c & 0x1f];
  return const [];
}

/// 软键盘上没有、内置快捷键里也没有的常用键，按 xterm 的序列。
const specialKeys = <String, List<int>>{
  'PgUp': [0x1b, 0x5b, 0x35, 0x7e],
  'PgDn': [0x1b, 0x5b, 0x36, 0x7e],
  'Home': [0x1b, 0x5b, 0x48],
  'End': [0x1b, 0x5b, 0x46],
  'Del': [0x1b, 0x5b, 0x33, 0x7e],
  'Ins': [0x1b, 0x5b, 0x32, 0x7e],
  '⌫': [0x7f],
  'Space': [0x20],
  'F1': [0x1b, 0x4f, 0x50],
  'F2': [0x1b, 0x4f, 0x51],
  'F3': [0x1b, 0x4f, 0x52],
  'F4': [0x1b, 0x4f, 0x53],
  'F5': [0x1b, 0x5b, 0x31, 0x35, 0x7e],
  'F6': [0x1b, 0x5b, 0x31, 0x37, 0x7e],
  'F7': [0x1b, 0x5b, 0x31, 0x38, 0x7e],
  'F8': [0x1b, 0x5b, 0x31, 0x39, 0x7e],
  'F9': [0x1b, 0x5b, 0x32, 0x30, 0x7e],
  'F10': [0x1b, 0x5b, 0x32, 0x31, 0x7e],
  'F11': [0x1b, 0x5b, 0x32, 0x33, 0x7e],
  'F12': [0x1b, 0x5b, 0x32, 0x34, 0x7e],
};

/// 快捷键栏上的一个键：内置的（[builtin] 是它的名字）或自己加的（[custom]）。
/// 键盘开关不在其中，固定在最左边。
class BarKey {
  const BarKey.builtin(String this.builtin) : custom = null;
  const BarKey.custom(CustomKey this.custom) : builtin = null;

  final String? builtin;
  final CustomKey? custom;

  /// 内置的终端键（Esc、方向键这些），图片和粘贴不是。
  TermKey? get termKey {
    for (final key in termKeys) {
      if (key.label == builtin) return key;
    }
    return null;
  }

  Object? toJson() => builtin != null ? {'builtin': builtin} : custom!.toJson();

  static BarKey? fromJson(Object? json) {
    if (json is Map && json['builtin'] is String) {
      final name = json['builtin'] as String;
      // 改过名的是改名前存的，位置是当时自动补上的，丢掉让 normalizeBarKeys 放回默认位置
      if (renamedBuiltins.containsKey(name)) return null;
      if (relabeledBuiltins[name] case final now?) return BarKey.builtin(now);
      return builtinBarKeys.contains(name) ? BarKey.builtin(name) : null;
    }
    final key = CustomKey.fromJson(json);
    return key == null ? null : BarKey.custom(key);
  }
}

/// 快捷键栏存盘格式的版本，见 [AppSettings.decode]。
const barKeysVersion = 2;

/// 改过名的内置键：旧名字 → 新名字。旧名字存的顺序读进来不认，新名字回到默认位置。
const renamedBuiltins = {'立即发送': sendNowLabel};

/// 只改了键帽文字的内置键：旧名字 → 新名字，留在用户排好的位置上。
const relabeledBuiltins = {'退出': exitAgentLabel};

/// 内置键的名字，也是默认顺序：图片、粘贴，然后是终端键。
final builtinBarKeys = [
  'image',
  'paste',
  for (final key in termKeys) key.label,
];

/// 默认顺序：图片、粘贴、自己加的键、终端键。旧版只存了自己加的键，读进来也排成这样。
List<BarKey> defaultBarKeys([List<CustomKey> custom = const []]) => [
  const BarKey.builtin('image'),
  const BarKey.builtin('paste'),
  for (final key in custom) BarKey.custom(key),
  for (final key in termKeys) BarKey.builtin(key.label),
];

/// 去掉重复的内置键；缺的（新版本加的内置键）插在默认顺序里它前面那个键后面，
/// 前面的都没有就放最前，每个内置键都在且只出现一次。
List<BarKey> normalizeBarKeys(List<BarKey> keys) {
  final seen = <String>{};
  final out = [
    for (final key in keys)
      if (key.builtin == null || seen.add(key.builtin!)) key,
  ];
  for (var i = 0; i < builtinBarKeys.length; i++) {
    final name = builtinBarKeys[i];
    if (!seen.add(name)) continue;
    var at = 0;
    for (var j = i - 1; j >= 0; j--) {
      final k = out.indexWhere((key) => key.builtin == builtinBarKeys[j]);
      if (k >= 0) {
        at = k + 1;
        break;
      }
    }
    out.insert(at, BarKey.builtin(name));
  }
  return out;
}
