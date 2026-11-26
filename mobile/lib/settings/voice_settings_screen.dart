import 'dart:async';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../voice/voice_bar.dart';
import '../voice/voice_config.dart';
import '../voice/voice_service.dart';

/// 设置 › 语音输入：引擎选择、API Key 安全存储、接口/模型配置、识别语言与编程术语提示词。
/// 接口地址、模型、明文 HTTP 收在「高级设置」里；自定义引擎没有默认地址，地址放在外面当必填项。
class VoiceSettingsScreen extends StatefulWidget {
  const VoiceSettingsScreen({super.key, required this.model});

  final PosseAppModel model;

  @override
  State<VoiceSettingsScreen> createState() => _VoiceSettingsScreenState();
}

class _VoiceSettingsScreenState extends State<VoiceSettingsScreen> {
  final _keyCtrl = TextEditingController();
  late final TextEditingController _urlCtrl;
  late final TextEditingController _modelCtrl;
  late final TextEditingController _vocabCtrl;

  var _hasKey = false;
  var _busyKey = false;
  var _keyOpGen = 0;

  /// 各云端引擎是否已存 Key，给引擎列表的副标题用。
  final _savedKeys = <VoiceEngine, bool>{};

  /// 保存、清除 Key 的结果，显示在按钮下面一行，下次输入时清掉。
  String? _keyNotice;
  var _keyNoticeOk = true;

  var _advanced = false;

  VoiceEngine get _engine =>
      VoiceEngine.fromId(widget.model.settings.voiceEngine);

  @override
  void initState() {
    super.initState();
    final s = widget.model.settings;
    _urlCtrl = TextEditingController(text: s.voiceBaseUrl);
    _modelCtrl = TextEditingController(text: s.voiceModel);
    _vocabCtrl = TextEditingController(text: s.voiceVocab);
    // 改过默认值的人下次进来直接看到改了什么
    _advanced =
        (s.voiceBaseUrl.trim().isNotEmpty && _engine != VoiceEngine.custom) ||
        s.voiceModel.trim().isNotEmpty ||
        s.voiceAllowInsecureHttp;
    unawaited(_refreshKeyState());
    unawaited(_refreshSavedKeys());
  }

  @override
  void dispose() {
    _keyCtrl.dispose();
    _urlCtrl.dispose();
    _modelCtrl.dispose();
    _vocabCtrl.dispose();
    super.dispose();
  }

  Future<void> _refreshKeyState() async {
    final engine = _engine;
    final gen = _keyOpGen;
    if (engine.isNative) {
      if (mounted) setState(() => _hasKey = false);
      return;
    }
    final has = await voiceSecretStore.has(engine.secretKey);
    if (mounted && _engine == engine && _keyOpGen == gen) {
      setState(() => _hasKey = has);
    }
  }

  Future<void> _refreshSavedKeys() async {
    final cloud = [
      for (final e in VoiceEngine.values)
        if (!e.isNative) e,
    ];
    final found = await Future.wait([
      for (final e in cloud)
        voiceSecretStore.has(e.secretKey).catchError((_) => false),
    ]);
    if (!mounted) return;
    setState(() {
      for (var i = 0; i < cloud.length; i++) {
        _savedKeys[cloud[i]] = found[i];
      }
    });
  }

  void _selectEngine(VoiceEngine next) {
    if (next == _engine) return;
    _keyOpGen++;
    widget.model.updateSettings((s) {
      s.voiceEngine = next.id;
      // 切引擎时清空旧引擎的自定义 URL/Model 覆盖，避免把 OpenAI URL 错带给豆包或 Groq。
      s.voiceBaseUrl = '';
      s.voiceModel = '';
    });
    _urlCtrl.clear();
    _modelCtrl.clear();
    _keyCtrl.clear();
    setState(() {
      _hasKey = _savedKeys[next] ?? false;
      _busyKey = false;
      _keyNotice = null;
    });
    unawaited(_refreshKeyState());
  }

  void _setKeyNotice(String text, {required bool ok}) {
    _keyNotice = text;
    _keyNoticeOk = ok;
  }

  Future<void> _saveKey() async {
    final raw = _keyCtrl.text.trim();
    if (raw.isEmpty || _busyKey) return;
    final engine = _engine;
    final gen = ++_keyOpGen;
    setState(() => _busyKey = true);
    try {
      final ok = await voiceSecretStore.save(engine.secretKey, raw);
      if (!mounted || _engine != engine || _keyOpGen != gen) return;
      if (ok) {
        _keyCtrl.clear();
        setState(() {
          _hasKey = true;
          _savedKeys[engine] = true;
          _setKeyNotice(context.l10n.voiceApiKeySaved, ok: true);
        });
      } else {
        setState(
          () => _setKeyNotice(context.l10n.voiceApiKeySaveFailed, ok: false),
        );
      }
    } finally {
      if (mounted && _engine == engine && _keyOpGen == gen) {
        setState(() => _busyKey = false);
      }
    }
  }

  Future<void> _clearKey() async {
    if (_busyKey) return;
    final engine = _engine;
    final gen = ++_keyOpGen;
    setState(() => _busyKey = true);
    try {
      final ok = await voiceSecretStore.delete(engine.secretKey);
      if (!mounted || _engine != engine || _keyOpGen != gen) return;
      if (ok) {
        _keyCtrl.clear();
        setState(() {
          _hasKey = false;
          _savedKeys[engine] = false;
          _setKeyNotice(context.l10n.voiceApiKeyCleared, ok: true);
        });
      } else {
        setState(
          () => _setKeyNotice(context.l10n.voiceApiKeyClearFailed, ok: false),
        );
      }
    } finally {
      if (mounted && _engine == engine && _keyOpGen == gen) {
        setState(() => _busyKey = false);
      }
    }
  }

  Widget _label(PosseColors c, String text, {Widget? trailing}) => Row(
    children: [
      Expanded(
        child: Text(
          text,
          style: TextStyle(
            fontSize: Fs.small,
            fontWeight: Fw.medium,
            color: c.muted,
          ),
        ),
      ),
      if (trailing != null) Flexible(child: trailing),
    ],
  );

  /// 小字状态：图标加一句话，颜色跟语义走。
  Widget _status(
    PosseColors c, {
    required IconData icon,
    required Color color,
    required String text,
    Key? key,
  }) => Row(
    key: key,
    mainAxisSize: MainAxisSize.min,
    children: [
      Icon(icon, size: Ic.inline, color: color),
      const SizedBox(width: 4),
      Flexible(
        child: Text(
          text,
          style: TextStyle(fontSize: Fs.small, color: color),
        ),
      ),
    ],
  );

  InputDecoration _decoration(PosseColors c, String hint) => InputDecoration(
    hintText: hint,
    hintStyle: TextStyle(fontSize: Fs.body, color: c.faint),
    filled: true,
    fillColor: c.fill,
    isDense: true,
    border: OutlineInputBorder(
      borderRadius: BorderRadius.circular(Rd.input),
      borderSide: BorderSide.none,
    ),
    contentPadding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
  );

  Widget _urlField(PosseColors c, VoiceEngine engine) => TextField(
    key: const Key('voice-base-url-field'),
    controller: _urlCtrl,
    keyboardType: TextInputType.url,
    autocorrect: false,
    enableSuggestions: false,
    style: TextStyle(fontSize: Fs.body, color: c.text),
    decoration: _decoration(c, engine.defaultBaseUrl),
    onChanged: (v) =>
        widget.model.updateSettings((st) => st.voiceBaseUrl = v.trim()),
  );

  Widget _warning(PosseColors c, String text, Color color, Key key) => Padding(
    padding: const EdgeInsets.only(top: 6),
    child: Text(
      text,
      key: key,
      style: TextStyle(fontSize: Fs.small, height: Lh.body, color: color),
    ),
  );

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: widget.model,
      builder: (context, _) {
        final c = PosseColors.of(context);
        final l = context.l10n;
        final s = widget.model.settings;
        final engine = _engine;
        final custom = engine == VoiceEngine.custom;
        final effectiveUrl = s.voiceBaseUrl.trim().isNotEmpty
            ? s.voiceBaseUrl.trim()
            : engine.defaultBaseUrl;
        final urlInvalid =
            !engine.isNative &&
            effectiveUrl.isNotEmpty &&
            parseVoiceEndpointUri(
                  effectiveUrl,
                  allowInsecureHttp: s.voiceAllowInsecureHttp,
                ) ==
                null;
        final urlMissing = custom && effectiveUrl.isEmpty;
        // 地址无效时要用到下面的明文 HTTP 开关，强制展开
        final advancedOpen = _advanced || urlInvalid;
        final urlWarnings = [
          if (urlMissing)
            _warning(
              c,
              l.voiceCustomUrlRequired,
              c.honey,
              const Key('voice-url-required'),
            ),
          if (urlInvalid)
            _warning(
              c,
              l.voiceHttpsRequired,
              c.danger,
              const Key('voice-https-warning'),
            ),
        ];

        return Scaffold(
          appBar: AppBar(title: Text(l.voiceSettingsTitle)),
          body: SideSafe(
            child: SingleChildScrollView(
              padding: const EdgeInsets.only(top: 8, bottom: 32),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Section(
                    title: l.voiceEngineSection,
                    children: [
                      for (final item in VoiceEngine.values)
                        RowTile(
                          key: Key('voice-engine-${item.id}'),
                          chevron: false,
                          title: Text(voiceEngineLabel(l, item)),
                          subtitle: _engineSubtitle(c, l, item, engine),
                          trailing: item == engine
                              ? Icon(
                                  Icons.check_rounded,
                                  size: Ic.medium,
                                  color: c.accent,
                                )
                              : null,
                          onTap: () => _selectEngine(item),
                        ),
                      Padding(
                        padding: const EdgeInsets.fromLTRB(14, 10, 14, 12),
                        child: Text(
                          engine.isNative
                              ? l.voiceNativeNote
                              : l.voiceCloudNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ),
                    ],
                  ),
                  if (!engine.isNative) ...[
                    Section(
                      title: l.voiceCredentialsSection,
                      children: [
                        Padding(
                          padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.stretch,
                            children: [
                              _label(
                                c,
                                l.voiceApiKeyLabel,
                                trailing:
                                    !_hasKey && !_savedKeys.containsKey(engine)
                                    ? null
                                    : _hasKey
                                    ? _status(
                                        c,
                                        key: const Key('voice-key-state'),
                                        icon: Icons.check_circle_rounded,
                                        color: c.sage,
                                        text: l.voiceKeyStateSaved,
                                      )
                                    : _status(
                                        c,
                                        key: const Key('voice-key-state'),
                                        icon: Icons.error_outline_rounded,
                                        color: c.honey,
                                        text: l.voiceKeyStateMissing,
                                      ),
                              ),
                              const SizedBox(height: 6),
                              TextField(
                                key: const Key('voice-api-key-field'),
                                controller: _keyCtrl,
                                obscureText: true,
                                autocorrect: false,
                                enableSuggestions: false,
                                enableIMEPersonalizedLearning: false,
                                textInputAction: TextInputAction.done,
                                style: TextStyle(
                                  fontSize: Fs.body,
                                  color: c.text,
                                ),
                                decoration: _decoration(
                                  c,
                                  _hasKey
                                      ? l.voiceApiKeySavedHint
                                      : l.voiceApiKeyEmptyHint,
                                ),
                                onChanged: (_) =>
                                    setState(() => _keyNotice = null),
                                onSubmitted: (_) => unawaited(_saveKey()),
                              ),
                              const SizedBox(height: 8),
                              Row(
                                children: [
                                  FilledButton(
                                    key: const Key('voice-api-key-save'),
                                    onPressed:
                                        _busyKey || _keyCtrl.text.trim().isEmpty
                                        ? null
                                        : _saveKey,
                                    child: Text(l.voiceApiKeySave),
                                  ),
                                  if (_hasKey) ...[
                                    const SizedBox(width: 8),
                                    TextButton(
                                      key: const Key('voice-api-key-clear'),
                                      onPressed: _busyKey ? null : _clearKey,
                                      style: TextButton.styleFrom(
                                        foregroundColor: c.danger,
                                      ),
                                      child: Text(l.voiceApiKeyClear),
                                    ),
                                  ],
                                ],
                              ),
                              if (_keyNotice case final notice?) ...[
                                const SizedBox(height: 8),
                                _status(
                                  c,
                                  key: const Key('voice-key-notice'),
                                  icon: _keyNoticeOk
                                      ? Icons.check_circle_rounded
                                      : Icons.error_outline_rounded,
                                  color: _keyNoticeOk ? c.sage : c.danger,
                                  text: notice,
                                ),
                              ],
                              if (custom) ...[
                                const SizedBox(height: 14),
                                _label(c, l.voiceBaseUrlLabel),
                                const SizedBox(height: 6),
                                _urlField(c, engine),
                                ...urlWarnings,
                              ],
                            ],
                          ),
                        ),
                      ],
                    ),
                    Section(
                      folded: !advancedOpen,
                      children: [
                        RowTile(
                          key: const Key('voice-advanced-toggle'),
                          chevron: false,
                          title: Text(l.voiceAdvancedSection),
                          trailing: Icon(
                            advancedOpen
                                ? Icons.expand_less_rounded
                                : Icons.expand_more_rounded,
                            size: Ic.medium,
                            color: c.muted,
                          ),
                          onTap: () => setState(() => _advanced = !_advanced),
                        ),
                        Padding(
                          padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.stretch,
                            children: [
                              if (!custom) ...[
                                _label(c, l.voiceBaseUrlLabel),
                                const SizedBox(height: 6),
                                _urlField(c, engine),
                                ...urlWarnings,
                                const SizedBox(height: 14),
                              ],
                              _label(c, l.voiceModelLabel),
                              const SizedBox(height: 6),
                              TextField(
                                key: const Key('voice-model-field'),
                                controller: _modelCtrl,
                                autocorrect: false,
                                enableSuggestions: false,
                                style: TextStyle(
                                  fontSize: Fs.body,
                                  color: c.text,
                                ),
                                decoration: _decoration(c, engine.defaultModel),
                                onChanged: (v) => widget.model.updateSettings(
                                  (st) => st.voiceModel = v.trim(),
                                ),
                              ),
                            ],
                          ),
                        ),
                        RowTile(
                          chevron: false,
                          title: Text(l.voiceAllowInsecureHttp),
                          subtitle: Text(
                            l.voiceAllowInsecureHttpNote,
                            style: s.voiceAllowInsecureHttp
                                ? TextStyle(color: c.danger)
                                : null,
                          ),
                          trailing: Switch(
                            key: const Key('voice-allow-http-switch'),
                            value: s.voiceAllowInsecureHttp,
                            onChanged: (v) => widget.model.updateSettings(
                              (st) => st.voiceAllowInsecureHttp = v,
                            ),
                          ),
                          onTap: () => widget.model.updateSettings(
                            (st) => st.voiceAllowInsecureHttp =
                                !st.voiceAllowInsecureHttp,
                          ),
                        ),
                      ],
                    ),
                  ],
                  Section(
                    title: l.voiceLanguageSection,
                    children: [
                      Padding(
                        padding: const EdgeInsets.fromLTRB(12, 10, 12, 10),
                        child: Row(
                          children: [
                            for (final (val, label) in [
                              ('', l.voiceLanguageAuto),
                              ('zh', l.voiceLanguageZh),
                              ('en', l.voiceLanguageEn),
                            ])
                              Expanded(
                                child: Padding(
                                  padding: const EdgeInsets.symmetric(
                                    horizontal: 2,
                                  ),
                                  child: Material(
                                    color: s.voiceLanguage == val
                                        ? c.tint
                                        : c.fill,
                                    borderRadius: BorderRadius.circular(
                                      Rd.input,
                                    ),
                                    child: InkWell(
                                      borderRadius: BorderRadius.circular(
                                        Rd.input,
                                      ),
                                      onTap: () => widget.model.updateSettings(
                                        (st) => st.voiceLanguage = val,
                                      ),
                                      child: Padding(
                                        padding: const EdgeInsets.symmetric(
                                          vertical: 10,
                                        ),
                                        child: Text(
                                          label,
                                          textAlign: TextAlign.center,
                                          style: TextStyle(
                                            fontSize: Fs.body,
                                            fontWeight: s.voiceLanguage == val
                                                ? Fw.medium
                                                : Fw.body,
                                            color: s.voiceLanguage == val
                                                ? c.accent
                                                : c.text,
                                          ),
                                        ),
                                      ),
                                    ),
                                  ),
                                ),
                              ),
                          ],
                        ),
                      ),
                    ],
                  ),
                  Section(
                    title: l.voiceVocabSection,
                    children: [
                      Padding(
                        padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            TextField(
                              key: const Key('voice-vocab-field'),
                              controller: _vocabCtrl,
                              minLines: 2,
                              maxLines: 4,
                              maxLength: 300,
                              style: TextStyle(
                                fontSize: Fs.body,
                                color: c.text,
                                height: Lh.body,
                              ),
                              decoration: _decoration(c, l.voiceVocabHint),
                              onChanged: (v) => widget.model.updateSettings(
                                (st) => st.voiceVocab = v,
                              ),
                            ),
                            const SizedBox(height: 6),
                            Text(
                              l.voiceVocabNote,
                              style: TextStyle(
                                fontSize: Fs.small,
                                height: Lh.body,
                                color: c.muted,
                              ),
                            ),
                          ],
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ),
        );
      },
    );
  }

  /// 引擎列表的副标题：已存 Key 的云端引擎写「已配置 Key」；当前选中却缺 Key 的写琥珀色「需配置 API Key」。
  Widget? _engineSubtitle(
    PosseColors c,
    AppLocalizations l,
    VoiceEngine item,
    VoiceEngine selected,
  ) {
    if (item.isNative) return null;
    final saved = item == selected ? _hasKey : _savedKeys[item] == true;
    if (saved) return Text(l.voiceKeyStateSaved);
    if (item == selected && _savedKeys.containsKey(item)) {
      return Text(l.voiceKeyStateMissing, style: TextStyle(color: c.honey));
    }
    return null;
  }
}
