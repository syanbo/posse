import 'dart:async';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../background.dart';
import '../desk/plugin.dart';
import '../desk/unlock.dart';
import '../discussions/profile.dart';
import '../l10n/l10n.dart';
import '../push/push.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../update/update.dart';
import 'custom_keys_screen.dart';

/// 设置：外观、打开会话的方式、终端字号和快捷键、对话里显示什么，以及配对的电脑。改了立即生效并存在手机上。
class SettingsScreen extends StatelessWidget {
  const SettingsScreen({
    super.key,
    required this.model,
    required this.onComputers,
  });

  final PosseAppModel model;

  /// 打开首页那个电脑列表。
  final VoidCallback onComputers;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: Listenable.merge([model, updates]),
      builder: (context, _) {
        final c = PosseColors.of(context);
        final l = context.l10n;
        final s = model.settings;
        return Scaffold(
          appBar: AppBar(title: Text(l.settingsTitle)),
          body: SideSafe(
            child: ListView(
              padding: const EdgeInsets.only(top: 8, bottom: 32),
              children: [
                Section(
                  title: l.settingsAppearance,
                  children: [
                    _Choice(
                      labels: [
                        l.settingsThemeDark,
                        l.settingsThemeLight,
                        l.settingsThemeSystem,
                      ],
                      values: const ['dark', 'light', 'system'],
                      value: s.theme,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.theme = v),
                    ),
                  ],
                ),
                Section(
                  title: l.settingsLanguageTitle,
                  children: [
                    _Choice(
                      labels: [
                        l.settingsLanguageSystem,
                        ...languageNames.values,
                      ],
                      values: ['', ...languageNames.keys],
                      value: s.language,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.language = v),
                    ),
                  ],
                ),
                Section(
                  title: l.settingsHome,
                  children: [
                    _Toggle(
                      title: l.settingsProjectMeta,
                      value: !s.projectMetaHidden,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.projectMetaHidden = !v),
                    ),
                    _Toggle(
                      title: l.settingsWorkspaceMeta,
                      value: !s.workspaceMetaHidden,
                      onChanged: (v) => model.updateSettings(
                        (s) => s.workspaceMetaHidden = !v,
                      ),
                    ),
                  ],
                ),
                Section(
                  title: l.settingsOpenFirst,
                  children: [
                    _Choice(
                      labels: [l.settingsOpenChat, l.settingsOpenTerminal],
                      values: const ['chat', 'terminal'],
                      value: s.openMode,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.openMode = v),
                    ),
                    _Note(
                      s.openMode == 'chat'
                          ? l.settingsOpenChatNote
                          : l.settingsOpenTerminalNote,
                    ),
                  ],
                ),
                Section(
                  title: l.settingsTerminal,
                  children: [
                    // 标题、滑块和示例是一件事，放在同一块里，中间不画分隔线
                    Padding(
                      padding: const EdgeInsets.fromLTRB(14, 12, 14, 14),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.stretch,
                        children: [
                          Row(
                            children: [
                              Text(
                                l.settingsFontSize,
                                style: TextStyle(
                                  fontSize: Fs.body,
                                  color: c.text,
                                ),
                              ),
                              const Spacer(),
                              Text(
                                s.terminalFontSize.toStringAsFixed(1),
                                style: mono(Fs.small, color: c.muted),
                              ),
                            ],
                          ),
                          SliderTheme(
                            data: SliderTheme.of(context).copyWith(
                              overlayShape: SliderComponentShape.noOverlay,
                            ),
                            child: Padding(
                              padding: const EdgeInsets.symmetric(vertical: 12),
                              child: Slider(
                                value: s.terminalFontSize,
                                min: 10,
                                max: 16,
                                divisions: 12,
                                activeColor: c.accent,
                                inactiveColor: c.fill,
                                onChanged: (v) => model.updateSettings(
                                  (s) => s.terminalFontSize = v,
                                ),
                              ),
                            ),
                          ),
                          Container(
                            padding: const EdgeInsets.symmetric(
                              horizontal: 12,
                              vertical: 10,
                            ),
                            decoration: BoxDecoration(
                              color: c.code,
                              borderRadius: BorderRadius.circular(Rd.button),
                            ),
                            child: Text(
                              l.settingsFontSample,
                              style: mono(s.terminalFontSize, color: c.text),
                            ),
                          ),
                        ],
                      ),
                    ),
                    _Note(l.settingsFontNote),
                    RowTile(
                      title: Text(l.settingsKeys),
                      subtitle: Text(
                        s.customKeys.isEmpty
                            ? l.settingsKeysNote
                            : l.settingsKeysCustom(s.customKeys.length),
                      ),
                      onTap: () => Navigator.of(context).push(
                        MaterialPageRoute<void>(
                          builder: (_) => CustomKeysScreen(model: model),
                        ),
                      ),
                    ),
                  ],
                ),
                Section(
                  title: l.settingsChat,
                  children: [
                    _Toggle(
                      title: l.settingsShowReasoning,
                      value: s.showReasoning,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.showReasoning = v),
                    ),
                    _Toggle(
                      title: l.settingsExpandTools,
                      value: s.expandTools,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.expandTools = v),
                    ),
                  ],
                ),
                Section(
                  title: l.settingsComputer,
                  inset: 50,
                  children: [
                    RowTile(
                      leading: Icon(
                        Icons.laptop_mac_rounded,
                        size: Ic.button,
                        color: c.accent,
                      ),
                      title: Text(
                        model.current == null
                            ? l.settingsNotConnected
                            : model.labelOf(model.current, l),
                      ),
                      subtitle: Text(
                        l.settingsPairedCount(model.computers.length),
                      ),
                      onTap: onComputers,
                    ),
                  ],
                ),
                ProfileSection(model: model),
                if (model.push != null)
                  Section(
                    title: l.settingsPushTitle,
                    children: [
                      _Toggle(
                        title: l.settingsPushTitle,
                        value: s.pushEnabled,
                        onChanged: (v) {
                          model.updateSettings((s) => s.pushEnabled = v);
                          unawaited(model.registerPush());
                        },
                      ),
                      _Toggle(
                        title: l.settingsPushBadge,
                        value: s.badgeEnabled,
                        onChanged: (v) {
                          model.updateSettings((s) => s.badgeEnabled = v);
                          unawaited(model.registerPush());
                        },
                      ),
                      if (s.pushEnabled) ...[
                        _Toggle(
                          title: l.settingsPushWaiting,
                          value: s.pushWaiting,
                          onChanged: (v) {
                            model.updateSettings((s) => s.pushWaiting = v);
                            unawaited(model.registerPush());
                          },
                        ),
                        _Toggle(
                          title: l.settingsPushDone,
                          value: s.pushDone,
                          onChanged: (v) {
                            model.updateSettings((s) => s.pushDone = v);
                            unawaited(model.registerPush());
                          },
                        ),
                      ],
                      _Note(switch (model.pushStatus) {
                        PushStatus.unsupportedRelay => l.settingsPushOldRelay,
                        PushStatus.on => l.settingsPushOn,
                        PushStatus.noRelay => l.settingsPushNoRelay,
                        PushStatus.denied => l.settingsPushDenied,
                        PushStatus.off => l.settingsPushOff,
                        PushStatus.unsupportedComputer =>
                          l.settingsPushOldComputer,
                        PushStatus.failed => l.settingsPushFailed,
                        PushStatus.unknown ||
                        PushStatus.unsupported => l.settingsPushUnknown,
                      }),
                      if (model.pushStatus == PushStatus.denied)
                        RowTile(
                          title: Text(l.settingsPushSystemSettings),
                          onTap: () => unawaited(model.push!.openSettings()),
                        ),
                    ],
                  ),
                if (backgroundKeepSupported)
                  Section(
                    title: l.settingsBackgroundTitle,
                    children: [
                      _Toggle(
                        title: l.settingsBackgroundKeep,
                        value: s.backgroundKeep,
                        onChanged: (v) =>
                            model.updateSettings((s) => s.backgroundKeep = v),
                      ),
                      _Note(
                        s.backgroundKeep
                            ? l.settingsBackgroundKeepOn
                            : l.settingsBackgroundKeepOff,
                      ),
                    ],
                  ),
                if (deskSupported)
                  Section(
                    title: l.homeRemoteDesktop,
                    children: [
                      _Toggle(
                        title: l.homeRemoteDesktop,
                        value: s.deskWatch,
                        onChanged: (v) =>
                            model.updateSettings((s) => s.deskWatch = v),
                      ),
                      _Note(l.settingsDeskNote),
                      if (s.deskWatch) ...[
                        _Choice(
                          labels: [
                            l.settingsDeskTrackpad,
                            l.settingsDeskDirect,
                          ],
                          values: const ['trackpad', 'direct'],
                          value: s.deskTrackpad ? 'trackpad' : 'direct',
                          // 换了操作方式，下次进看桌面再提示一次手势
                          onChanged: (v) => model.updateSettings((s) {
                            s.deskTrackpad = v == 'trackpad';
                            s.deskGesturesSeen = false;
                          }),
                        ),
                        _Note(
                          s.deskTrackpad
                              ? l.settingsDeskTrackpadNote
                              : l.settingsDeskDirectNote,
                        ),
                        const _DeskPasswords(),
                      ],
                    ],
                  ),
                Section(
                  title: l.settingsAbout,
                  children: [
                    RowTile(
                      chevron: false,
                      title: const Text('Posse'),
                      trailing: Text(
                        appBuild > 0
                            ? l.settingsVersionBuild(appVersion, '$appBuild')
                            : appVersion,
                        style: mono(Fs.small, color: c.muted),
                      ),
                    ),
                    // 首页的更新提示关掉了也能在这里更新
                    if (updates.latest case final release?)
                      RowTile(
                        leading: Icon(
                          Icons.system_update_rounded,
                          size: Ic.button,
                          color: c.accent,
                        ),
                        title: Text(
                          l.settingsUpdateTo(
                            release.version,
                            '${release.build}',
                          ),
                        ),
                        trailing: release.size.isEmpty
                            ? null
                            : Text(
                                release.size,
                                style: TextStyle(
                                  fontSize: Fs.small,
                                  color: c.faint,
                                ),
                              ),
                        onTap: () => showUpdateSheet(context, release),
                      )
                    else if (updates.enabled)
                      _CheckUpdate(checker: updates),
                  ],
                ),
              ],
            ),
          ),
        );
      },
    );
  }
}

/// 「关于」里手动检查更新：查到新版本时这一行换成「更新到 …」，没有就在行尾写「已是最新」。
class _CheckUpdate extends StatefulWidget {
  const _CheckUpdate({required this.checker});

  final UpdateChecker checker;

  @override
  State<_CheckUpdate> createState() => _CheckUpdateState();
}

class _CheckUpdateState extends State<_CheckUpdate> {
  /// 这一页上手动查过的结果；离开设置页再进来就不显示了。
  bool? _ok;

  Future<void> _check() async {
    setState(() => _ok = null);
    final ok = await widget.checker.check(force: true);
    if (mounted) setState(() => _ok = ok);
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final checking = widget.checker.checking;
    return RowTile(
      chevron: false,
      leading: Icon(Icons.refresh_rounded, size: Ic.button, color: c.muted),
      title: Text(context.l10n.settingsCheckUpdate),
      trailing: checking
          ? HexSpinner(size: Ic.small, color: c.muted)
          : switch (_ok) {
              true => Text(
                context.l10n.settingsUpToDate,
                style: TextStyle(fontSize: Fs.small, color: c.faint),
              ),
              false => Text(
                context.l10n.settingsCheckFailed,
                style: TextStyle(fontSize: Fs.small, color: c.danger),
              ),
              null => null,
            },
      onTap: checking ? null : _check,
    );
  }
}

/// 看桌面在锁屏时记住的 Mac 登录密码：记着几台，全部忘掉。
class _DeskPasswords extends StatefulWidget {
  const _DeskPasswords();

  @override
  State<_DeskPasswords> createState() => _DeskPasswordsState();
}

class _DeskPasswordsState extends State<_DeskPasswords> {
  final _store = const DeskPasswordStore();
  int? _count;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final n = await _store.count();
    if (mounted) setState(() => _count = n);
  }

  Future<void> _forget() async {
    await _store.forgetAll();
    await _load();
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final n = _count ?? 0;
    final l = context.l10n;
    return RowTile(
      key: const Key('settings-desk-passwords'),
      chevron: false,
      title: Text(l.settingsDeskPasswords),
      subtitle: Text(
        n > 0 ? l.settingsDeskPasswordsCount(n) : l.settingsDeskPasswordsNone,
      ),
      trailing: TextButton(
        key: const Key('settings-desk-passwords-forget'),
        onPressed: n > 0 ? _forget : null,
        child: Text(
          l.settingsDeskPasswordsForget,
          style: TextStyle(color: n > 0 ? c.danger : null),
        ),
      ),
    );
  }
}

/// 卡片里的分段选择。
class _Choice extends StatelessWidget {
  const _Choice({
    required this.labels,
    required this.values,
    required this.value,
    required this.onChanged,
  });

  final List<String> labels;
  final List<String> values;
  final String value;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(10),
      child: Segmented(
        labels: labels,
        selected: values.indexOf(value).clamp(0, values.length - 1),
        onChanged: (i) => onChanged(values[i]),
      ),
    );
  }
}

class _Toggle extends StatelessWidget {
  const _Toggle({
    required this.title,
    required this.value,
    required this.onChanged,
  });

  final String title;
  final bool value;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return InkWell(
      onTap: () => onChanged(!value),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 6, 8, 6),
        child: Row(
          children: [
            Expanded(
              child: Text(
                title,
                style: TextStyle(fontSize: Fs.body, color: c.text),
              ),
            ),
            PosseSwitch(value: value, onChanged: onChanged),
          ],
        ),
      ),
    );
  }
}

class _Note extends StatelessWidget {
  const _Note(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(14, 10, 14, 12),
      child: Text(
        text,
        style: TextStyle(fontSize: Fs.small, height: Lh.body, color: c.muted),
      ),
    );
  }
}
