import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

const _linuxLingerCommand = 'loginctl enable-linger \$USER';
const _autostartWriteTimeout = Duration(seconds: 45);

/// 手机端「主机详情」数据模型：对应服务端 posse.host.info / posse.host.memory / posse.host.autostartSet。
class HostDetailsData {
  const HostDetailsData({
    required this.version,
    required this.commit,
    required this.build,
    required this.platform,
    required this.arch,
    required this.edition,
    required this.managed,
    required this.pid,
    required this.startedAtMs,
    required this.home,
    required this.terminals,
    required this.runningTerminals,
    required this.daemonMemoryKB,
    this.autostart,
    this.autostartError = '',
  });

  final String version;
  final String commit;
  final String build;
  final String platform;
  final String arch;
  final String edition;
  final bool managed;
  final int pid;
  final int startedAtMs;
  final String home;
  final int terminals;
  final int runningTerminals;
  final int daemonMemoryKB;
  final HostAutostartData? autostart;
  final String autostartError;

  static HostDetailsData fromJson(Map<String, Object?> raw) {
    final autoRaw = raw['autostart'];
    return HostDetailsData(
      version: raw['version'] as String? ?? '',
      commit: raw['commit'] as String? ?? '',
      build: raw['build'] as String? ?? '',
      platform: raw['platform'] as String? ?? '',
      arch: raw['arch'] as String? ?? '',
      edition: raw['edition'] as String? ?? '',
      managed: raw['managed'] == true,
      pid: (raw['pid'] as num?)?.toInt() ?? 0,
      startedAtMs: (raw['startedAt'] as num?)?.toInt() ?? 0,
      home: raw['home'] as String? ?? '',
      terminals: (raw['terminals'] as num?)?.toInt() ?? 0,
      runningTerminals: (raw['runningTerminals'] as num?)?.toInt() ?? 0,
      daemonMemoryKB: (raw['daemonMemoryKB'] as num?)?.toInt() ?? 0,
      autostart: autoRaw is Map<String, Object?>
          ? HostAutostartData.fromJson(autoRaw)
          : autoRaw is Map
          ? HostAutostartData.fromJson(Map<String, Object?>.from(autoRaw))
          : null,
      autostartError: raw['autostartError'] as String? ?? '',
    );
  }

  HostDetailsData copyWith({
    HostAutostartData? autostart,
    String? autostartError,
  }) => HostDetailsData(
    version: version,
    commit: commit,
    build: build,
    platform: platform,
    arch: arch,
    edition: edition,
    managed: managed,
    pid: pid,
    startedAtMs: startedAtMs,
    home: home,
    terminals: terminals,
    runningTerminals: runningTerminals,
    daemonMemoryKB: daemonMemoryKB,
    autostart: autostart ?? this.autostart,
    autostartError: autostartError ?? this.autostartError,
  );
}

class HostAutostartData {
  const HostAutostartData({
    required this.supported,
    required this.kind,
    required this.managed,
    required this.writable,
    required this.installed,
    required this.enabled,
    required this.active,
    required this.unit,
    required this.path,
    required this.linger,
    required this.noteCode,
  });

  final bool supported;
  final String kind;
  final bool managed;
  final bool writable;
  final bool installed;
  final bool enabled;
  final bool active;
  final String unit;
  final String path;
  final String linger;
  final String noteCode;

  static HostAutostartData fromJson(Map<String, Object?> raw) =>
      HostAutostartData(
        supported: raw['supported'] == true,
        kind: raw['kind'] as String? ?? '',
        managed: raw['managed'] == true,
        writable: raw['writable'] == true,
        installed: raw['installed'] == true,
        enabled: raw['enabled'] == true,
        active: raw['active'] == true,
        unit: raw['unit'] as String? ?? '',
        path: raw['path'] as String? ?? '',
        linger: raw['linger'] as String? ?? '',
        noteCode: raw['noteCode'] as String? ?? '',
      );
}

class HostMemoryData {
  const HostMemoryData({
    required this.totalKB,
    required this.workspaces,
    required this.truncated,
  });

  final int totalKB;
  final List<HostMemoryWorkspace> workspaces;
  final bool truncated;

  static HostMemoryData fromJson(Map<String, Object?> raw) {
    final list = <HostMemoryWorkspace>[];
    final ws = raw['workspaces'];
    if (ws is List) {
      for (final item in ws) {
        if (item is Map) {
          list.add(
            HostMemoryWorkspace.fromJson(Map<String, Object?>.from(item)),
          );
        }
      }
    }
    return HostMemoryData(
      totalKB: (raw['totalKB'] as num?)?.toInt() ?? 0,
      workspaces: list,
      truncated: raw['truncated'] == true,
    );
  }
}

class HostMemoryWorkspace {
  const HostMemoryWorkspace({
    required this.id,
    required this.label,
    required this.kb,
    required this.terminals,
  });

  final String id;
  final String label;
  final int kb;
  final List<HostMemoryTerminal> terminals;

  static HostMemoryWorkspace fromJson(Map<String, Object?> raw) {
    final terms = <HostMemoryTerminal>[];
    final rawTerms = raw['terminals'];
    if (rawTerms is List) {
      for (final item in rawTerms) {
        if (item is Map) {
          terms.add(
            HostMemoryTerminal.fromJson(Map<String, Object?>.from(item)),
          );
        }
      }
    }
    return HostMemoryWorkspace(
      id: raw['id'] as String? ?? '',
      label: raw['label'] as String? ?? '',
      kb: (raw['kb'] as num?)?.toInt() ?? 0,
      terminals: terms,
    );
  }
}

class HostMemoryTerminal {
  const HostMemoryTerminal({
    required this.id,
    required this.title,
    required this.agent,
    required this.kb,
    required this.procs,
  });

  final String id;
  final String title;
  final String agent;
  final int kb;
  final List<HostMemoryProc> procs;

  static HostMemoryTerminal fromJson(Map<String, Object?> raw) {
    final procs = <HostMemoryProc>[];
    final rawProcs = raw['procs'];
    if (rawProcs is List) {
      for (final item in rawProcs) {
        if (item is Map) {
          procs.add(HostMemoryProc.fromJson(Map<String, Object?>.from(item)));
        }
      }
    }
    return HostMemoryTerminal(
      id: raw['id'] as String? ?? '',
      title: raw['title'] as String? ?? '',
      agent: raw['agent'] as String? ?? '',
      kb: (raw['kb'] as num?)?.toInt() ?? 0,
      procs: procs,
    );
  }
}

/// 对应服务端 rpc.DaemonMemoryProc：同名进程按内核短名合并计数（name / kb / count）。
class HostMemoryProc {
  const HostMemoryProc({
    required this.name,
    required this.kb,
    required this.count,
  });

  final String name;
  final int kb;
  final int count;

  static HostMemoryProc fromJson(Map<String, Object?> raw) {
    final count = (raw['count'] as num?)?.toInt() ?? 1;
    return HostMemoryProc(
      name: raw['name'] as String? ?? '',
      kb: (raw['kb'] as num?)?.toInt() ?? 0,
      count: count < 1 ? 1 : count,
    );
  }
}

/// 已连接主机详情页：展示平台/形态/版本/PID/运行时长、托管无头节点开机自启状态与开关，以及终端内存明细。
class HostDetailsScreen extends StatefulWidget {
  const HostDetailsScreen({super.key, required this.model});

  final PosseAppModel model;

  @override
  State<HostDetailsScreen> createState() => _HostDetailsScreenState();
}

class _HostDetailsScreenState extends State<HostDetailsScreen> {
  HostDetailsData? _info;
  PhoneRpc? _infoRpc;
  String? _infoComputerId;

  HostMemoryData? _memory;
  bool _loadingInfo = true;
  bool _loadingMemory = false;
  bool _infoUnsupported = false;
  bool _memoryUnsupported = false;

  Object? _infoError;
  Object? _memoryError;
  Object? _autostartError;

  final Set<String> _expanded = {};
  int _loadGen = 0;
  int _memGen = 0;

  // 自启写入按页面电脑代次 + 精确令牌追踪，彻底隔离 A→B→A 迟到旧回执；写失败或服务端仍忙时不误解锁。
  int _computerEpoch = 0;
  int _writeTokenSeq = 0;
  final Set<int> _inFlightWriteTokens = {};
  int _reconcileSeq = 0;
  bool _autostartNeedsReconcile = false;
  int _autostartVersion = 0;

  PhoneRpc? _boundRpc;
  String? _boundComputerId;

  bool get _autostartBusy => _inFlightWriteTokens.isNotEmpty;

  @override
  void initState() {
    super.initState();
    _boundRpc = widget.model.rpc;
    _boundComputerId = widget.model.activeId;
    widget.model.addListener(_onModelChanged);
    unawaited(_refreshAll());
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModelChanged);
    _loadGen++;
    _memGen++;
    super.dispose();
  }

  void _onModelChanged() {
    if (!mounted) return;
    final nowRpc = widget.model.rpc;
    final nowComputerId = widget.model.activeId;
    final computerChanged = nowComputerId != _boundComputerId;
    final rpcChanged = !identical(nowRpc, _boundRpc);
    if (!computerChanged && !rpcChanged) {
      setState(() {});
      return;
    }

    _boundRpc = nowRpc;
    _boundComputerId = nowComputerId;
    // 切换电脑时递增页面代次并清空旧主机数据与写令牌，确保 A→B→A 时旧 A 回执绝不干扰新 A 状态；
    // 同电脑更换传输（如 WS→QUIC）或断线时保留同代次在途写令牌，待旧写结束后再用当前 RPC 对账。
    if (computerChanged) {
      _computerEpoch++;
      _loadGen++;
      _memGen++;
      _reconcileSeq++;
      _inFlightWriteTokens.clear();
      _autostartNeedsReconcile = false;
      _info = null;
      _infoRpc = null;
      _infoComputerId = null;
      _memory = null;
      _expanded.clear();
      _infoUnsupported = false;
      _memoryUnsupported = false;
      _infoError = null;
      _memoryError = null;
      _autostartError = null;
    } else if (rpcChanged && _inFlightWriteTokens.isNotEmpty) {
      // 同机换传输时旧写仍在途：标记需要待旧写结束后再按新连接对账，禁止提前重复提交。
      _autostartNeedsReconcile = true;
    }

    if (nowRpc != null) {
      unawaited(_refreshAll());
    } else {
      _loadGen++;
      _memGen++;
      setState(() {
        _loadingInfo = false;
        _loadingMemory = false;
      });
    }
  }

  Future<void> _refreshAll() async {
    final rpc = widget.model.rpc;
    final computerId = widget.model.activeId;
    final gen = ++_loadGen;
    final memGen = ++_memGen;
    if (rpc == null) {
      if (mounted) {
        setState(() {
          _loadingInfo = false;
          _loadingMemory = false;
        });
      }
      return;
    }
    if (mounted) {
      setState(() {
        _loadingInfo = true;
        _loadingMemory = true;
        _infoError = null;
        _memoryError = null;
      });
    }
    final epoch = _computerEpoch;
    final expanded = _expanded.toList()..sort();
    await Future.wait([
      _fetchInfo(rpc, computerId, epoch, gen, _autostartVersion),
      _fetchMemory(rpc, computerId, gen, memGen, expanded),
    ]);
  }

  Future<void> _fetchInfo(
    PhoneRpc rpc,
    String? computerId,
    int epoch,
    int gen,
    int autostartVersionAtStart,
  ) async {
    try {
      final raw = await rpc.call('posse.host.info', const {});
      if (!mounted ||
          epoch != _computerEpoch ||
          gen != _loadGen ||
          !identical(widget.model.rpc, rpc) ||
          widget.model.activeId != computerId) {
        return;
      }
      var parsed = HostDetailsData.fromJson(raw);
      // 若本次读取期间已完成更新的自启写入，不让较早发出的读回执覆盖较新的自启状态。
      if (autostartVersionAtStart < _autostartVersion &&
          _info != null &&
          _infoComputerId == computerId) {
        parsed = parsed.copyWith(
          autostart: _info!.autostart,
          autostartError: _info!.autostartError,
        );
      }
      final autostartReady =
          parsed.autostart != null && parsed.autostartError.isEmpty;
      setState(() {
        if (autostartReady) _reconcileSeq++;
        _info = parsed;
        _infoRpc = rpc;
        _infoComputerId = computerId;
        _infoUnsupported = false;
        _infoError = null;
        _loadingInfo = false;
        if (_inFlightWriteTokens.isEmpty && autostartReady) {
          _autostartNeedsReconcile = false;
        }
      });
    } catch (err) {
      if (!mounted ||
          gen != _loadGen ||
          !identical(widget.model.rpc, rpc) ||
          widget.model.activeId != computerId) {
        return;
      }
      setState(() {
        _loadingInfo = false;
        if (err is RpcException && err.missingMethod) {
          _infoUnsupported = true;
          _infoError = null;
        } else {
          _infoError = err;
        }
      });
    }
  }

  Future<void> _fetchMemory(
    PhoneRpc rpc,
    String? computerId,
    int loadGen,
    int memGen,
    List<String> expanded,
  ) async {
    try {
      final raw = await rpc.call(
        'posse.host.memory',
        expanded.isEmpty ? const {} : {'expanded': expanded},
      );
      if (!mounted ||
          loadGen != _loadGen ||
          memGen != _memGen ||
          !identical(widget.model.rpc, rpc) ||
          widget.model.activeId != computerId) {
        return;
      }
      setState(() {
        _memory = HostMemoryData.fromJson(raw);
        _memoryUnsupported = false;
        _memoryError = null;
        _loadingMemory = false;
      });
    } catch (err) {
      if (!mounted ||
          loadGen != _loadGen ||
          memGen != _memGen ||
          !identical(widget.model.rpc, rpc) ||
          widget.model.activeId != computerId) {
        return;
      }
      setState(() {
        _loadingMemory = false;
        if (err is RpcException && err.missingMethod) {
          _memoryUnsupported = true;
          _memoryError = null;
        } else {
          _memoryError = err;
        }
      });
    }
  }

  Future<void> _toggleTerminal(String termId) async {
    final rpc = widget.model.rpc;
    final computerId = widget.model.activeId;
    if (rpc == null) return;
    setState(() {
      if (!_expanded.remove(termId)) {
        _expanded.add(termId);
      }
      _loadingMemory = true;
      _memoryError = null;
    });
    final memGen = ++_memGen;
    final expanded = _expanded.toList()..sort();
    await _fetchMemory(rpc, computerId, _loadGen, memGen, expanded);
  }

  /// 仅当开关绑定的电脑 ID 与当前活跃 RPC 完全一致、且无待对账/在途写时才允许提交修改，防切主机误写与并发叠写。
  Future<void> _setAutostart(
    bool enabled, {
    required PhoneRpc expectedRpc,
    required String? expectedComputerId,
  }) async {
    if (_autostartBusy || _autostartNeedsReconcile) return;
    if (!identical(widget.model.rpc, expectedRpc) ||
        widget.model.activeId != expectedComputerId) {
      return;
    }
    final epoch = _computerEpoch;
    final token = ++_writeTokenSeq;
    setState(() {
      _reconcileSeq++;
      _inFlightWriteTokens.add(token);
      _autostartError = null;
    });
    try {
      final raw = await expectedRpc.call('posse.host.autostartSet', {
        'enabled': enabled,
      }, timeout: _autostartWriteTimeout);
      if (!mounted ||
          epoch != _computerEpoch ||
          !_inFlightWriteTokens.remove(token)) {
        return;
      }
      final updated = HostAutostartData.fromJson(raw);
      final sameRpc = identical(widget.model.rpc, expectedRpc);
      if (sameRpc) {
        setState(() {
          _reconcileSeq++;
          _autostartVersion++;
          _autostartError = null;
          if (_inFlightWriteTokens.isEmpty) {
            _autostartNeedsReconcile = false;
          }
          if (_info != null && _infoComputerId == expectedComputerId) {
            _info = _info!.copyWith(autostart: updated, autostartError: '');
          }
        });
        return;
      }
      // 同电脑切换了传输（如 WS→QUIC）：旧连接写操作可能晚于新连接首读才生效，必须在当前连接重新拉取权威状态。
      setState(() {
        _autostartNeedsReconcile = true;
      });
      await _reconcileAutostartOnCurrentRpc(expectedComputerId, epoch);
    } catch (err) {
      if (!mounted ||
          epoch != _computerEpoch ||
          !_inFlightWriteTokens.remove(token)) {
        return;
      }
      setState(() {
        _autostartError = err;
        _autostartNeedsReconcile = true;
      });
      // 写失败、连接中断或超时后用当前连接对账真实状态；仅在服务端返回无错的终态快照时解锁开关。
      await _reconcileAutostartOnCurrentRpc(expectedComputerId, epoch);
    }
  }

  Future<void> _reconcileAutostartOnCurrentRpc(
    String? computerId,
    int epoch,
  ) async {
    final curRpc = widget.model.rpc;
    if (curRpc == null ||
        epoch != _computerEpoch ||
        widget.model.activeId != computerId) {
      return;
    }
    final seq = ++_reconcileSeq;
    final versionAtStart = _autostartVersion;
    try {
      final fresh = await curRpc.call('posse.host.info', const {});
      if (!mounted ||
          epoch != _computerEpoch ||
          seq != _reconcileSeq ||
          versionAtStart != _autostartVersion ||
          widget.model.activeId != computerId ||
          !identical(widget.model.rpc, curRpc)) {
        return;
      }
      final parsed = HostDetailsData.fromJson(fresh);
      final autostartReady =
          parsed.autostart != null && parsed.autostartError.isEmpty;
      setState(() {
        _autostartVersion++;
        _info = parsed;
        _infoRpc = curRpc;
        _infoComputerId = computerId;
        _infoUnsupported = false;
        _infoError = null;
        if (_inFlightWriteTokens.isEmpty && autostartReady) {
          _autostartNeedsReconcile = false;
        }
      });
    } catch (_) {
      // 对账失败时保持 _autostartNeedsReconcile = true，避免拿过时或未知终态继续提交写操作。
    }
  }

  Future<void> _copyText(String text) async {
    await Clipboard.setData(ClipboardData(text: text));
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(context.l10n.hostDetailCopied),
        duration: const Duration(seconds: 2),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final computer = widget.model.current;
    final title = widget.model.labelOf(computer, l);
    final connected = widget.model.rpc != null;
    final refreshing = _loadingInfo || _loadingMemory;

    return Scaffold(
      appBar: AppBar(
        title: Text(title.isEmpty ? l.hostDetailsTitle : title),
        actions: [
          if (connected)
            IconButton(
              tooltip: l.hostDetailRefresh,
              onPressed: refreshing ? null : () => unawaited(_refreshAll()),
              icon: refreshing
                  ? const HexSpinner(size: Ic.medium)
                  : const Icon(Icons.refresh_rounded),
            ),
        ],
      ),
      body: SideSafe(
        child: !connected
            ? Center(
                child: Padding(
                  padding: const EdgeInsets.all(24),
                  child: Text(
                    l.hostDetailDisconnected,
                    textAlign: TextAlign.center,
                    style: TextStyle(fontSize: Fs.body, color: c.muted),
                  ),
                ),
              )
            : RefreshIndicator(
                color: c.accent,
                onRefresh: _refreshAll,
                child: ListView(
                  physics: const AlwaysScrollableScrollPhysics(),
                  padding: EdgeInsets.only(
                    top: 8,
                    bottom: 24 + MediaQuery.paddingOf(context).bottom,
                  ),
                  children: [
                    if (_infoUnsupported && _memoryUnsupported)
                      NoticeBar(l.hostDetailLegacyNote),
                    if (_infoError != null)
                      NoticeBar(friendlyError(_infoError!, l)),
                    _buildOverviewSection(context, c, l),
                    if (!_infoUnsupported && _info != null)
                      _buildAutostartSection(context, c, l, _info!),
                    if (!_memoryUnsupported)
                      _buildMemorySection(context, c, l)
                    else if (!_infoUnsupported)
                      Section(
                        title: l.hostSectionResources,
                        children: [
                          Padding(
                            padding: const EdgeInsets.all(14),
                            child: Text(
                              l.hostDetailLegacyNote,
                              style: TextStyle(
                                fontSize: Fs.small,
                                height: Lh.body,
                                color: c.muted,
                              ),
                            ),
                          ),
                        ],
                      ),
                  ],
                ),
              ),
      ),
    );
  }

  Widget _buildOverviewSection(
    BuildContext context,
    PosseColors c,
    AppLocalizations l,
  ) {
    final info = _info;
    final live = widget.model.hostStatus;
    final saved = widget.model.current;
    final platform = info?.platform.isNotEmpty == true
        ? info!.platform
        : (live?.platform.isNotEmpty == true
              ? live!.platform
              : (saved?.platform ?? ''));
    final arch = info?.arch.isNotEmpty == true ? info!.arch : (live?.arch ?? '');
    final edition = info?.edition.isNotEmpty == true
        ? info!.edition
        : (live?.edition.isNotEmpty == true
              ? live!.edition
              : (saved?.edition ?? ''));
    final managed = info?.managed ?? live?.managed ?? saved?.managed ?? false;
    final version = info?.version.isNotEmpty == true
        ? info!.version
        : (live?.version.isNotEmpty == true
              ? live!.version
              : (saved?.version ?? ''));
    final commit = info?.commit.isNotEmpty == true
        ? info!.commit
        : (live?.commit ?? '');

    final editionLabel = switch (edition) {
      'headless' =>
        managed ? l.hostEditionHeadlessManaged : l.hostEditionHeadlessCustom,
      'full' => l.hostEditionFull,
      _ => '',
    };
    final platformLabel = _formatPlatform(platform, arch);
    final versionLabel = commit.isNotEmpty && commit != 'dev'
        ? '$version (${commit.length > 7 ? commit.substring(0, 7) : commit})'
        : version;

    final rows = <Widget>[];
    if (editionLabel.isNotEmpty) {
      rows.add(_kvRow(c, l.hostFieldEdition, editionLabel));
    }
    if (versionLabel.isNotEmpty) {
      rows.add(_kvRow(c, l.hostFieldVersion, versionLabel, monospace: true));
    }
    if (platformLabel.isNotEmpty) {
      rows.add(_kvRow(c, l.hostFieldPlatform, platformLabel));
    }
    if (info != null && info.pid > 0) {
      final uptime = info.startedAtMs > 0
          ? _formatUptime(
              l,
              DateTime.now().difference(
                DateTime.fromMillisecondsSinceEpoch(info.startedAtMs),
              ),
            )
          : '';
      final procText = uptime.isNotEmpty
          ? '${l.hostProcessPid('${info.pid}')} · ${l.hostProcessUp(uptime)}'
          : l.hostProcessPid('${info.pid}');
      rows.add(_kvRow(c, l.hostFieldProcess, procText, monospace: true));
    }
    if (info != null && info.home.isNotEmpty) {
      rows.add(
        RowTile(
          chevron: false,
          title: Text(
            l.hostFieldDataDir,
            style: TextStyle(fontSize: Fs.small, color: c.muted),
          ),
          subtitle: Text(
            info.home,
            style: mono(Fs.small, color: c.text),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
          ),
          trailing: Icon(Icons.copy_rounded, size: Ic.small, color: c.muted),
          onTap: () => unawaited(_copyText(info.home)),
        ),
      );
    }
    if (rows.isEmpty && _loadingInfo) {
      rows.add(
        Padding(
          padding: const EdgeInsets.all(16),
          child: Row(
            children: [
              const HexSpinner(size: Ic.medium),
              const SizedBox(width: 8),
              Text(
                l.hostDetailLoading,
                style: TextStyle(fontSize: Fs.body, color: c.muted),
              ),
            ],
          ),
        ),
      );
    }
    return Section(title: l.hostSectionServer, children: rows);
  }

  Widget _buildAutostartSection(
    BuildContext context,
    PosseColors c,
    AppLocalizations l,
    HostDetailsData info,
  ) {
    final auto = info.autostart;
    final rows = <Widget>[];
    if (_autostartError != null) {
      rows.add(
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 12, 14, 4),
          child: Text(
            friendlyError(_autostartError!, l),
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.danger,
            ),
          ),
        ),
      );
    }
    if (info.autostartError.isNotEmpty) {
      rows.add(
        Padding(
          padding: const EdgeInsets.all(14),
          child: Text(
            info.autostartError,
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.danger,
            ),
          ),
        ),
      );
      return Section(title: l.hostSectionAutostart, children: rows);
    }
    if (auto == null || !auto.supported) {
      rows.add(
        Padding(
          padding: const EdgeInsets.all(14),
          child: Text(
            l.hostAutostartUnsupported,
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.muted,
            ),
          ),
        ),
      );
      return Section(title: l.hostSectionAutostart, children: rows);
    }

    final serviceKind = switch (auto.kind) {
      'launchd' => l.hostAutostartLaunchd,
      'systemd' => l.hostAutostartSystemd,
      _ => auto.kind,
    };
    final statusText = _autostartStatusLabel(l, auto);
    final boundRpc = _infoRpc;
    final boundComputerId = _infoComputerId;
    // 只有当前快照确实来自当前活跃 RPC 与同一电脑、且无在途写或待对账状态时才允许操作开关。
    final canWriteNow =
        auto.managed &&
        auto.writable &&
        !_autostartBusy &&
        !_autostartNeedsReconcile &&
        boundRpc != null &&
        identical(widget.model.rpc, boundRpc) &&
        widget.model.activeId == boundComputerId;

    if (auto.managed && auto.writable) {
      rows.add(
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
          child: Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      serviceKind,
                      style: TextStyle(
                        fontSize: Fs.body,
                        fontWeight: Fw.medium,
                        color: c.text,
                      ),
                    ),
                    const SizedBox(height: 2),
                    Text(
                      _autostartBusy ? l.hostAutostartUpdating : statusText,
                      style: TextStyle(fontSize: Fs.small, color: c.muted),
                    ),
                  ],
                ),
              ),
              if (_autostartBusy) ...[
                const HexSpinner(size: Ic.medium),
                const SizedBox(width: 8),
              ],
              Switch.adaptive(
                value: auto.enabled,
                activeTrackColor: c.accent,
                onChanged: (_autostartBusy || !canWriteNow)
                    ? null
                    : (v) => unawaited(
                        _setAutostart(
                          v,
                          expectedRpc: boundRpc,
                          expectedComputerId: boundComputerId,
                        ),
                      ),
              ),
            ],
          ),
        ),
      );
      rows.add(
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 0, 14, 12),
          child: Text(
            l.hostAutostartToggleNote,
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.muted,
            ),
          ),
        ),
      );
    } else {
      rows.add(_kvRow(c, serviceKind, statusText));
      final note = info.edition == 'full'
          ? l.hostAutostartFullNote
          : l.hostAutostartCustomNote;
      rows.add(
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 0, 14, 12),
          child: Text(
            note,
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.muted,
            ),
          ),
        ),
      );
    }

    if (auto.path.isNotEmpty) {
      rows.add(
        RowTile(
          chevron: false,
          title: Text(
            l.hostAutostartFieldPath,
            style: TextStyle(fontSize: Fs.small, color: c.muted),
          ),
          subtitle: Text(
            auto.path,
            style: mono(Fs.small, color: c.text),
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
          ),
          trailing: Icon(Icons.copy_rounded, size: Ic.small, color: c.muted),
          onTap: () => unawaited(_copyText(auto.path)),
        ),
      );
    }

    if (auto.noteCode == 'aqua_inactive') {
      rows.add(_noteBanner(c, c.honey, l.hostAutostartNoteAqua));
    } else if (auto.noteCode == 'systemd_bus_inactive') {
      rows.add(_noteBanner(c, c.honey, l.hostAutostartNoteSystemdBus));
    }

    if (auto.kind == 'systemd' &&
        (auto.linger == 'no' || auto.noteCode == 'systemd_linger_disabled')) {
      rows.add(
        Padding(
          padding: const EdgeInsets.fromLTRB(14, 8, 14, 14),
          child: Container(
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(
              color: c.soft(c.honey),
              borderRadius: BorderRadius.circular(Rd.input),
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  l.hostAutostartNoteLinger,
                  style: TextStyle(
                    fontSize: Fs.small,
                    height: Lh.body,
                    color: c.text,
                  ),
                ),
                const SizedBox(height: 8),
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        _linuxLingerCommand,
                        style: mono(Fs.small, color: c.text),
                      ),
                    ),
                    const SizedBox(width: 8),
                    TextButton(
                      onPressed: () =>
                          unawaited(_copyText(_linuxLingerCommand)),
                      child: Text(l.hostAutostartCopyLinger),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      );
    }

    return Section(title: l.hostSectionAutostart, children: rows);
  }

  Widget _buildMemorySection(
    BuildContext context,
    PosseColors c,
    AppLocalizations l,
  ) {
    final info = _info;
    final mem = _memory;
    final rows = <Widget>[];
    if (info != null && info.daemonMemoryKB > 0) {
      rows.add(
        _kvRow(
          c,
          l.hostResDaemonMem,
          _formatMemoryKB(info.daemonMemoryKB),
          monospace: true,
        ),
      );
    }
    if (info != null) {
      rows.add(
        _kvRow(
          c,
          l.hostResTerminals,
          l.hostResTerminalsCount(info.runningTerminals, info.terminals),
        ),
      );
    }
    if (mem != null) {
      rows.add(
        _kvRow(
          c,
          l.hostResTermsMem,
          _formatMemoryKB(mem.totalKB),
          monospace: true,
        ),
      );
    }
    if (_memoryError != null) {
      rows.add(
        Padding(
          padding: const EdgeInsets.all(14),
          child: Text(
            friendlyError(_memoryError!, l),
            style: TextStyle(
              fontSize: Fs.small,
              height: Lh.body,
              color: c.danger,
            ),
          ),
        ),
      );
    } else if (_loadingMemory && mem == null) {
      rows.add(
        Padding(
          padding: const EdgeInsets.all(14),
          child: Row(
            children: [
              const HexSpinner(size: Ic.medium),
              const SizedBox(width: 8),
              Text(
                l.hostDetailLoading,
                style: TextStyle(fontSize: Fs.small, color: c.muted),
              ),
            ],
          ),
        ),
      );
    }
    if (mem != null) {
      for (final ws in mem.workspaces) {
        rows.add(
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 10, 14, 4),
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    ws.label,
                    style: TextStyle(
                      fontSize: Fs.small,
                      fontWeight: Fw.medium,
                      color: c.muted,
                    ),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  _formatMemoryKB(ws.kb),
                  style: mono(Fs.small, color: c.muted),
                ),
              ],
            ),
          ),
        );
        for (final term in ws.terminals) {
          final isOpen = _expanded.contains(term.id);
          rows.add(
            InkWell(
              onTap: () => unawaited(_toggleTerminal(term.id)),
              child: Padding(
                padding: const EdgeInsets.symmetric(
                  horizontal: 14,
                  vertical: 10,
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Icon(
                          isOpen
                              ? Icons.expand_more_rounded
                              : Icons.chevron_right_rounded,
                          size: Ic.small,
                          color: c.faint,
                        ),
                        const SizedBox(width: 6),
                        Expanded(
                          child: Text(
                            term.title.isEmpty ? term.id : term.title,
                            style: TextStyle(fontSize: Fs.body, color: c.text),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                        const SizedBox(width: 8),
                        Text(
                          _formatMemoryKB(term.kb),
                          style: mono(Fs.small, color: c.muted),
                        ),
                      ],
                    ),
                    if (isOpen && term.procs.isNotEmpty)
                      Padding(
                        padding: const EdgeInsets.only(left: 20, top: 6),
                        child: Column(
                          children: [
                            for (final proc in term.procs)
                              Padding(
                                padding: const EdgeInsets.symmetric(
                                  vertical: 2,
                                ),
                                child: Row(
                                  children: [
                                    Expanded(
                                      child: Text(
                                        proc.count > 1
                                            ? l.hostResProcGroup(
                                                proc.name,
                                                proc.count,
                                              )
                                            : proc.name,
                                        style: mono(Fs.small, color: c.muted),
                                        maxLines: 1,
                                        overflow: TextOverflow.ellipsis,
                                      ),
                                    ),
                                    const SizedBox(width: 8),
                                    Text(
                                      _formatMemoryKB(proc.kb),
                                      style: mono(Fs.small, color: c.faint),
                                    ),
                                  ],
                                ),
                              ),
                          ],
                        ),
                      ),
                  ],
                ),
              ),
            ),
          );
        }
      }
      if (mem.truncated) {
        rows.add(
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 6, 14, 12),
            child: Text(
              l.hostResTruncated,
              style: TextStyle(
                fontSize: Fs.small,
                height: Lh.body,
                color: c.muted,
              ),
            ),
          ),
        );
      }
    }
    return Section(title: l.hostSectionResources, children: rows);
  }

  Widget _kvRow(
    PosseColors c,
    String label,
    String value, {
    bool monospace = false,
  }) {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
      child: Row(
        children: [
          Text(label, style: TextStyle(fontSize: Fs.body, color: c.muted)),
          const SizedBox(width: 12),
          Expanded(
            child: Text(
              value,
              textAlign: TextAlign.end,
              style: monospace
                  ? mono(Fs.small, color: c.text)
                  : TextStyle(fontSize: Fs.body, color: c.text),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
            ),
          ),
        ],
      ),
    );
  }

  Widget _noteBanner(PosseColors c, Color tone, String text) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(14, 8, 14, 12),
      child: Container(
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(
          color: c.soft(tone),
          borderRadius: BorderRadius.circular(Rd.input),
        ),
        child: Text(
          text,
          style: TextStyle(fontSize: Fs.small, height: Lh.body, color: c.text),
        ),
      ),
    );
  }
}

String _formatPlatform(String platform, String arch) {
  final os = switch (platform) {
    'darwin' => 'macOS',
    'linux' => 'Linux',
    _ => platform,
  };
  if (os.isEmpty) return '';
  if (arch.isEmpty) return os;
  return '$os ($arch)';
}

String _formatMemoryKB(int kb) {
  if (kb >= 1024 * 1024) {
    return '${(kb / (1024 * 1024)).toStringAsFixed(1)} GB';
  }
  if (kb >= 1024) {
    return '${(kb / 1024).toStringAsFixed(1)} MB';
  }
  return '$kb KB';
}

String _formatUptime(AppLocalizations l, Duration d) {
  if (d.inMinutes < 1) return l.hostUptimeSeconds;
  final days = d.inDays;
  final hours = d.inHours % 24;
  final mins = d.inMinutes % 60;
  if (days > 0) return l.hostUptimeDays(days, hours);
  if (hours > 0) return l.hostUptimeHours(hours, mins);
  return l.hostUptimeMins(mins);
}

String _autostartStatusLabel(AppLocalizations l, HostAutostartData st) {
  if (!st.installed) return l.hostAutostartOff;
  if (!st.enabled) return l.hostAutostartOff;
  if (st.active) {
    return st.kind == 'systemd'
        ? l.hostAutostartTimerActive
        : l.hostAutostartLoaded;
  }
  return l.hostAutostartInstalledPending;
}
