import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:posse_quic/posse_quic.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../chat/latency_label.dart';
import '../l10n/l10n.dart';
import '../protocol/quic_socket.dart';
import '../protocol/tailscale_service.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

/// 外部浏览器打开回调，测试可注入桩替代 `url_launcher`。
typedef TailscaleUrlLauncher = Future<bool> Function(Uri uri);

final _controlOrSpace = RegExp(r'[\x00-\x20\x7f]');
final _badPercentEscape = RegExp(r'%(?![0-9a-fA-F]{2})');
final _strictPortDigits = RegExp(r'^[1-9][0-9]{0,4}$');
final _forbiddenHostChars = RegExp(r'''[\x00-\x20\x7f%:/\\?#@\[\]<>"'`{}|^]''');

/// 校验 `[...]` 内的 IPv6 字面量（支持 RFC 6874 `%25<zone>` 作用域后缀）。
bool _isValidBracketedIpv6(String inner) {
  if (inner.isEmpty) return false;
  final pct = inner.indexOf('%');
  final String addrPart;
  if (pct >= 0) {
    if (!inner.substring(pct).startsWith('%25')) return false;
    final zone = inner.substring(pct + 3);
    if (zone.isEmpty || _forbiddenHostChars.hasMatch(zone)) return false;
    addrPart = inner.substring(0, pct);
  } else {
    addrPart = inner;
  }
  try {
    Uri.parseIPv6Address(addrPart);
    return true;
  } on FormatException {
    return false;
  }
}

/// 解析并校验 authority 段：禁止空白/控制字符、非法转义、任何 `@`（含空 userinfo）、
/// 空端口、符号/前导零/越界端口，以及非 IPv6 主机名里的 `%` 或非法主机字符。
bool _hasValidAuthority(String raw, {required bool strictHost}) {
  if (_controlOrSpace.hasMatch(raw) || _badPercentEscape.hasMatch(raw)) {
    return false;
  }
  final schemeEnd = raw.indexOf('://');
  if (schemeEnd < 0) return false;
  final afterScheme = raw.substring(schemeEnd + 3);
  final endIdx = afterScheme.indexOf(RegExp(r'[/?#]'));
  final authority = endIdx < 0 ? afterScheme : afterScheme.substring(0, endIdx);
  if (authority.isEmpty || authority.contains('@')) return false;
  if (authority.endsWith(':')) return false;

  String hostPart;
  String portPart = '';
  if (authority.startsWith('[')) {
    final closeBracket = authority.indexOf(']');
    if (closeBracket <= 1) return false;
    hostPart = authority.substring(1, closeBracket);
    if (!_isValidBracketedIpv6(hostPart)) return false;
    final rest = authority.substring(closeBracket + 1);
    if (rest.isNotEmpty) {
      if (!rest.startsWith(':')) return false;
      portPart = rest.substring(1);
    }
  } else {
    final colon = authority.indexOf(':');
    if (colon >= 0) {
      if (authority.indexOf(':', colon + 1) >= 0) return false;
      hostPart = authority.substring(0, colon);
      portPart = authority.substring(colon + 1);
    } else {
      hostPart = authority;
    }
    if (hostPart.isEmpty) return false;
    if (strictHost && _forbiddenHostChars.hasMatch(hostPart)) return false;
  }

  if (portPart.isNotEmpty) {
    if (!_strictPortDigits.hasMatch(portPart)) return false;
    final port = int.tryParse(portPart);
    if (port == null || port < 1 || port > 65535) return false;
  }
  return true;
}

/// 校验登录链接（允许 query/fragment，因为 Tailscale 登录 URL 含路径或参数）：
/// 仅允许 `http` / `https`、非空主机名、合法端口且不含 userinfo（含空 `@`）。
Uri? parseSafeTailscaleAuthUrl(String raw) {
  final trimmed = raw.trim();
  if (trimmed.isEmpty || !_hasValidAuthority(trimmed, strictHost: false)) {
    return null;
  }
  final uri = Uri.tryParse(trimmed);
  if (uri == null) return null;
  final scheme = uri.scheme.toLowerCase();
  if (scheme != 'http' && scheme != 'https') return null;
  if (uri.host.isEmpty || uri.userInfo.isNotEmpty) return null;
  return uri;
}

/// 校验自定义控制服务器地址（Headscale），与原生 `internal/tslink/config.go:ValidateControlURL` 保持一致：
/// 仅允许 `http` / `https`、严格主机名（禁空格/百分号编码）、纯数字合法端口（1..65535，禁 `+`/前导零/空端口）、
/// 合法路径转义，且无 userinfo/query/fragment。
Uri? parseSafeTailscaleControlUrl(String raw) {
  final trimmed = raw.trim();
  if (trimmed.isEmpty) return null;
  if (trimmed.contains('?') || trimmed.contains('#')) return null;
  if (!_hasValidAuthority(trimmed, strictHost: true)) return null;
  final uri = Uri.tryParse(trimmed);
  if (uri == null) return null;
  final scheme = uri.scheme.toLowerCase();
  if (scheme != 'http' && scheme != 'https') return null;
  if (uri.host.isEmpty || uri.userInfo.isNotEmpty) return null;
  if (uri.hasQuery || uri.hasFragment) return null;
  return uri;
}

/// 设置页入口行里的简短状态文案。
String tailscaleSummaryText(
  AppLocalizations l,
  TailscaleController controller,
  AppSettings settings,
) {
  final st = controller.status;
  if (st.isRunning) return st.ip;
  return switch (st.state) {
    'needs_login' => l.tailscaleNeedsLogin,
    'needs_machine_auth' => l.tailscaleNeedsApproval,
    'starting' => l.tailscaleStarting,
    'error' => l.tailscaleError,
    'stopped' => l.tailscaleStopped,
    _ =>
      settings.tailscaleEnabled && controller.busy
          ? l.tailscaleStarting
          : l.tailscaleOff,
  };
}

/// 状态对应的强调色。
Color tailscaleStatusColor(PosseColors c, TailscaleStatus status) {
  if (status.isRunning) return c.sage;
  return switch (status.state) {
    'needs_login' || 'needs_machine_auth' || 'starting' => c.honey,
    'error' => c.danger,
    _ => c.faint,
  };
}

/// 手机端「内置 Tailscale」设置与交互登录页。
class TailscaleSettingsScreen extends StatefulWidget {
  const TailscaleSettingsScreen({
    super.key,
    required this.model,
    this.launchExternalUrl,
  });

  final PosseAppModel model;
  final TailscaleUrlLauncher? launchExternalUrl;

  @override
  State<TailscaleSettingsScreen> createState() =>
      _TailscaleSettingsScreenState();
}

class _TailscaleSettingsScreenState extends State<TailscaleSettingsScreen> {
  late final TextEditingController _hostnameCtrl;
  late final TextEditingController _controlUrlCtrl;
  late final TextEditingController _authKeyCtrl;
  late bool _disableBind;

  late String _syncedHostname;
  late String _syncedControlUrl;
  late bool _syncedDisableBind;

  bool _advancedExpanded = false;
  bool _draftDirty = false;
  bool _obscureAuthKey = true;
  bool _actionBusy = false;
  bool? _pendingEnabled;
  int _uiActionSeq = 0;

  String? _topError;
  String? _advancedError;
  String? _controlUrlError;
  String? _warning;

  @override
  void initState() {
    super.initState();
    final s = widget.model.settings;
    _syncedHostname = s.tailscaleHostname;
    _syncedControlUrl = s.tailscaleControlUrl;
    _syncedDisableBind = s.tailscaleDisableBind;
    _hostnameCtrl = TextEditingController(text: _syncedHostname)
      ..addListener(_onFieldEdited);
    _controlUrlCtrl = TextEditingController(text: _syncedControlUrl)
      ..addListener(_onFieldEdited);
    _authKeyCtrl = TextEditingController();
    _disableBind = _syncedDisableBind;
    widget.model.addListener(_onModelChanged);
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModelChanged);
    _hostnameCtrl
      ..removeListener(_onFieldEdited)
      ..dispose();
    _controlUrlCtrl
      ..removeListener(_onFieldEdited)
      ..dispose();
    _authKeyCtrl.dispose();
    super.dispose();
  }

  void _onFieldEdited() {
    if (_hostnameCtrl.text != _syncedHostname ||
        _controlUrlCtrl.text != _syncedControlUrl) {
      _draftDirty = true;
    }
    if (_controlUrlError != null || _advancedError != null) {
      setState(() {
        _controlUrlError = null;
        _advancedError = null;
      });
    }
  }

  void _onModelChanged() {
    if (!mounted) return;
    final s = widget.model.settings;
    if (!_draftDirty) {
      if (s.tailscaleHostname != _syncedHostname) {
        _syncedHostname = s.tailscaleHostname;
        _hostnameCtrl.text = _syncedHostname;
      }
      if (s.tailscaleControlUrl != _syncedControlUrl) {
        _syncedControlUrl = s.tailscaleControlUrl;
        _controlUrlCtrl.text = _syncedControlUrl;
      }
      if (s.tailscaleDisableBind != _syncedDisableBind) {
        _syncedDisableBind = s.tailscaleDisableBind;
        _disableBind = _syncedDisableBind;
      }
    }
    setState(() {});
  }

  bool get _busy => _actionBusy || widget.model.tailscale.busy;

  void _showNotice(String text) {
    ScaffoldMessenger.maybeOf(context)
      ?..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(text)));
  }

  String _tsErrorText(Object error, AppLocalizations l) {
    if (error is PlatformException) {
      final msg = error.message?.trim() ?? '';
      return msg.isNotEmpty
          ? localizeTailscaleError(msg, l)
          : l.tailscaleErrorGeneric;
    }
    return friendlyError(error, l);
  }

  Future<void> _restorePreviousNode({
    required String hostname,
    required String controlUrl,
    required bool disableBind,
  }) async {
    try {
      await widget.model.tailscale.start(
        hostname: hostname,
        controlUrl: controlUrl,
        disableBindInterface: disableBind,
      );
    } on Object {
      // 恢复也失败时保留原错误提示，不吞掉主路径报错。
    }
  }

  Future<void> _onToggle(bool enable) async {
    if (_busy) return;
    final l = context.l10n;
    final seq = ++_uiActionSeq;
    setState(() {
      _actionBusy = true;
      _pendingEnabled = enable;
      _topError = null;
      _advancedError = null;
      _warning = null;
    });
    try {
      if (enable) {
        final s = widget.model.settings;
        final applied = await widget.model.tailscale.start(
          hostname: s.tailscaleHostname,
          controlUrl: s.tailscaleControlUrl,
          disableBindInterface: s.tailscaleDisableBind,
        );
        if (!applied) return;
        final st = widget.model.tailscale.status;
        if (st.state == 'error') {
          // 原生启动出错时会发出 Enabled=true, State="error" 的快照：不要持久化为开启态。
          if (mounted && seq == _uiActionSeq) {
            setState(() => _topError = localizeTailscaleError(st.error, l));
          }
          return;
        }
        widget.model.updateSettings((cfg) => cfg.tailscaleEnabled = true);
      } else {
        widget.model.updateSettings((cfg) => cfg.tailscaleEnabled = false);
        await widget.model.tailscale.stop();
      }
    } on Object catch (e) {
      if (mounted && seq == _uiActionSeq) {
        setState(() => _topError = _tsErrorText(e, l));
      }
    } finally {
      if (mounted && seq == _uiActionSeq) {
        setState(() {
          _actionBusy = false;
          _pendingEnabled = null;
        });
      }
    }
  }

  Future<void> _openLogin(String rawUrl) async {
    final l = context.l10n;
    final seq = ++_uiActionSeq;
    final uri = parseSafeTailscaleAuthUrl(rawUrl);
    if (uri == null) {
      setState(() => _topError = l.tailscaleInvalidAuthUrl);
      return;
    }
    setState(() => _topError = null);
    final launcher =
        widget.launchExternalUrl ??
        (Uri target) => launchUrl(target, mode: LaunchMode.externalApplication);
    try {
      final ok = await launcher(uri);
      if (!ok && mounted && seq == _uiActionSeq) {
        setState(() => _topError = l.tailscaleOpenLoginFailed);
      }
    } on Object {
      if (mounted && seq == _uiActionSeq) {
        setState(() => _topError = l.tailscaleOpenLoginFailed);
      }
    }
  }

  Future<void> _copyLoginUrl(String rawUrl) async {
    if (_busy) return;
    final l = context.l10n;
    final uri = parseSafeTailscaleAuthUrl(rawUrl);
    if (uri == null) {
      setState(() => _topError = l.tailscaleInvalidAuthUrl);
      return;
    }
    setState(() => _topError = null);
    await Clipboard.setData(ClipboardData(text: uri.toString()));
    if (mounted) _showNotice(l.commonCopied);
  }

  Future<void> _copyText(String text) async {
    final l = context.l10n;
    await Clipboard.setData(ClipboardData(text: text));
    if (mounted) _showNotice(l.commonCopied);
  }

  Future<void> _refreshLogin() async {
    if (_busy) return;
    final l = context.l10n;
    final seq = ++_uiActionSeq;
    setState(() {
      _actionBusy = true;
      _topError = null;
      _warning = null;
    });
    try {
      await widget.model.tailscale.startLogin();
    } on Object catch (e) {
      if (mounted && seq == _uiActionSeq) {
        setState(() => _topError = _tsErrorText(e, l));
      }
    } finally {
      if (mounted && seq == _uiActionSeq) {
        setState(() => _actionBusy = false);
      }
    }
  }

  Future<void> _saveAdvanced() async {
    if (_busy) return;
    final l = context.l10n;
    final hostname = _hostnameCtrl.text.trim();
    final controlUrl = _controlUrlCtrl.text.trim();
    final authKey = _authKeyCtrl.text.trim();
    final disableBind = _disableBind;

    if (controlUrl.isNotEmpty &&
        parseSafeTailscaleControlUrl(controlUrl) == null) {
      setState(() {
        _controlUrlError = l.tailscaleInvalidControlUrl;
        _advancedError = l.tailscaleInvalidControlUrl;
      });
      return;
    }

    final prevSettings = widget.model.settings;
    final wasEnabled = prevSettings.tailscaleEnabled;
    final prevHostname = prevSettings.tailscaleHostname;
    final prevControlUrl = prevSettings.tailscaleControlUrl;
    final prevDisableBind = prevSettings.tailscaleDisableBind;
    final shouldEnable = wasEnabled || authKey.isNotEmpty;
    final seq = ++_uiActionSeq;
    setState(() {
      _actionBusy = true;
      _pendingEnabled = shouldEnable ? true : null;
      _controlUrlError = null;
      _topError = null;
      _advancedError = null;
      _warning = null;
    });

    try {
      if (shouldEnable) {
        final applied = await widget.model.tailscale.start(
          hostname: hostname,
          controlUrl: controlUrl,
          authKey: authKey,
          disableBindInterface: disableBind,
        );
        if (!applied) return;
        final st = widget.model.tailscale.status;
        if (st.state == 'error') {
          final msg = localizeTailscaleError(st.error, l);
          if (wasEnabled) {
            // 原生 Start 失败前已停掉旧实例：恢复先前可用配置，避免节点停留在离线态。
            await _restorePreviousNode(
              hostname: prevHostname,
              controlUrl: prevControlUrl,
              disableBind: prevDisableBind,
            );
          }
          if (mounted && seq == _uiActionSeq) {
            setState(() {
              _topError = msg;
              _advancedError = msg;
            });
          }
          return;
        }
      }

      // 仅在校验及启动（若需要）通过后才持久化；一次性认证密钥绝不写入持久化设置。
      _syncedHostname = hostname;
      _syncedControlUrl = controlUrl;
      _syncedDisableBind = disableBind;
      _draftDirty = false;
      if (mounted) {
        _hostnameCtrl.text = hostname;
        _controlUrlCtrl.text = controlUrl;
      }
      widget.model.updateSettings((s) {
        s.tailscaleHostname = hostname;
        s.tailscaleControlUrl = controlUrl;
        s.tailscaleDisableBind = disableBind;
        if (authKey.isNotEmpty) s.tailscaleEnabled = true;
      });
      if (mounted) _showNotice(l.tailscaleSaved);
    } on Object catch (e) {
      final msg = _tsErrorText(e, l);
      if (wasEnabled) {
        await _restorePreviousNode(
          hostname: prevHostname,
          controlUrl: prevControlUrl,
          disableBind: prevDisableBind,
        );
      }
      if (mounted && seq == _uiActionSeq) {
        setState(() {
          _topError = msg;
          _advancedError = msg;
        });
      }
    } finally {
      // 无论成功或失败都清空内存中的一次性 AuthKey，避免残留在输入框中被后续保存误发。
      if (mounted && authKey.isNotEmpty) {
        _authKeyCtrl.clear();
      }
      if (mounted && seq == _uiActionSeq) {
        setState(() {
          _actionBusy = false;
          _pendingEnabled = null;
        });
      }
    }
  }

  Future<void> _resetIdentity() async {
    if (_busy) return;
    final l = context.l10n;
    final confirmed = await showPosseConfirm(
      context,
      title: l.tailscaleResetConfirm,
      body: l.tailscaleResetBody,
      confirm: l.tailscaleReset,
      tone: ConfirmTone.danger,
    );
    if (!confirmed || !mounted || _busy) return;
    final seq = ++_uiActionSeq;
    setState(() {
      _actionBusy = true;
      _pendingEnabled = false;
      _topError = null;
      _advancedError = null;
      _warning = null;
    });
    widget.model.updateSettings((s) => s.tailscaleEnabled = false);
    try {
      final res = await widget.model.tailscale.reset();
      if (mounted) _authKeyCtrl.clear();
      if (mounted &&
          seq == _uiActionSeq &&
          res != null &&
          res.localCleared &&
          !res.loggedOut &&
          res.logoutError.isNotEmpty) {
        setState(() => _warning = l.tailscaleResetLocalOnly(res.logoutError));
      }
    } on Object catch (e) {
      if (mounted && seq == _uiActionSeq) {
        final msg = _tsErrorText(e, l);
        setState(() {
          _topError = msg;
          _advancedError = msg;
        });
      }
    } finally {
      if (mounted && seq == _uiActionSeq) {
        setState(() {
          _actionBusy = false;
          _pendingEnabled = null;
        });
      }
    }
  }

  String _statusLabel(AppLocalizations l, TailscaleStatus st) {
    if (st.isRunning) return l.tailscaleRunning;
    return switch (st.state) {
      'needs_login' => l.tailscaleNeedsLogin,
      'needs_machine_auth' => l.tailscaleNeedsApproval,
      'starting' => l.tailscaleStarting,
      'error' => l.tailscaleError,
      'stopped' => l.tailscaleStopped,
      _ =>
        (_pendingEnabled ?? widget.model.settings.tailscaleEnabled) && _busy
            ? l.tailscaleStarting
            : l.tailscaleOff,
    };
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final s = widget.model.settings;
    final ts = widget.model.tailscale;
    final st = ts.status;
    final busy = _busy;
    final switchValue = _pendingEnabled ?? s.tailscaleEnabled;
    final dotColor = tailscaleStatusColor(c, st);
    final activeTopError =
        _topError ??
        (st.error.isNotEmpty
            ? localizeTailscaleError(st.error, l)
            : (ts.error != null ? localizeTailscaleError(ts.error!, l) : null));
    final connectedUrl = widget.model.connectedUrl;
    final activeRoute = connectedUrl != null && isTsQuicUrl(connectedUrl)
        ? routeLabel(
            l,
            connectedUrl,
            peerRoute: widget.model.tailscalePeerRoute,
          )
        : null;
    final nodeLabel = st.dnsName.isNotEmpty ? st.dnsName : st.hostname;

    return Scaffold(
      appBar: AppBar(title: Text(l.tailscaleTitle)),
      body: SideSafe(
        child: ListView(
          padding: const EdgeInsets.only(top: 8, bottom: 32),
          children: [
            Section(
              children: [
                InkWell(
                  onTap: busy ? null : () => _onToggle(!switchValue),
                  child: Padding(
                    padding: const EdgeInsets.fromLTRB(14, 6, 8, 6),
                    child: Row(
                      children: [
                        Expanded(
                          child: Text(
                            l.tailscaleEnable,
                            style: TextStyle(fontSize: Fs.body, color: c.text),
                          ),
                        ),
                        PosseSwitch(
                          key: const Key('tailscale-enable-switch'),
                          value: switchValue,
                          onChanged: busy ? null : _onToggle,
                        ),
                      ],
                    ),
                  ),
                ),
                Padding(
                  padding: const EdgeInsets.fromLTRB(14, 12, 14, 14),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Row(
                        children: [
                          Container(
                            width: 8,
                            height: 8,
                            decoration: BoxDecoration(
                              color: dotColor,
                              shape: BoxShape.circle,
                            ),
                          ),
                          const SizedBox(width: 8),
                          Expanded(
                            child: Text(
                              _statusLabel(l, st),
                              style: TextStyle(
                                fontSize: Fs.body,
                                fontWeight: Fw.medium,
                                color: c.text,
                              ),
                            ),
                          ),
                          if (busy || st.state == 'starting')
                            HexSpinner(size: Ic.small, color: c.muted),
                        ],
                      ),
                      if (st.isRunning) ...[
                        const SizedBox(height: 10),
                        Container(
                          padding: const EdgeInsets.fromLTRB(12, 8, 4, 8),
                          decoration: BoxDecoration(
                            color: c.fill,
                            borderRadius: BorderRadius.circular(Rd.input),
                          ),
                          child: Row(
                            children: [
                              Expanded(
                                child: Column(
                                  crossAxisAlignment: CrossAxisAlignment.start,
                                  children: [
                                    Text(
                                      st.ip,
                                      key: const Key('tailscale-ip-text'),
                                      style: mono(Fs.body, color: c.text),
                                    ),
                                    if (nodeLabel.isNotEmpty) ...[
                                      const SizedBox(height: 2),
                                      Text(
                                        nodeLabel,
                                        style: mono(Fs.small, color: c.muted),
                                        maxLines: 1,
                                        overflow: TextOverflow.ellipsis,
                                      ),
                                    ],
                                  ],
                                ),
                              ),
                              IconButton(
                                key: const Key('tailscale-copy-ip'),
                                tooltip: l.commonCopy,
                                onPressed: () => _copyText(st.ip),
                                icon: Icon(
                                  Icons.content_copy_rounded,
                                  size: Ic.small,
                                  color: c.muted,
                                ),
                              ),
                            ],
                          ),
                        ),
                        if (activeRoute != null) ...[
                          const SizedBox(height: 8),
                          Text(
                            l.tailscaleCurrentRoute(activeRoute),
                            key: const Key('tailscale-current-route'),
                            style: TextStyle(
                              fontSize: Fs.small,
                              color: c.muted,
                            ),
                          ),
                        ],
                        const SizedBox(height: 8),
                        Text(
                          l.tailscaleRunningNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ] else if (st.needsLogin) ...[
                        const SizedBox(height: 8),
                        Text(
                          l.tailscaleNeedsLoginNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                        const SizedBox(height: 12),
                        if (st.authUrl.isNotEmpty) ...[
                          FilledButton.icon(
                            key: const Key('tailscale-open-login'),
                            onPressed: busy
                                ? null
                                : () => _openLogin(st.authUrl),
                            icon: const Icon(
                              Icons.open_in_browser_rounded,
                              size: Ic.small,
                            ),
                            label: Text(l.tailscaleOpenLogin),
                          ),
                          const SizedBox(height: 8),
                          Wrap(
                            spacing: 8,
                            runSpacing: 8,
                            children: [
                              OutlinedButton.icon(
                                key: const Key('tailscale-copy-login'),
                                onPressed: busy
                                    ? null
                                    : () => _copyLoginUrl(st.authUrl),
                                icon: const Icon(
                                  Icons.content_copy_rounded,
                                  size: Ic.small,
                                ),
                                label: Text(l.tailscaleCopyLoginUrl),
                              ),
                              TextButton.icon(
                                key: const Key('tailscale-refresh-login'),
                                onPressed: busy ? null : _refreshLogin,
                                icon: const Icon(
                                  Icons.refresh_rounded,
                                  size: Ic.small,
                                ),
                                label: Text(l.tailscaleRefreshLogin),
                              ),
                            ],
                          ),
                        ] else
                          Align(
                            alignment: Alignment.centerLeft,
                            child: TextButton.icon(
                              key: const Key('tailscale-refresh-login'),
                              onPressed: busy ? null : _refreshLogin,
                              icon: const Icon(
                                Icons.refresh_rounded,
                                size: Ic.small,
                              ),
                              label: Text(l.tailscaleRefreshLogin),
                            ),
                          ),
                      ] else if (st.needsMachineAuth) ...[
                        const SizedBox(height: 8),
                        Text(
                          l.tailscaleNeedsApprovalNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ] else if (st.state == 'starting') ...[
                        const SizedBox(height: 8),
                        Text(
                          l.tailscaleStartingNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ] else if (st.state == 'stopped') ...[
                        const SizedBox(height: 8),
                        Text(
                          l.tailscaleStoppedNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ] else ...[
                        const SizedBox(height: 8),
                        Text(
                          l.tailscaleNote,
                          style: TextStyle(
                            fontSize: Fs.small,
                            height: Lh.body,
                            color: c.muted,
                          ),
                        ),
                      ],
                      if (activeTopError != null &&
                          activeTopError.isNotEmpty) ...[
                        const SizedBox(height: 12),
                        _Banner(
                          key: const Key('tailscale-error-banner'),
                          icon: Icons.error_outline_rounded,
                          color: c.danger,
                          background: c.soft(c.danger),
                          text: activeTopError,
                        ),
                      ],
                      if (_warning != null && _warning!.isNotEmpty) ...[
                        const SizedBox(height: 12),
                        _Banner(
                          key: const Key('tailscale-warning-banner'),
                          icon: Icons.warning_amber_rounded,
                          color: c.honey,
                          background: c.soft(c.honey),
                          text: _warning!,
                        ),
                      ],
                    ],
                  ),
                ),
              ],
            ),
            Section(
              folded: !_advancedExpanded,
              children: [
                RowTile(
                  key: const Key('tailscale-advanced-toggle'),
                  chevron: false,
                  title: Text(l.tailscaleAdvanced),
                  trailing: Icon(
                    _advancedExpanded
                        ? Icons.expand_less_rounded
                        : Icons.expand_more_rounded,
                    size: Ic.medium,
                    color: c.muted,
                  ),
                  onTap: () =>
                      setState(() => _advancedExpanded = !_advancedExpanded),
                ),
                Padding(
                  padding: const EdgeInsets.fromLTRB(14, 12, 14, 14),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      _FieldLabel(l.tailscaleHostname),
                      const SizedBox(height: 6),
                      TextField(
                        key: const Key('tailscale-hostname-field'),
                        controller: _hostnameCtrl,
                        enabled: !busy,
                        autocorrect: false,
                        enableSuggestions: false,
                        decoration: InputDecoration(
                          hintText: l.tailscaleHostnamePlaceholder,
                        ),
                      ),
                      const SizedBox(height: 14),
                      _FieldLabel(l.tailscaleControlUrl),
                      const SizedBox(height: 6),
                      TextField(
                        key: const Key('tailscale-control-url-field'),
                        controller: _controlUrlCtrl,
                        enabled: !busy,
                        keyboardType: TextInputType.url,
                        autocorrect: false,
                        enableSuggestions: false,
                        decoration: InputDecoration(
                          hintText: l.tailscaleControlUrlPlaceholder,
                          errorText: _controlUrlError,
                        ),
                      ),
                      const SizedBox(height: 14),
                      _FieldLabel(l.tailscaleAuthKey),
                      const SizedBox(height: 6),
                      TextField(
                        key: const Key('tailscale-auth-key-field'),
                        controller: _authKeyCtrl,
                        enabled: !busy,
                        obscureText: _obscureAuthKey,
                        autocorrect: false,
                        enableSuggestions: false,
                        enableIMEPersonalizedLearning: false,
                        decoration: InputDecoration(
                          hintText: l.tailscaleAuthKeyPlaceholder,
                          suffixIcon: IconButton(
                            onPressed: () => setState(
                              () => _obscureAuthKey = !_obscureAuthKey,
                            ),
                            icon: Icon(
                              _obscureAuthKey
                                  ? Icons.visibility_rounded
                                  : Icons.visibility_off_rounded,
                              size: Ic.small,
                              color: c.muted,
                            ),
                          ),
                        ),
                      ),
                      const SizedBox(height: 6),
                      Text(
                        l.tailscaleAuthKeyNote,
                        style: TextStyle(
                          fontSize: Fs.small,
                          height: Lh.body,
                          color: c.muted,
                        ),
                      ),
                      const SizedBox(height: 14),
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              l.tailscaleDisableBind,
                              style: TextStyle(
                                fontSize: Fs.body,
                                color: c.text,
                              ),
                            ),
                          ),
                          PosseSwitch(
                            key: const Key('tailscale-disable-bind-switch'),
                            value: _disableBind,
                            onChanged: busy
                                ? null
                                : (v) => setState(() {
                                    _disableBind = v;
                                    _draftDirty = true;
                                  }),
                          ),
                        ],
                      ),
                      const SizedBox(height: 4),
                      Text(
                        l.tailscaleDisableBindNote,
                        style: TextStyle(
                          fontSize: Fs.small,
                          height: Lh.body,
                          color: c.muted,
                        ),
                      ),
                      if (_advancedError != null &&
                          _advancedError!.isNotEmpty) ...[
                        const SizedBox(height: 12),
                        _Banner(
                          key: const Key('tailscale-advanced-error-banner'),
                          icon: Icons.error_outline_rounded,
                          color: c.danger,
                          background: c.soft(c.danger),
                          text: _advancedError!,
                        ),
                      ],
                      if (_warning != null && _warning!.isNotEmpty) ...[
                        const SizedBox(height: 12),
                        _Banner(
                          key: const Key('tailscale-advanced-warning-banner'),
                          icon: Icons.warning_amber_rounded,
                          color: c.honey,
                          background: c.soft(c.honey),
                          text: _warning!,
                        ),
                      ],
                      const SizedBox(height: 16),
                      FilledButton(
                        key: const Key('tailscale-save-advanced'),
                        onPressed: busy ? null : _saveAdvanced,
                        child: Text(l.commonSave),
                      ),
                      const SizedBox(height: 8),
                      TextButton.icon(
                        key: const Key('tailscale-reset-button'),
                        onPressed: busy ? null : _resetIdentity,
                        icon: Icon(
                          Icons.restart_alt_rounded,
                          size: Ic.small,
                          color: busy ? c.faint : c.danger,
                        ),
                        label: Text(
                          l.tailscaleReset,
                          style: TextStyle(color: busy ? c.faint : c.danger),
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
    );
  }
}

class _FieldLabel extends StatelessWidget {
  const _FieldLabel(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Text(
      text,
      style: TextStyle(
        fontSize: Fs.small,
        fontWeight: Fw.medium,
        color: c.muted,
      ),
    );
  }
}

class _Banner extends StatelessWidget {
  const _Banner({
    super.key,
    required this.icon,
    required this.color,
    required this.background,
    required this.text,
  });

  final IconData icon;
  final Color color;
  final Color background;
  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(Rd.input),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(icon, size: Ic.small, color: color),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              text,
              style: TextStyle(
                fontSize: Fs.small,
                height: Lh.body,
                color: color,
              ),
            ),
          ),
        ],
      ),
    );
  }
}
