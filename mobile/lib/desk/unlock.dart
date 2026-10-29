import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../ui/theme.dart';

/// 弹层的结果：[password] 为空是点了「忘掉密码」。
typedef MacPasswordAnswer = ({String? password, bool remember});

/// 问 Mac 的登录密码，用来在锁屏界面上解锁。[canRemember] 为假时不给「记住密码」，
/// [saved] 是这台电脑已经记着密码。取消为 null。
Future<MacPasswordAnswer?> askMacPassword(
  BuildContext context, {
  bool canRemember = true,
  bool saved = false,
}) {
  return showModalBottomSheet<MacPasswordAnswer>(
    context: context,
    showDragHandle: true,
    isScrollControlled: true,
    builder: (sheet) => _PasswordSheet(canRemember: canRemember, saved: saved),
  );
}

class _PasswordSheet extends StatefulWidget {
  const _PasswordSheet({required this.canRemember, required this.saved});

  final bool canRemember;
  final bool saved;

  @override
  State<_PasswordSheet> createState() => _PasswordSheetState();
}

class _PasswordSheetState extends State<_PasswordSheet> {
  final _password = TextEditingController();
  late var _remember = widget.saved;

  @override
  void initState() {
    super.initState();
    _password.addListener(() => setState(() {}));
  }

  @override
  void dispose() {
    _password.dispose();
    super.dispose();
  }

  void _submit() {
    if (_password.text.isEmpty) return;
    Navigator.of(context).pop((
      password: _password.text,
      remember: widget.canRemember && _remember,
    ));
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    // 横屏时键盘占掉大半屏：「记住」和「解锁」跟输入框放一行，看得见输入框就点得到
    return Padding(
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: SafeArea(
        top: false,
        child: SingleChildScrollView(
          padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Padding(
                      padding: const EdgeInsets.symmetric(
                        horizontal: 4,
                        vertical: 8,
                      ),
                      child: Text(
                        widget.canRemember
                            ? '输入 Mac 的登录密码，发到锁屏界面后自动回车。勾「记住」只存在这台手机上'
                            : '输入 Mac 的登录密码，发到锁屏界面后自动回车',
                        style: TextStyle(fontSize: Fs.meta, color: c.muted),
                      ),
                    ),
                  ),
                  if (widget.saved)
                    TextButton(
                      key: const Key('desk-password-forget'),
                      onPressed: () =>
                          Navigator.of(context)
                              .pop((password: null, remember: false)),
                      child: Text('忘掉密码', style: TextStyle(color: c.danger)),
                    ),
                ],
              ),
              const SizedBox(height: 8),
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      key: const Key('desk-password'),
                      controller: _password,
                      autofocus: true,
                      obscureText: true,
                      autocorrect: false,
                      enableSuggestions: false,
                      keyboardType: TextInputType.visiblePassword,
                      textInputAction: TextInputAction.done,
                      onSubmitted: (_) => _submit(),
                      decoration: InputDecoration(
                        hintText: '登录密码',
                        fillColor: c.card,
                      ),
                    ),
                  ),
                  if (widget.canRemember) ...[
                    const SizedBox(width: 8),
                    FilterChip(
                      key: const Key('desk-password-remember'),
                      label: const Text('记住'),
                      selected: _remember,
                      onSelected: (on) => setState(() => _remember = on),
                    ),
                  ],
                  const SizedBox(width: 8),
                  FilledButton(
                    key: const Key('desk-unlock-send'),
                    onPressed: _password.text.isEmpty ? null : _submit,
                    child: const Text('解锁'),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// 记住的 Mac 登录密码，iOS 存在钥匙串里（DeskPassword.swift），安卓用 Keystore 的密钥加密后存在不进备份的目录（DeskPlugin.kt），按电脑的主机公钥分开。
class DeskPasswordStore {
  const DeskPasswordStore([
    this._channel = const MethodChannel('posse/desk/password'),
  ]);

  final MethodChannel _channel;

  /// 有没有记着。
  Future<bool> has(String key) async {
    try {
      return await _channel.invokeMethod<bool>('has', {'key': key}) ?? false;
    } on Object {
      return false;
    }
  }

  /// 没有时为 null。
  Future<String?> read(String key) async {
    try {
      return await _channel.invokeMethod<String>('read', {'key': key});
    } on Object {
      return null;
    }
  }

  Future<bool> save(String key, String password) async {
    try {
      return await _channel.invokeMethod<bool>('save', {
            'key': key,
            'password': password,
          }) ??
          false;
    } on Object {
      return false;
    }
  }

  /// 记着几台电脑的密码。
  Future<int> count() async {
    try {
      return await _channel.invokeMethod<int>('count') ?? 0;
    } on Object {
      return 0;
    }
  }

  Future<void> forgetAll() async {
    try {
      await _channel.invokeMethod<void>('forgetAll');
    } on Object {
      // 没有原生层就是没记着。
    }
  }

  Future<void> forget(String key) async {
    try {
      await _channel.invokeMethod<void>('forget', {'key': key});
    } on Object {
      // 没有原生层（测试、安卓）就是没记着。
    }
  }
}
