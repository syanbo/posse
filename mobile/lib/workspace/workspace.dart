import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../app_state.dart';
import '../chat/chat.dart';
import '../git/git.dart';
import '../pair/scan.dart';
import '../protocol/rpc.dart';
import '../terminal/terminal.dart';
import 'models.dart';

class PairScreen extends StatefulWidget {
  const PairScreen({super.key, required this.model});

  final HiveAppModel model;

  @override
  State<PairScreen> createState() => _PairScreenState();
}

class _PairScreenState extends State<PairScreen> {
  final _code = TextEditingController();
  String? _cameraNote;

  @override
  void dispose() {
    _code.dispose();
    super.dispose();
  }

  Future<void> _scan() async {
    bool granted = false;
    try {
      granted = await qrChannel.invokeMethod<bool>('authorize') ?? false;
    } on MissingPluginException {
      return;
    } on PlatformException {
      if (!mounted) return;
      setState(() => _cameraNote = '打不开相机，可以把配对码贴进来。');
      return;
    }
    if (!mounted) return;
    if (!granted) {
      setState(() => _cameraNote = '需要相机权限才能扫码，也可以把配对码贴进来。');
      return;
    }
    final pick = await Navigator.of(context)
        .push<ScanPick>(MaterialPageRoute(builder: (_) => const QrScanPage()));
    if (!mounted || pick == null) return;
    if (pick.failed || pick.code == null) {
      setState(() => _cameraNote = '打不开相机，可以把配对码贴进来。');
      return;
    }
    setState(() => _cameraNote = null);
    widget.model.connectCode(pick.code!);
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    return Scaffold(
      appBar: AppBar(title: const Text('连接 Hive')),
      body: ListView(
        padding: const EdgeInsets.all(20),
        children: [
          const Text('把桌面端配对码贴进来。手机和电脑要在同一个网络，或都能访问配对码里的地址。'),
          if (Platform.isIOS) ...[
            const SizedBox(height: 16),
            FilledButton(
              onPressed: model.connecting ? null : _scan,
              child: const Text('扫二维码'),
            ),
            if (_cameraNote != null) ...[
              const SizedBox(height: 12),
              Text(
                _cameraNote!,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ],
          ],
          const SizedBox(height: 16),
          TextField(
            controller: _code,
            minLines: 3,
            maxLines: 6,
            decoration: const InputDecoration(
              hintText: 'orca://pair?code=…',
              border: OutlineInputBorder(),
            ),
          ),
          const SizedBox(height: 12),
          FilledButton(
            onPressed: model.connecting
                ? null
                : () => model.connectCode(_code.text),
            child: Text(model.connecting ? '正在连接' : '连接'),
          ),
          if (model.error != null) ...[
            const SizedBox(height: 12),
            Text(
              model.error!,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ],
        ],
      ),
    );
  }
}

class WorkspaceScreen extends StatelessWidget {
  const WorkspaceScreen({super.key, required this.model});

  final HiveAppModel model;

  @override
  Widget build(BuildContext context) {
    final groups = <String, List<WorktreeRow>>{};
    for (final row in model.worktrees) {
      groups
          .putIfAbsent(row.repo.isEmpty ? '工作区' : row.repo, () => [])
          .add(row);
    }
    final names = groups.keys.toList();
    return Scaffold(
      appBar: AppBar(
        title: const Text('Hive'),
        actions: [
          IconButton(
            onPressed: model.refreshWorktrees,
            icon: const Icon(Icons.refresh),
          ),
          IconButton(onPressed: model.forget, icon: const Icon(Icons.link_off)),
        ],
      ),
      body: model.worktrees.isEmpty
          ? Center(
              child: model.connecting
                  ? const CircularProgressIndicator()
                  : Padding(
                      padding: const EdgeInsets.all(24),
                      child: Text(
                        model.error ?? '还没有工作区',
                        textAlign: TextAlign.center,
                        style: model.error == null
                            ? null
                            : TextStyle(
                                color: Theme.of(context).colorScheme.error,
                              ),
                      ),
                    ),
            )
          : ListView(
              children: [
                if (model.error != null)
                  Padding(
                    padding: const EdgeInsets.all(12),
                    child: Text(
                      model.error!,
                      style: TextStyle(
                        color: Theme.of(context).colorScheme.error,
                      ),
                    ),
                  ),
                for (final name in names) ...[
                  Padding(
                    padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
                    child: Text(
                      name,
                      style: const TextStyle(fontWeight: FontWeight.w600),
                    ),
                  ),
                  for (final row in groups[name]!)
                    ListTile(
                      title: Text(row.name),
                      subtitle: Text(row.subtitle),
                      trailing: Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          IconButton(
                            tooltip: '改动',
                            icon: const Icon(Icons.difference_outlined),
                            onPressed: () {
                              final rpc = model.rpc;
                              if (rpc == null) return;
                              Navigator.of(context).push(
                                MaterialPageRoute<void>(
                                  builder: (_) => GitScreen(
                                    title: row.name,
                                    loadStatus: () =>
                                        loadGitStatus(rpc, row.id),
                                    loadDiff: (path) =>
                                        loadGitDiff(rpc, row.id, path),
                                  ),
                                ),
                              );
                            },
                          ),
                          Text(
                            statusLabel(row.status),
                            style: TextStyle(
                              color: row.status == 'permission'
                                  ? const Color(0xFFB45309)
                                  : null,
                            ),
                          ),
                        ],
                      ),
                      onTap: () {
                        Navigator.of(context).push(
                          MaterialPageRoute<void>(
                            builder: (_) =>
                                TabScreen(model: model, worktree: row),
                          ),
                        );
                      },
                    ),
                ],
              ],
            ),
    );
  }
}

class TabScreen extends StatefulWidget {
  const TabScreen({super.key, required this.model, required this.worktree});

  final HiveAppModel model;
  final WorktreeRow worktree;

  @override
  State<TabScreen> createState() => _TabScreenState();
}

class _TabScreenState extends State<TabScreen> {
  List<SessionTab> _tabs = const [];
  String? _error;
  RpcStream? _stream;
  PhoneRpc? _bound;
  var _gen = 0;
  Future<void> _gate = Future<void>.value();

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
    _schedule();
  }

  void _onModel() {
    if (widget.model.rpc != _bound) _schedule();
  }

  void _schedule() {
    _gate = _gate.then((_) => _open()).catchError((Object _) {});
  }

  Future<void> _open() async {
    final rpc = widget.model.rpc;
    if (!mounted || rpc == null || identical(rpc, _bound)) return;
    final gen = ++_gen;
    final previous = _bound;
    final previousId = _stream?.id;
    await _stream?.cancel();
    _stream = null;
    if (previous != null && previousId != null) {
      unawaited(
        previous
            .call('session.tabs.unsubscribe', {'subscriptionId': previousId})
            .catchError((Object _) => <String, Object?>{}),
      );
    }
    if (gen != _gen || !mounted) return;
    try {
      final stream = await rpc.subscribe('session.tabs.subscribe', {
        'worktree': widget.worktree.id,
      });
      if (gen != _gen || !mounted) {
        unawaited(
          rpc
              .call('session.tabs.unsubscribe', {'subscriptionId': stream.id})
              .catchError((Object _) => <String, Object?>{}),
        );
        await stream.cancel();
        return;
      }
      _bound = rpc;
      _stream = stream;
      stream.frames.listen((frame) {
        if (!mounted || gen != _gen) return;
        final tabs = tabsFromFrame(widget.worktree.id, frame);
        if (tabs == null) return;
        setState(() {
          _tabs = tabs;
          _error = null;
        });
      });
    } catch (error) {
      if (gen == _gen && mounted) setState(() => _error = '$error');
    }
  }

  @override
  void dispose() {
    _gen++;
    widget.model.removeListener(_onModel);
    final stream = _stream;
    final rpc = _bound;
    if (rpc != null && stream != null) {
      unawaited(
        rpc
            .call('session.tabs.unsubscribe', {'subscriptionId': stream.id})
            .catchError((Object _) => <String, Object?>{}),
      );
    }
    unawaited(stream?.cancel());
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(widget.worktree.name)),
      body: _error != null
          ? Center(child: Text(_error!))
          : ListView(
              children: [
                if (_tabs.isEmpty) const ListTile(title: Text('没有打开的终端')),
                for (final tab in _tabs)
                  ListTile(
                    title: Text(tab.title),
                    subtitle: Text(tab.eligible ? (tab.agent ?? '') : '终端'),
                    trailing: tab.state == 'waiting'
                        ? const Text(
                            '待处理',
                            style: TextStyle(color: Color(0xFFB45309)),
                          )
                        : null,
                    onTap: () {
                      final nav = Navigator.of(context);
                      nav.push(
                        MaterialPageRoute<void>(
                          builder: (_) => tab.eligible
                              ? ChatScreen(
                                  model: widget.model,
                                  tab: tab,
                                  onOpenTerminal: () {
                                    nav.push(
                                      MaterialPageRoute<void>(
                                        builder: (_) => TerminalScreen(
                                          model: widget.model,
                                          tab: tab,
                                        ),
                                      ),
                                    );
                                  },
                                )
                              : TerminalScreen(model: widget.model, tab: tab),
                        ),
                      );
                    },
                  ),
              ],
            ),
    );
  }
}
