import 'dart:io';
import 'dart:math' as math;

import '../app_state.dart';

/// 语音转写引擎预设。
enum VoiceEngine {
  native('native'),
  openai('openai'),
  groq('groq'),
  siliconflow('siliconflow'),
  volcengine('volcengine'),
  custom('custom');

  const VoiceEngine(this.id);
  final String id;

  static VoiceEngine fromId(String? id) {
    for (final e in values) {
      if (e.id == id) return e;
    }
    return VoiceEngine.native;
  }

  /// 是否走手机端系统离线语音识别（不上传音频到云端）。
  bool get isNative => this == VoiceEngine.native;

  /// 是否走 OpenAI 兼容 `/v1/audio/transcriptions` multipart 接口。
  bool get isOpenAICompatible =>
      this == VoiceEngine.openai ||
      this == VoiceEngine.groq ||
      this == VoiceEngine.siliconflow ||
      this == VoiceEngine.custom;

  /// 是否支持 `prompt` 词表提示（SenseVoice / 豆包 Flash 不传 OpenAI prompt）。
  bool get supportsPrompt =>
      this == VoiceEngine.openai ||
      this == VoiceEngine.groq ||
      this == VoiceEngine.custom;

  /// 该引擎最稳的客户端录音格式（零转码依赖）。
  String get recordFormat => this == VoiceEngine.volcengine ? 'wav' : 'm4a';

  /// 安全存储中保存 API Key 的键名。
  String get secretKey => 'asr_$id';

  /// 默认接口地址（OpenAI 兼容为 base URL，不含 `/audio/transcriptions`；豆包为完整接口 URL）。
  String get defaultBaseUrl => switch (this) {
    VoiceEngine.native => '',
    VoiceEngine.openai => 'https://api.openai.com/v1',
    VoiceEngine.groq => 'https://api.groq.com/openai/v1',
    VoiceEngine.siliconflow => 'https://api.siliconflow.cn/v1',
    VoiceEngine.volcengine =>
      'https://openspeech.bytedance.com/api/v3/auc/bigmodel/recognize/flash',
    VoiceEngine.custom => '',
  };

  /// 默认模型名。
  String get defaultModel => switch (this) {
    VoiceEngine.native => '',
    VoiceEngine.openai => 'gpt-4o-mini-transcribe',
    VoiceEngine.groq => 'whisper-large-v3-turbo',
    VoiceEngine.siliconflow => 'FunAudioLLM/SenseVoiceSmall',
    VoiceEngine.volcengine => 'volc.bigasr.auc_turbo',
    VoiceEngine.custom => 'whisper-1',
  };
}

/// 运行时生效的语音配置快照（不含敏感 API Key）。
class VoiceConfig {
  const VoiceConfig({
    required this.engine,
    required this.baseUrl,
    required this.model,
    required this.language,
    required this.vocab,
    this.allowInsecureHttp = false,
  });

  factory VoiceConfig.fromSettings(AppSettings s) {
    final engine = VoiceEngine.fromId(s.voiceEngine);
    final customUrl = s.voiceBaseUrl.trim();
    final customModel = s.voiceModel.trim();
    return VoiceConfig(
      engine: engine,
      baseUrl: customUrl.isNotEmpty ? customUrl : engine.defaultBaseUrl,
      model: customModel.isNotEmpty ? customModel : engine.defaultModel,
      language: s.voiceLanguage.trim(),
      vocab: s.voiceVocab.trim(),
      allowInsecureHttp: s.voiceAllowInsecureHttp,
    );
  }

  final VoiceEngine engine;
  final String baseUrl;
  final String model;

  /// `''` 自动、`'zh'` 中文、`'en'` 英文。
  final String language;
  final String vocab;

  /// 是否允许明文 HTTP（默认仅允许 HTTPS，避免把 API Key 与语音暴露在明文链路上）。
  final bool allowInsecureHttp;

  /// 传给系统原生识别器的 Locale 标识（空串时返回 null 跟随系统）。
  String? get nativeLocale => switch (language) {
    'zh' => 'zh-CN',
    'en' => 'en-US',
    _ => null,
  };
}

/// 校验语音接口 URL：默认强制 HTTPS；仅当 [allowInsecureHttp] 为真且目标为本机/局域网私网地址时放行明文 HTTP。
Uri? parseVoiceEndpointUri(String raw, {bool allowInsecureHttp = false}) {
  final trimmed = raw.trim();
  if (trimmed.isEmpty) return null;
  final uri = Uri.tryParse(trimmed);
  if (uri == null || !uri.hasScheme || uri.host.isEmpty) return null;
  if (uri.scheme == 'https') return uri;
  if (allowInsecureHttp && uri.scheme == 'http' && isLocalNetworkHost(uri.host)) {
    return uri;
  }
  return null;
}

/// 判断主机是否为本机/局域网私网 IP 字面量（不接受域名或 .local，避免 DNS/代理绕过私网约束）。
bool isLocalNetworkHost(String rawHost) {
  final host = rawHost.trim().toLowerCase();
  if (host.isEmpty) return false;
  final bare = host.startsWith('[') && host.endsWith(']')
      ? host.substring(1, host.length - 1)
      : host;
  final ip = InternetAddress.tryParse(bare);
  if (ip == null) return false;
  final bytes = ip.rawAddress;
  if (ip.type == InternetAddressType.IPv4 && bytes.length == 4) {
    final a = bytes[0];
    final b = bytes[1];
    if (a == 127 || a == 10) return true;
    if (a == 172 && b >= 16 && b <= 31) return true;
    if (a == 192 && b == 168) return true;
    if (a == 100 && b >= 64 && b <= 127) return true; // Tailscale CGNAT
    if (a == 169 && b == 254) return true; // link-local
    return false;
  }
  if (ip.type == InternetAddressType.IPv6 && bytes.length == 16) {
    if (ip.isLoopback) return true;
    // fc00::/7 (ULA) 或 fe80::/10 (link-local)
    if ((bytes[0] & 0xfe) == 0xfc) return true;
    if (bytes[0] == 0xfe && (bytes[1] & 0xc0) == 0x80) return true;
    return false;
  }
  return false;
}

/// 构造编程术语提示词（同时服务 OpenAI `prompt` 与 iOS/Android 离线词表提示）。
class VoicePromptHints {
  static final _sep = RegExp(r'[,，、;\n]+');

  /// 从上下文（项目名、分支、Agent 名、成员名）与用户自定义词表提取去重短词，最多 100 项（iOS 上限）。
  static List<String> buildTerms({
    String vocab = '',
    Iterable<String> contextTerms = const [],
    int maxTerms = 100,
  }) {
    final seen = <String>{};
    final out = <String>[];
    void addRaw(String raw) {
      for (final part in raw.split(_sep)) {
        final t = part.trim();
        if (t.isEmpty || t.length > 64) continue;
        if (seen.add(t.toLowerCase())) {
          out.add(t);
          if (out.length >= maxTerms) return;
        }
      }
    }

    for (final term in contextTerms) {
      addRaw(term);
      if (out.length >= maxTerms) break;
    }
    if (out.length < maxTerms) {
      addRaw(vocab);
    }
    return out;
  }

  /// 构造 OpenAI 兼容 `/v1/audio/transcriptions` 的 `prompt`（自然语境词表，限制 <= 420 字符避免超出 224 tokens）。
  static String buildOpenAIPrompt({
    String vocab = '',
    Iterable<String> contextTerms = const [],
    int maxChars = 420,
  }) {
    final terms = buildTerms(vocab: vocab, contextTerms: contextTerms);
    if (terms.isEmpty) return '';
    final buf = StringBuffer('Technical coding context: ');
    for (var i = 0; i < terms.length; i++) {
      final piece = i == 0 ? terms[i] : ', ${terms[i]}';
      if (buf.length + piece.length > maxChars) break;
      buf.write(piece);
    }
    return buf.toString();
  }
}

/// 将转写文本合并进已有草稿的选区/光标处，并在两端均为 ASCII 字母数字时自动补空格。
({String text, int cursor}) mergeVoiceDraft({
  required String current,
  required int start,
  required int end,
  required String incoming,
}) {
  final clean = incoming.trim();
  if (clean.isEmpty) {
    final clamped = math.max(0, math.min(current.length, end));
    return (text: current, cursor: clamped);
  }
  final s = start < 0 ? current.length : start.clamp(0, current.length);
  final e = end < 0 ? current.length : end.clamp(s, current.length);
  final before = current.substring(0, s);
  final after = current.substring(e);

  final needLeftSpace =
      before.isNotEmpty &&
      _isAsciiWord(before.codeUnitAt(before.length - 1)) &&
      _isAsciiWord(clean.codeUnitAt(0));
  final needRightSpace =
      after.isNotEmpty &&
      _isAsciiWord(clean.codeUnitAt(clean.length - 1)) &&
      _isAsciiWord(after.codeUnitAt(0));

  final inserted =
      '${needLeftSpace ? ' ' : ''}$clean${needRightSpace ? ' ' : ''}';
  final next = '$before$inserted$after';
  final cursor = before.length + (needLeftSpace ? 1 : 0) + clean.length;
  return (text: next, cursor: cursor);
}

/// 原样插入剪贴板文本（保留行首缩进和换行，不做 trim）。
({String text, int cursor}) insertRawDraft({
  required String current,
  required int start,
  required int end,
  required String raw,
}) {
  if (raw.isEmpty) {
    final clamped = math.max(0, math.min(current.length, end));
    return (text: current, cursor: clamped);
  }
  final s = start < 0 ? current.length : start.clamp(0, current.length);
  final e = end < 0 ? current.length : end.clamp(s, current.length);
  final next = current.replaceRange(s, e, raw);
  return (text: next, cursor: s + raw.length);
}

/// 插入文件路径（前接非空白字符或路径时自动补空格分隔，避免 `/a/b` + `/c/d` 粘连）。
({String text, int cursor}) insertPathDraft({
  required String current,
  required int start,
  required int end,
  required String path,
}) {
  final clean = path.trim();
  if (clean.isEmpty) {
    final clamped = math.max(0, math.min(current.length, end));
    return (text: current, cursor: clamped);
  }
  final s = start < 0 ? current.length : start.clamp(0, current.length);
  final e = end < 0 ? current.length : end.clamp(s, current.length);
  final before = current.substring(0, s);
  final after = current.substring(e);
  final needLeft =
      before.isNotEmpty && !before.endsWith(' ') && !before.endsWith('\n');
  final needRight =
      after.isNotEmpty && !after.startsWith(' ') && !after.startsWith('\n');
  final inserted = '${needLeft ? ' ' : ''}$clean${needRight ? ' ' : ''}';
  final next = '$before$inserted$after';
  return (
    text: next,
    cursor: before.length + (needLeft ? 1 : 0) + clean.length,
  );
}

bool _isAsciiWord(int code) =>
    (code >= 0x30 && code <= 0x39) ||
    (code >= 0x41 && code <= 0x5a) ||
    (code >= 0x61 && code <= 0x7a);
