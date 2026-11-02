import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:url_launcher/url_launcher.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../protocol/rpc.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';
import '../workspace/models.dart';
import 'media.dart' show mediaAgents;

bool hasTools(SessionTab tab) =>
    mediaAgents.contains(tab.agent) && (tab.sessionId?.isNotEmpty ?? false);

/// 用了几次的一个名字：技能、MCP 服务、命令行程序或网站（域名）。
class ToolCount {
  const ToolCount(this.name, this.count, {this.source = ''});

  final String name;
  final int count;

  /// 命令行程序的来源：homebrew、system，其余为空。
  final String source;
}

/// 一次调用（电脑的 history.UsageEvent）：技能、MCP 工具、命令行程序、访问网址、网页搜索之一。
class ToolEvent {
  const ToolEvent({
    this.at,
    this.skill = '',
    this.server = '',
    this.tool = '',
    this.cli = '',
    this.source = '',
    this.command = '',
    this.url = '',
    this.query = '',
    this.via = '',
    this.failed = false,
  });

  factory ToolEvent.fromJson(Map<String, Object?> j) => ToolEvent(
    at: DateTime.tryParse(j['at'] as String? ?? '')?.toLocal(),
    skill: j['skill'] as String? ?? '',
    server: j['server'] as String? ?? '',
    tool: j['tool'] as String? ?? '',
    cli: j['cli'] as String? ?? '',
    source: j['source'] as String? ?? '',
    command: j['command'] as String? ?? '',
    url: j['url'] as String? ?? '',
    query: j['query'] as String? ?? '',
    via: j['via'] as String? ?? '',
    failed: j['failed'] as bool? ?? false,
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

  /// 访问的网址（电脑截到 300 个字符）和网页搜索的搜索词。
  final String url;
  final String query;

  /// 网址、搜索经什么访问：工具名、程序名，用户自己发的是 user。
  final String via;

  /// MCP 调用出错或被拒，电脑没算进次数。
  final bool failed;

  bool get isWeb => url.isNotEmpty || query.isNotEmpty;

  /// 这个版本认得的记录：电脑以后加了新类型，旧 App 跳过，不画成没名字的一行。
  bool get known =>
      skill.isNotEmpty || tool.isNotEmpty || cli.isNotEmpty || isWeb;

  bool same(ToolEvent o) =>
      skill == o.skill &&
      server == o.server &&
      tool == o.tool &&
      cli == o.cli &&
      url == o.url &&
      query == o.query &&
      via == o.via &&
      failed == o.failed;
}

/// `session.tools` 的结果。
class ToolUsage {
  const ToolUsage({
    this.found = false,
    this.skills = const [],
    this.mcp = const [],
    this.cli = const [],
    this.web = const [],
    this.searches = 0,
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
      web: [
        for (final r in rows(j['web']))
          ToolCount(r['host'] as String? ?? '', n(r['count'])),
      ],
      searches: n(j['searches']),
      events: [for (final r in rows(j['events'])) ToolEvent.fromJson(r)],
    );
  }

  final bool found;
  final List<ToolCount> skills;
  final List<ToolCount> mcp;
  final List<ToolCount> cli;

  /// 访问过的网站（按域名）和网页搜索的次数，旧版电脑没有。
  final List<ToolCount> web;
  final int searches;
  final List<ToolEvent> events;
}

/// 时间线上的一项：连着几次调用同一个工具合成一项，[event] 是最近的一次。
class ToolStep {
  ToolStep(this.event, this.first);

  ToolEvent event;
  final DateTime? first;
  var count = 1;
}

/// 页面的筛选：全部、只看技能和 MCP、只看网址。
enum ToolsFilter { all, skills, web }

/// 把逐次记录合并成时间线，新的在前；按 [filter] 跳过别的类别。同桌面的 usageSteps。
List<ToolStep> toolSteps(
  List<ToolEvent> events, {
  ToolsFilter filter = ToolsFilter.all,
}) {
  final steps = <ToolStep>[];
  for (final e in events) {
    if (!e.known ||
        filter == ToolsFilter.skills && (e.cli.isNotEmpty || e.isWeb) ||
        filter == ToolsFilter.web && !e.isWeb) {
      continue;
    }
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

/// 筛选（命令行程序常是几十个 git、adb，把技能挤得看不见）。和桌面端各记各的，存在手机上；
/// 旧版只记了是否只看技能和 MCP（tools.skillsOnly）。
final toolsFilter = ValueNotifier<ToolsFilter>(ToolsFilter.all);
const _filterKey = 'tools.filter';
const _skillsOnlyKey = 'tools.skillsOnly';

Future<void> _loadFilter() async {
  final prefs = await SharedPreferences.getInstance();
  final i = prefs.getInt(_filterKey);
  toolsFilter.value = i != null && i >= 0 && i < ToolsFilter.values.length
      ? ToolsFilter.values[i]
      : (prefs.getBool(_skillsOnlyKey) ?? false)
      ? ToolsFilter.skills
      : ToolsFilter.all;
}

Future<void> _setFilter(ToolsFilter v) async {
  toolsFilter.value = v;
  final prefs = await SharedPreferences.getInstance();
  await prefs.setInt(_filterKey, v.index);
}

/// 网址拆成显示用的域名（去掉 www.）和其余部分，同桌面的 webSite。
(String, String) webSite(String raw) {
  final u = Uri.tryParse(raw);
  if (u == null || u.host.isEmpty) return (raw, '');
  final host = u.host.startsWith('www.') ? u.host.substring(4) : u.host;
  var rest = u.path;
  if (u.hasQuery) rest += '?${u.query}';
  return (host, rest == '/' ? '' : rest);
}

/// 网址的来源说明：用户发的写"你发的"，其余是工具名或程序名。
String webVia(String via) => via == 'user' ? '你发的' : via;

/// 会话的"技能与工具"：用到的技能、MCP 服务、命令行程序和网址各几次，下面是时间线。
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
    unawaited(_loadFilter());
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
        setState(() => _error = friendlyError(e, context.l10n));
      }
    } catch (e) {
      // 读过一次之后断线不打扰，下一轮再读
      if (mounted && _usage == null) {
        setState(() => _error = friendlyError(e, context.l10n));
      }
    } finally {
      _busy = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(titleSpacing: 0, title: const Text('技能与工具')),
      body: ValueListenableBuilder(
        valueListenable: toolsFilter,
        builder: (context, filter, _) => _body(context, filter),
      ),
    );
  }

  Widget _body(BuildContext context, ToolsFilter filter) {
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
        icon: Icons.extension_rounded,
        text: '还没找到会话文件，agent 开始对话后会出现',
      );
    }
    if (u.skills.isEmpty &&
        u.mcp.isEmpty &&
        u.cli.isEmpty &&
        u.web.isEmpty &&
        u.searches == 0) {
      return const EmptyState(
        icon: Icons.extension_rounded,
        text: '这个会话还没用到技能、MCP、命令行工具和网址',
      );
    }
    return ToolsView(usage: u, filter: filter);
  }
}

/// 页面的内容，单独拿出来好测：筛选、汇总、时间线。
class ToolsView extends StatelessWidget {
  const ToolsView({
    super.key,
    required this.usage,
    this.filter = ToolsFilter.all,
  });

  final ToolUsage usage;
  final ToolsFilter filter;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final u = usage;
    final steps = toolSteps(u.events, filter: filter);
    final noSkills = u.skills.isEmpty && u.mcp.isEmpty;
    final noWeb = u.web.isEmpty && u.searches == 0;
    final empty = switch (filter) {
      ToolsFilter.skills when noSkills => '这个会话还没用到技能和 MCP',
      ToolsFilter.web when noWeb => '这个会话还没访问网址',
      _ => null,
    };
    return CustomScrollView(
      slivers: [
        SliverPadding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
          sliver: SliverList.list(
            children: [
              Segmented(
                labels: const ['全部', '技能和 MCP', '网址'],
                selected: filter.index,
                onChanged: (i) => _setFilter(ToolsFilter.values[i]),
              ),
              if (empty != null)
                Padding(
                  padding: const EdgeInsets.only(top: 24),
                  child: Text(
                    empty,
                    textAlign: TextAlign.center,
                    style: TextStyle(fontSize: Fs.body, color: c.muted),
                  ),
                ),
              if (filter != ToolsFilter.web && !noSkills) ...[
                const SizedBox(height: 16),
                // 技能琥珀色、MCP 蓝色、命令行程序绿色、网址紫色，同桌面面板的数据色
                Wrap(
                  spacing: 6,
                  runSpacing: 6,
                  children: [
                    for (final s in u.skills)
                      _Chip(
                        icon: Icons.auto_awesome_rounded,
                        color: c.honey,
                        item: s,
                      ),
                    for (final m in u.mcp)
                      _Chip(
                        icon: Icons.extension_rounded,
                        color: c.sky,
                        item: m,
                      ),
                  ],
                ),
              ],
              if (filter == ToolsFilter.all)
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
                              color: c.teal,
                              item: it,
                            ),
                      ],
                    ),
                  ],
              if (filter != ToolsFilter.skills && !noWeb) ...[
                _header(c, u.searches > 0 ? '网址  搜索 ${u.searches} 次' : '网址'),
                Wrap(
                  spacing: 6,
                  runSpacing: 6,
                  children: [
                    for (final it in u.web)
                      _Chip(
                        icon: Icons.language_rounded,
                        color: c.plum,
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
      style: TextStyle(fontSize: Fs.small, fontWeight: Fw.bold, color: c.muted),
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
              Icon(icon, size: Ic.inline, color: color),
              const SizedBox(width: 4),
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 200),
                child: Text(
                  item.name,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: mono(Fs.small, color: c.text),
                ),
              ),
              const SizedBox(width: 4),
              Text(
                '×${item.count}',
                style: TextStyle(
                  fontSize: Fs.small,
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
/// 点一下复制名字（网址复制完整网址）；命令行程序长按看最近一次的完整命令，网址长按看完整网址、用浏览器打开。
class _StepRow extends StatelessWidget {
  const _StepRow({required this.step, required this.last});

  final ToolStep step;
  final bool last;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final e = step.event;
    final (color, kind, name) = switch (e) {
      _ when e.skill.isNotEmpty => (c.honey, '技能', e.skill),
      _ when e.cli.isNotEmpty => (c.teal, cliSourceLabel(e.source), e.cli),
      _ when e.url.isNotEmpty => () {
        final (host, rest) = webSite(e.url);
        final via = webVia(e.via);
        return (c.plum, rest.isEmpty ? via : '$via · $rest', host);
      }(),
      _ when e.query.isNotEmpty => (c.plum, '搜索 · ${webVia(e.via)}', e.query),
      _ => (c.sky, e.failed ? '${e.server} · 失败' : e.server, e.tool),
    };
    return InkWell(
      onTap: () => _copy(context, e.url.isNotEmpty ? e.url : name),
      onLongPress: e.url.isNotEmpty
          ? () => _showDetail(context, e.url, '复制网址', url: e.url)
          : e.command.isEmpty
          ? null
          : () => _showDetail(context, e.command, '复制命令'),
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
                                  style: mono(Fs.small, color: c.text),
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
                      style: TextStyle(fontSize: Fs.small, color: c.muted),
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
    fontSize: Fs.small,
    color: c.muted,
    fontFeatures: const [FontFeature.tabularFigures()],
  );

  /// 底部弹出完整的命令或网址，下面是复制按钮；网址再加一个用浏览器打开。
  void _showDetail(
    BuildContext context,
    String text,
    String copyLabel, {
    String? url,
  }) {
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
                    text,
                    style: mono(Fs.small, color: c.text, height: Lh.body),
                  ),
                ),
                const SizedBox(height: 12),
                FilledButton(
                  onPressed: () {
                    Navigator.of(sheet).pop();
                    _copy(context, text);
                  },
                  child: Text(copyLabel),
                ),
                if (url != null) ...[
                  const SizedBox(height: 8),
                  OutlinedButton(
                    onPressed: () {
                      Navigator.of(sheet).pop();
                      final uri = Uri.tryParse(url);
                      if (uri != null) {
                        unawaited(
                          launchUrl(uri, mode: LaunchMode.externalApplication),
                        );
                      }
                    },
                    child: const Text('在浏览器中打开'),
                  ),
                ],
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
