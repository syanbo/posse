import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../app_state.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import 'media.dart' show mediaAgents;

bool hasTools(SessionTab tab) =>
    mediaAgents.contains(tab.agent) && (tab.sessionId?.isNotEmpty ?? false);

/// 用了几次的一个名字：技能、MCP 服务或命令行程序。
class ToolCount {
  const ToolCount(this.name, this.count, {this.source = ''});

  final String name;
  final int count;

  /// 命令行程序的来源：homebrew、system，其余为空。
  final String source;
}

/// 一次调用（电脑的 history.UsageEvent）：技能、MCP 工具、命令行程序三者之一。
class ToolEvent {
  const ToolEvent({
    this.at,
    this.skill = '',
    this.server = '',
    this.tool = '',
    this.cli = '',
    this.source = '',
    this.command = '',
  });

  factory ToolEvent.fromJson(Map<String, Object?> j) => ToolEvent(
    at: DateTime.tryParse(j['at'] as String? ?? '')?.toLocal(),
    skill: j['skill'] as String? ?? '',
    server: j['server'] as String? ?? '',
    tool: j['tool'] as String? ?? '',
    cli: j['cli'] as String? ?? '',
    source: j['source'] as String? ?? '',
    command: j['command'] as String? ?? '',
  );

  /// grok、cursor 的会话文件没有时间。
  final DateTime? at;
  final String skill;
  final String server;
  final String tool;
  final String cli;
  final String source;

  /// 命令行程序所在的那条命令，电脑截到 300 个字符。
  final String command;

  bool same(ToolEvent o) =>
      skill == o.skill && server == o.server && tool == o.tool && cli == o.cli;
}

/// `session.tools` 的结果。
class ToolUsage {
  const ToolUsage({
    this.found = false,
    this.skills = const [],
    this.mcp = const [],
    this.cli = const [],
    this.events = const [],
  });

  factory ToolUsage.fromJson(Map<String, Object?> j) {
    List<Map<String, Object?>> rows(Object? v) => [
      for (final e in (v as List?) ?? const [])
        if (e is Map) e.cast<String, Object?>(),
    ];
    int n(Object? v) => (v as num?)?.toInt() ?? 0;
    return ToolUsage(
      found: j['found'] == true,
      skills: [
        for (final r in rows(j['skills']))
          ToolCount(r['name'] as String? ?? '', n(r['count'])),
      ],
      mcp: [
        for (final r in rows(j['mcp']))
          ToolCount(r['server'] as String? ?? '', n(r['count'])),
      ],
      cli: [
        for (final r in rows(j['cli']))
          ToolCount(
            r['name'] as String? ?? '',
            n(r['count']),
            source: r['source'] as String? ?? '',
          ),
      ],
      events: [for (final r in rows(j['events'])) ToolEvent.fromJson(r)],
    );
  }

  final bool found;
  final List<ToolCount> skills;
  final List<ToolCount> mcp;
  final List<ToolCount> cli;
  final List<ToolEvent> events;
}

/// 时间线上的一项：连着几次调用同一个工具合成一项，[event] 是最近的一次。
class ToolStep {
  ToolStep(this.event, this.first);

  ToolEvent event;
  final DateTime? first;
  var count = 1;
}

/// 把逐次记录合并成时间线，新的在前；[skillsOnly] 时跳过命令行程序。同桌面的 usageSteps。
List<ToolStep> toolSteps(List<ToolEvent> events, {bool skillsOnly = false}) {
  final steps = <ToolStep>[];
  for (final e in events) {
    if (skillsOnly && e.cli.isNotEmpty) continue;
    if (steps.isNotEmpty && steps.last.event.same(e)) {
      steps.last
        ..event = e
        ..count += 1;
      continue;
    }
    steps.add(ToolStep(e, e.at));
  }
  return steps.reversed.toList();
}

/// 命令行程序分组的顺序和名字，同桌面。
const cliSources = [
  ('homebrew', 'Homebrew'),
  ('', '其他命令行'),
  ('system', '系统自带'),
];

String cliSourceLabel(String source) =>
    cliSources.firstWhere((s) => s.$1 == source, orElse: () => ('', '')).$2;

/// 只看技能和 MCP（命令行程序常是几十个 git、adb，把技能挤得看不见）。和桌面端各记各的，存在手机上。
final toolsSkillsOnly = ValueNotifier<bool>(false);
const _skillsOnlyKey = 'tools.skillsOnly';

Future<void> _loadSkillsOnly() async {
  final prefs = await SharedPreferences.getInstance();
  toolsSkillsOnly.value = prefs.getBool(_skillsOnlyKey) ?? false;
}

Future<void> _setSkillsOnly(bool v) async {
  toolsSkillsOnly.value = v;
  final prefs = await SharedPreferences.getInstance();
  await prefs.setBool(_skillsOnlyKey, v);
}

/// 技能是琥珀色、MCP 是蓝色、命令行程序是绿色，同桌面面板（MCP、命令行用桌面的数据色）。
Color _mcpColor(PosseColors c) =>
    c.dark ? const Color(0xFF7AA2F7) : const Color(0xFF3B6FD9);
Color _cliColor(PosseColors c) =>
    c.dark ? const Color(0xFF5FBFA0) : const Color(0xFF21866A);

/// 会话的"技能与工具"：用到的技能、MCP 服务和命令行程序各几次，下面是时间线。
/// 开着时每 3 秒重读一次（同桌面面板）。点名字复制。
class ToolsScreen extends StatefulWidget {
  const ToolsScreen({super.key, required this.model, required this.tab});

  final PosseAppModel model;
  final SessionTab tab;

  @override
  State<ToolsScreen> createState() => _ToolsScreenState();
}

class _ToolsScreenState extends State<ToolsScreen> {
  ToolUsage? _usage;
  String? _error;
  Timer? _timer;
  var _busy = false;

  @override
  void initState() {
    super.initState();
    unawaited(_loadSkillsOnly());
    _refresh();
    _timer = Timer.periodic(const Duration(seconds: 3), (_) => _refresh());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _refresh() async {
    final rpc = widget.model.rpc;
    if (_busy || rpc == null) return;
    _busy = true;
    final tab = widget.tab;
    try {
      final res = await rpc.call('session.tools', {
        'agent': tab.agent,
        'sessionId': tab.sessionId,
        if (tab.transcriptPath != null) 'transcriptPath': tab.transcriptPath,
      });
      if (mounted && _usage == null) await routeSettled(context);
      if (mounted) {
        setState(() {
          _usage = ToolUsage.fromJson(res);
          _error = null;
        });
      }
    } on RpcException catch (e) {
      if (e.code == 'method_not_found') {
        _timer?.cancel();
        if (mounted) {
          setState(() => _error = '电脑上的 posse 版本太旧，更新后才能在手机上看会话用到的技能和工具');
        }
      } else if (mounted && _usage == null) {
        setState(() => _error = friendlyError(e));
      }
    } catch (e) {
      // 读过一次之后断线不打扰，下一轮再读
      if (mounted && _usage == null) setState(() => _error = friendlyError(e));
    } finally {
      _busy = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(titleSpacing: 0, title: const Text('技能与工具')),
      body: ValueListenableBuilder(
        valueListenable: toolsSkillsOnly,
        builder: (context, skillsOnly, _) => _body(context, skillsOnly),
      ),
    );
  }

  Widget _body(BuildContext context, bool skillsOnly) {
    final u = _usage;
    if (u == null) {
      if (_error != null) {
        return EmptyState(
          icon: Icons.error_outline_rounded,
          text: _error!,
          error: true,
        );
      }
      return const Center(child: PosseLoader());
    }
    if (!u.found) {
      return const EmptyState(
        icon: Icons.extension_outlined,
        text: '还没找到会话文件，agent 开始对话后会出现',
      );
    }
    if (u.skills.isEmpty && u.mcp.isEmpty && u.cli.isEmpty) {
      return const EmptyState(
        icon: Icons.extension_outlined,
        text: '这个会话还没用到技能、MCP 和命令行工具',
      );
    }
    return ToolsView(usage: u, skillsOnly: skillsOnly);
  }
}

/// 页面的内容，单独拿出来好测：筛选、汇总、时间线。
class ToolsView extends StatelessWidget {
  const ToolsView({super.key, required this.usage, required this.skillsOnly});

  final ToolUsage usage;
  final bool skillsOnly;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final u = usage;
    final steps = toolSteps(u.events, skillsOnly: skillsOnly);
    final noSkills = u.skills.isEmpty && u.mcp.isEmpty;
    return CustomScrollView(
      slivers: [
        SliverPadding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
          sliver: SliverList.list(
            children: [
              Segmented(
                labels: const ['全部', '技能和 MCP'],
                selected: skillsOnly ? 1 : 0,
                onChanged: (i) => _setSkillsOnly(i == 1),
              ),
              if (skillsOnly && noSkills)
                Padding(
                  padding: const EdgeInsets.only(top: 24),
                  child: Text(
                    '这个会话还没用到技能和 MCP',
                    textAlign: TextAlign.center,
                    style: TextStyle(fontSize: 14, color: c.muted),
                  ),
                ),
              if (!noSkills) ...[
                const SizedBox(height: 16),
                Wrap(
                  spacing: 6,
                  runSpacing: 6,
                  children: [
                    for (final s in u.skills)
                      _Chip(
                        icon: Icons.auto_awesome_outlined,
                        color: c.honey,
                        item: s,
                      ),
                    for (final m in u.mcp)
                      _Chip(
                        icon: Icons.extension_outlined,
                        color: _mcpColor(c),
                        item: m,
                      ),
                  ],
                ),
              ],
              if (!skillsOnly)
                for (final (source, label) in cliSources)
                  if (u.cli.any((it) => it.source == source)) ...[
                    _header(c, label),
                    Wrap(
                      spacing: 6,
                      runSpacing: 6,
                      children: [
                        for (final it in u.cli)
                          if (it.source == source)
                            _Chip(
                              icon: Icons.terminal_rounded,
                              color: _cliColor(c),
                              item: it,
                            ),
                      ],
                    ),
                  ],
              if (steps.isNotEmpty) _header(c, '时间线'),
            ],
          ),
        ),
        SliverPadding(
          padding: const EdgeInsets.symmetric(horizontal: 16),
          sliver: SliverList.builder(
            itemCount: steps.length,
            itemBuilder: (context, i) =>
                _StepRow(step: steps[i], last: i == steps.length - 1),
          ),
        ),
        const SliverSafeArea(
          top: false,
          sliver: SliverToBoxAdapter(child: SizedBox(height: 24)),
        ),
      ],
    );
  }

  Widget _header(PosseColors c, String title) => Padding(
    padding: const EdgeInsets.fromLTRB(0, 20, 0, 10),
    child: Text(
      title,
      style: TextStyle(
        fontSize: Fs.meta,
        fontWeight: FontWeight.w600,
        color: c.muted,
      ),
    ),
  );
}

/// 名字复制到剪贴板：常要拿去搜配置、写进提示里。
void _copy(BuildContext context, String name) {
  Clipboard.setData(ClipboardData(text: name));
  ScaffoldMessenger.of(context)
    ..hideCurrentSnackBar()
    ..showSnackBar(SnackBar(content: Text('已复制 $name')));
}

/// 汇总里的一个小标签：图标、名字、次数，点一下复制名字。
class _Chip extends StatelessWidget {
  const _Chip({required this.icon, required this.color, required this.item});

  final IconData icon;
  final Color color;
  final ToolCount item;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    return Material(
      color: c.fill,
      shape: const StadiumBorder(),
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: () => _copy(context, item.name),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(8, 6, 10, 6),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(icon, size: 14, color: color),
              const SizedBox(width: 5),
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 200),
                child: Text(
                  item.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: mono(Fs.caption, color: c.text),
                ),
              ),
              const SizedBox(width: 5),
              Text(
                '×${item.count}',
                style: TextStyle(
                  fontSize: Fs.caption,
                  color: c.muted,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

String _hm(DateTime t) =>
    '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}';

/// 时间线上的一项：左边圆点和连到下一项的线，右边名字、次数、时间，下面是所属服务或来源。
/// 点一下复制名字；命令行程序长按看最近一次的完整命令。
class _StepRow extends StatelessWidget {
  const _StepRow({required this.step, required this.last});

  final ToolStep step;
  final bool last;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final e = step.event;
    final (color, kind, name) = e.skill.isNotEmpty
        ? (c.honey, '技能', e.skill)
        : e.cli.isNotEmpty
        ? (_cliColor(c), cliSourceLabel(e.source), e.cli)
        : (_mcpColor(c), e.server, e.tool);
    return InkWell(
      onTap: () => _copy(context, name),
      onLongPress: e.command.isEmpty ? null : () => _showCommand(context, e),
      child: IntrinsicHeight(
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            SizedBox(
              width: 12,
              child: CustomPaint(
                painter: _RailPainter(color: color, line: c.line, last: last),
              ),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: Padding(
                padding: const EdgeInsets.only(bottom: 16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        // 名字和次数占满剩下的宽，时间靠右；直接放 Spacer 会和名字的 Flexible 平分
                        Expanded(
                          child: Row(
                            children: [
                              Flexible(
                                child: Text(
                                  name,
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                  style: mono(Fs.meta, color: c.text),
                                ),
                              ),
                              if (step.count > 1) ...[
                                const SizedBox(width: 6),
                                Text('×${step.count}', style: _num(c)),
                              ],
                            ],
                          ),
                        ),
                        if (e.at != null) Text(_hm(e.at!), style: _num(c)),
                      ],
                    ),
                    const SizedBox(height: 2),
                    Text(
                      kind,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(fontSize: Fs.caption, color: c.muted),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  TextStyle _num(PosseColors c) => TextStyle(
    fontSize: Fs.caption,
    color: c.muted,
    fontFeatures: const [FontFeature.tabularFigures()],
  );

  void _showCommand(BuildContext context, ToolEvent e) {
    showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      builder: (sheet) {
        final c = PosseColors.of(sheet);
        return SafeArea(
          top: false,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(20, 0, 20, 20),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: c.code,
                    borderRadius: BorderRadius.circular(Rd.code),
                  ),
                  child: SelectableText(
                    e.command,
                    style: mono(Fs.meta, color: c.text, height: 1.5),
                  ),
                ),
                const SizedBox(height: 12),
                FilledButton(
                  onPressed: () {
                    Navigator.of(sheet).pop();
                    _copy(context, e.command);
                  },
                  child: const Text('复制命令'),
                ),
              ],
            ),
          ),
        );
      },
    );
  }
}

class _RailPainter extends CustomPainter {
  const _RailPainter({
    required this.color,
    required this.line,
    required this.last,
  });

  final Color color;
  final Color line;
  final bool last;

  @override
  void paint(Canvas canvas, Size size) {
    final cx = size.width / 2;
    if (!last) {
      canvas.drawRect(
        Rect.fromLTWH(cx - 0.75, 16, 1.5, size.height - 16),
        Paint()..color = line,
      );
    }
    canvas.drawCircle(Offset(cx, 9), 4, Paint()..color = color);
  }

  @override
  bool shouldRepaint(_RailPainter old) =>
      old.color != color || old.line != line || old.last != last;
}
