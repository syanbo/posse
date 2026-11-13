import 'package:flutter/material.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import 'unlock.dart';

/// 返回 true 时由桌面页显示手势提示，避免提示被设置面板挡住。
Future<bool?> showDeskSettings(
  BuildContext context,
  PosseAppModel model, {
  required Future<void> Function() forgetPasswords,
}) => showModalBottomSheet<bool>(
  context: context,
  showDragHandle: true,
  isScrollControlled: true,
  useSafeArea: true,
  builder: (_) => _DeskSettings(model: model, forgetPasswords: forgetPasswords),
);

class _DeskSettings extends StatefulWidget {
  const _DeskSettings({required this.model, required this.forgetPasswords});

  final PosseAppModel model;
  final Future<void> Function() forgetPasswords;

  @override
  State<_DeskSettings> createState() => _DeskSettingsState();
}

class _DeskSettingsState extends State<_DeskSettings> {
  final _passwords = const DeskPasswordStore();
  int _count = 0;
  bool _forgetting = false;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final count = await _passwords.count();
    if (mounted) setState(() => _count = count);
  }

  Future<void> _forget() async {
    if (_forgetting) return;
    setState(() {
      _forgetting = true;
      _error = null;
    });
    try {
      await widget.forgetPasswords();
      await _load();
    } catch (error) {
      if (mounted) setState(() => _error = error);
    } finally {
      if (mounted) setState(() => _forgetting = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: SafeArea(
        top: false,
        child: SingleChildScrollView(
          padding: const EdgeInsets.only(bottom: 12),
          child: ListenableBuilder(
            listenable: widget.model,
            builder: (context, _) {
              final trackpad = widget.model.settings.deskTrackpad;
              return Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Padding(
                    padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
                    child: Text(
                      l.settingsTitle,
                      style: TextStyle(fontSize: Fs.title, color: c.text),
                    ),
                  ),
                  Section(
                    children: [
                      Padding(
                        padding: const EdgeInsets.all(14),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            Segmented(
                              labels: [
                                l.settingsDeskTrackpad,
                                l.settingsDeskDirect,
                              ],
                              selected: trackpad ? 0 : 1,
                              onChanged: (index) {
                                final next = index == 0;
                                if (next == trackpad) return;
                                widget.model.updateSettings((s) {
                                  s.deskTrackpad = next;
                                  s.deskGesturesSeen = false;
                                });
                              },
                            ),
                            const SizedBox(height: 12),
                            Text(
                              trackpad
                                  ? l.settingsDeskTrackpadNote
                                  : l.settingsDeskDirectNote,
                              style: TextStyle(
                                fontSize: Fs.small,
                                height: Lh.body,
                                color: c.muted,
                              ),
                            ),
                          ],
                        ),
                      ),
                      RowTile(
                        title: Text(l.deskGestureTitle),
                        onTap: () => Navigator.of(context).pop(true),
                      ),
                    ],
                  ),
                  Section(
                    children: [
                      RowTile(
                        chevron: false,
                        title: Text(l.settingsDeskPasswords),
                        subtitle: Text(
                          _count > 0
                              ? l.settingsDeskPasswordsCount(_count)
                              : l.settingsDeskPasswordsNone,
                        ),
                        trailing: TextButton(
                          onPressed: _count > 0 && !_forgetting
                              ? _forget
                              : null,
                          child: Text(
                            l.settingsDeskPasswordsForget,
                            style: TextStyle(
                              color: _count > 0 && !_forgetting
                                  ? c.danger
                                  : null,
                            ),
                          ),
                        ),
                      ),
                      if (_error case final error?)
                        Padding(
                          padding: const EdgeInsets.all(14),
                          child: Text(
                            friendlyError(error, l),
                            style: TextStyle(
                              fontSize: Fs.small,
                              color: c.danger,
                            ),
                          ),
                        ),
                    ],
                  ),
                ],
              );
            },
          ),
        ),
      ),
    );
  }
}
