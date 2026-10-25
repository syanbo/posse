import 'dart:io';

import 'package:flutter/material.dart';

import '../app_state.dart';
import '../desk/unlock.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'custom_keys_screen.dart';

const appVersion = '0.1.0';

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
      listenable: model,
      builder: (context, _) {
        final c = PosseColors.of(context);
        final s = model.settings;
        return Scaffold(
          appBar: AppBar(title: const Text('设置')),
          body: ListView(
            padding: const EdgeInsets.only(top: 8, bottom: 32),
            children: [
              Section(
                title: '外观',
                children: [
                  _Choice(
                    labels: const ['深色', '浅色', '跟随系统'],
                    values: const ['dark', 'light', 'system'],
                    value: s.theme,
                    onChanged: (v) => model.updateSettings((s) => s.theme = v),
                  ),
                ],
              ),
              Section(
                title: '点开会话时先看',
                children: [
                  _Choice(
                    labels: const ['对话', '终端'],
                    values: const ['chat', 'terminal'],
                    value: s.openMode,
                    onChanged: (v) =>
                        model.updateSettings((s) => s.openMode = v),
                  ),
                  _Note(
                    s.openMode == 'chat'
                        ? '对话页右上角可以切到终端。没有会话存档的 agent 和 shell 总是开终端。'
                        : '终端页右上角可以切到对话。对话和终端是同一个会话的两种看法，切换时互相替换。',
                  ),
                ],
              ),
              Section(
                title: '终端',
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
                              '字号',
                              style: TextStyle(
                                fontSize: Fs.body,
                                color: c.text,
                              ),
                            ),
                            const Spacer(),
                            Text(
                              s.terminalFontSize.toStringAsFixed(1),
                              style: mono(14, color: c.muted),
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
                            'agent 正在运行 Claude Code',
                            style: mono(s.terminalFontSize, color: c.text),
                          ),
                        ),
                      ],
                    ),
                  ),
                  const _Note('手机看终端时终端按手机屏幕排版，字越小一行放得越多。'),
                  RowTile(
                    title: const Text('快捷键'),
                    subtitle: Text(
                      s.customKeys.isEmpty
                          ? '调整键盘上方快捷键栏的顺序，加自己的键'
                          : '自定义 ${s.customKeys.length} 个',
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
                title: '对话',
                children: [
                  _Toggle(
                    title: '显示思考过程',
                    value: s.showReasoning,
                    onChanged: (v) =>
                        model.updateSettings((s) => s.showReasoning = v),
                  ),
                  _Toggle(
                    title: '工具调用默认展开',
                    value: s.expandTools,
                    onChanged: (v) =>
                        model.updateSettings((s) => s.expandTools = v),
                  ),
                ],
              ),
              Section(
                title: '电脑',
                inset: 50,
                children: [
                  RowTile(
                    leading: Icon(
                      Icons.laptop_mac_rounded,
                      size: 24,
                      color: c.accent,
                    ),
                    title: Text(
                      model.current == null
                          ? '没有连接'
                          : model.labelOf(model.current),
                    ),
                    subtitle: Text('共配对 ${model.computers.length} 台'),
                    onTap: onComputers,
                  ),
                ],
              ),
              if (Platform.isIOS)
                Section(
                  title: '实验',
                  children: [
                    _Toggle(
                      title: '实验：看桌面',
                      value: s.deskWatch,
                      onChanged: (v) =>
                          model.updateSettings((s) => s.deskWatch = v),
                    ),
                    const _Note('打开后，点首页的电脑名，菜单里有「看桌面」。画面走局域网，输入先显示在画面上。'),
                    if (s.deskWatch) const _DeskPasswords(),
                  ],
                ),
              Section(
                title: '关于',
                children: [
                  RowTile(
                    chevron: false,
                    title: const Text('Posse'),
                    trailing: Text(appVersion, style: mono(13, color: c.muted)),
                  ),
                ],
              ),
            ],
          ),
        );
      },
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
    return RowTile(
      key: const Key('settings-desk-passwords'),
      chevron: false,
      title: const Text('记住的 Mac 密码'),
      subtitle: Text(n > 0 ? '记着 $n 台电脑的登录密码' : '没有记住的密码'),
      trailing: TextButton(
        key: const Key('settings-desk-passwords-forget'),
        onPressed: n > 0 ? _forget : null,
        child: Text('全部忘掉', style: TextStyle(color: n > 0 ? c.danger : null)),
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
        style: TextStyle(fontSize: 12.5, height: 1.5, color: c.muted),
      ),
    );
  }
}
