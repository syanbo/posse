import 'package:flutter/material.dart';

import '../protocol/rpc.dart';
import 'status.dart';

String gitWorktree(String id) => id.startsWith('id:') ? id : 'id:$id';

Future<GitStatusView> loadGitStatus(PhoneRpc rpc, String worktree) {
  return rpc
      .call('git.status', {'worktree': gitWorktree(worktree)})
      .then(parseGitStatus);
}

Future<GitDiffView> loadGitDiff(PhoneRpc rpc, String worktree, String path) {
  return rpc
      .call('git.diff', {'worktree': gitWorktree(worktree), 'path': path})
      .then(parseGitDiff);
}

class GitScreen extends StatefulWidget {
  const GitScreen({
    super.key,
    required this.title,
    required this.loadStatus,
    required this.loadDiff,
  });

  final String title;
  final Future<GitStatusView> Function() loadStatus;
  final Future<GitDiffView> Function(String path) loadDiff;

  @override
  State<GitScreen> createState() => _GitScreenState();
}

class _GitScreenState extends State<GitScreen> {
  GitStatusView? _status;
  String? _error;
  var _loading = true;

  @override
  void initState() {
    super.initState();
    _reload();
  }

  Future<void> _reload() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final status = await widget.loadStatus();
      if (!mounted) return;
      setState(() {
        _status = status;
        _loading = false;
      });
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _error = '$error';
        _loading = false;
      });
    }
  }

  Future<void> _open(GitFileChange file) async {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      builder: (context) {
        return _DiffDrawer(
          path: file.path,
          load: () => widget.loadDiff(file.path),
        );
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    final status = _status;
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.title),
        actions: [
          IconButton(onPressed: _reload, icon: const Icon(Icons.refresh)),
        ],
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _error != null
          ? Center(
              child: Padding(
                padding: const EdgeInsets.all(24),
                child: Text(
                  _error!,
                  textAlign: TextAlign.center,
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ),
            )
          : Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
                  child: Text(
                    status?.headline ?? '',
                    style: const TextStyle(fontWeight: FontWeight.w600),
                  ),
                ),
                Expanded(
                  child: status == null || status.files.isEmpty
                      ? const Center(child: Text('没有改动'))
                      : ListView.builder(
                          itemCount: status.files.length,
                          itemBuilder: (context, index) {
                            final file = status.files[index];
                            return ListTile(
                              title: Text(file.path),
                              subtitle: file.stats.isEmpty
                                  ? null
                                  : Text(file.stats),
                              onTap: () => _open(file),
                            );
                          },
                        ),
                ),
              ],
            ),
    );
  }
}

class _DiffDrawer extends StatefulWidget {
  const _DiffDrawer({required this.path, required this.load});

  final String path;
  final Future<GitDiffView> Function() load;

  @override
  State<_DiffDrawer> createState() => _DiffDrawerState();
}

class _DiffDrawerState extends State<_DiffDrawer> {
  GitDiffView? _diff;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final diff = await widget.load();
      if (mounted) setState(() => _diff = diff);
    } catch (error) {
      if (mounted) setState(() => _error = '$error');
    }
  }

  @override
  Widget build(BuildContext context) {
    final height = MediaQuery.sizeOf(context).height * 0.7;
    final diff = _diff;
    return SafeArea(
      child: SizedBox(
        height: height,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: Text(
                widget.path,
                style: const TextStyle(fontWeight: FontWeight.w600),
              ),
            ),
            if (diff?.truncated == true)
              const Padding(
                padding: EdgeInsets.symmetric(horizontal: 16),
                child: Text('差异太长，只显示前面一部分'),
              ),
            Expanded(
              child: _error != null
                  ? Padding(
                      padding: const EdgeInsets.all(16),
                      child: Text(
                        _error!,
                        style: TextStyle(
                          color: Theme.of(context).colorScheme.error,
                        ),
                      ),
                    )
                  : diff == null
                  ? const Center(child: CircularProgressIndicator())
                  : Scrollbar(
                      child: SingleChildScrollView(
                        padding: const EdgeInsets.fromLTRB(16, 8, 16, 24),
                        child: SelectableText(
                          diff.diff.isEmpty ? '没有差异' : diff.diff,
                          style: const TextStyle(
                            fontFamily: 'monospace',
                            fontSize: 12,
                            height: 1.35,
                          ),
                        ),
                      ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}
