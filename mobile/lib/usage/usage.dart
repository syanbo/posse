import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../app_state.dart';
import '../l10n/l10n.dart';
import '../ui/loading.dart';
import '../ui/theme.dart';
import '../ui/widgets.dart';

// 各 agent 的套餐用量（Posse 扩展 posse.usage.v1 的 usage.get）。电脑上的 Posse 只在 agent 运行时
// 顺带更新读数，手机这边定时取一次就够了。

DateTime? _ms(Object? v) {
  final n = (v as num?)?.toInt() ?? 0;
  return n > 0 ? DateTime.fromMillisecondsSinceEpoch(n) : null;
}

/// 一个限额窗口：用了多少、多长、什么时候重置。[name] 是分池（Claude 的 Fable、Cursor 的 Auto 和 API），
/// 空的是总额度；[at] 是分池自己的读数时间，和整家的不是同一份读数时才有。
class UsageWindow {
  const UsageWindow({
    required this.percent,
    this.minutes = 0,
    this.resetsAt,
    this.name = '',
    this.at,
  });

  final double percent;
  final int minutes;
  final DateTime? resetsAt;
  final String name;
  final DateTime? at;

  factory UsageWindow.fromJson(Map<String, Object?> j) => UsageWindow(
    percent: (j['percent'] as num?)?.toDouble() ?? 0,
    minutes: (j['minutes'] as num?)?.toInt() ?? 0,
    resetsAt: _ms(j['resetsAt']),
    name: j['name'] as String? ?? '',
    at: _ms(j['at']),
  );
}

/// 金额，单位美分；[limit] 为 0 是没有上限。
class UsageMoney {
  const UsageMoney({required this.used, this.limit = 0, this.at});

  final double used;
  final double limit;
  final DateTime? at;

  static UsageMoney? fromJson(Object? v) {
    if (v is! Map) return null;
    return UsageMoney(
      used: (v['used'] as num?)?.toDouble() ?? 0,
      limit: (v['limit'] as num?)?.toDouble() ?? 0,
      at: _ms(v['at']),
    );
  }
}

class AgentUsage {
  const AgentUsage({
    required this.agent,
    required this.windows,
    required this.updatedAt,
    this.plan = '',
    this.spend,
    this.onDemand,
    this.shares = const [],
    this.sharesAt,
    this.resets,
    this.resetsExpireAt,
    this.resetsAt,
  });

  final String agent;
  final String plan;
  final List<UsageWindow> windows;
  final DateTime updatedAt;

  /// 套餐内的金额（Cursor）
  final UsageMoney? spend;

  /// 超出套餐后按量付费的部分
  final UsageMoney? onDemand;

  /// 本周用量用在了哪（Claude）：名字和百分比
  final List<(String, double)> shares;
  final DateTime? sharesAt;

  /// 重置卡（Codex）：张数、最早一张的过期时间、读数时间
  final int? resets;
  final DateTime? resetsExpireAt;
  final DateTime? resetsAt;

  factory AgentUsage.fromJson(Map<String, Object?> j) {
    final shares = j['shares'] as Map?;
    final resets = j['resets'] as Map?;
    return AgentUsage(
      agent: j['agent'] as String? ?? '',
      plan: planName(j['plan'] as String? ?? ''),
      windows: [
        for (final w in (j['windows'] as List?) ?? const [])
          UsageWindow.fromJson((w as Map).cast<String, Object?>()),
      ],
      updatedAt: _ms(j['updatedAt']) ?? DateTime.fromMillisecondsSinceEpoch(0),
      spend: UsageMoney.fromJson(j['spend']),
      onDemand: UsageMoney.fromJson(j['onDemand']),
      shares: [
        for (final it in (shares?['items'] as List?) ?? const [])
          (
            (it as Map)['name'] as String? ?? '',
            (it['percent'] as num?)?.toDouble() ?? 0,
          ),
      ],
      sharesAt: _ms(shares?['at']),
      resets: (resets?['count'] as num?)?.toInt(),
      resetsExpireAt: _ms(resets?['expiresAt']),
      resetsAt: _ms(resets?['at']),
    );
  }

  /// 要显示的窗口，和桌面端 planWindows 一致：Cursor 有分池时照它的控制台只列两池，
  /// 总的百分比按 Auto 池折算，远低于两池，并排看着像算错了。
  List<UsageWindow> get shownWindows {
    if (agent != 'cursor') return windows;
    final pools = windows.where((w) => w.name.isNotEmpty).toList();
    return pools.isEmpty ? windows : pools;
  }

  /// 首页卡片代表这一家的窗口，和桌面状态栏一致：总额度里有 5 小时就是它；
  /// 只有分池时取用得最多的一池，否则是第一个。Claude 读数里没有 5 小时就是这个窗口里
  /// 还没用过，照样写 5 小时，不换成每周。
  UsageWindow get headWindow {
    final ws = shownWindows;
    for (final w in ws) {
      if (w.name.isEmpty && WindowKind.of(w.minutes) == WindowKind.fiveHours) {
        return w;
      }
    }
    if (agent == 'claude') return const UsageWindow(percent: 0, minutes: 300);
    if (ws.first.name.isEmpty) return ws.first;
    return ws.reduce((a, b) => b.percent > a.percent ? b : a);
  }
}

/// Cursor 两池在它控制台上的叫法：Auto 池含 Composer 和 Cursor Grok，API 池是其余模型。
String? _cursorPool(AppLocalizations l, String name) => switch (name) {
  'Auto' => l.usageCursorModels,
  'API' => l.usageOtherModels,
  _ => null,
};

/// 详情里窗口的名字：分池带上池名，"Fable 每周"；Cursor 的两池只写池名，重置时间在同一行。
String windowName(AppLocalizations l, UsageWindow w) {
  if (w.name.isEmpty) return windowLabel(l, w.minutes);
  return _cursorPool(l, w.name) ??
      l.usageNamedWindow(w.name, windowLabel(l, w.minutes));
}

String _money(double cents) => '\$${(cents / 100).toStringAsFixed(2)}';

/// planName 照各家网页的叫法把套餐名首字母大写（Codex 给的是 plus、pro）。新的常驻进程已经这样写，
/// 这里兜住还没升级的电脑。
String planName(String s) =>
    s.isEmpty ? s : s[0].toUpperCase() + s.substring(1);

/// 显示的套餐名：月费的 /mo（Cursor 给的原样）按界面语言写。
String planLabel(AppLocalizations l, String plan) =>
    plan.replaceFirst('/mo', l.usagePerMonth);

/// 详情里窗口下面的几行，和桌面端 planFacts 一致：套餐内用量的金额、按需用量、本周用量用在哪、重置卡。
/// 和整家的读数不是同一份的，后面写上是多久前的。
List<String> usageFacts(AppLocalizations l, AgentUsage u, DateTime now) {
  String stale(DateTime? at) =>
      at != null && u.updatedAt.difference(at) > const Duration(minutes: 10)
      ? l.usageStaleReading(agoText(at, now))
      : '';
  final out = <String>[];
  final sp = u.spend;
  if (sp != null && sp.limit > 0) {
    out.add(
      l.usageIncludedSpend(
        _money(sp.used),
        _money(sp.limit),
        '${(sp.used / sp.limit * 100).round()}%',
      ),
    );
  }
  final od = u.onDemand;
  if (od != null) {
    out.add(
      od.limit > 0
          ? l.usageOnDemandLimited(
              _money(od.used),
              _money(od.limit),
              stale(od.at),
            )
          : l.usageOnDemand(_money(od.used), stale(od.at)),
    );
  }
  if (u.shares.isNotEmpty) {
    final parts = [for (final (n, p) in u.shares) '$n ${p.round()}%'];
    out.add(l.usageShares(parts.join(l.usageListSeparator), stale(u.sharesAt)));
  }
  final rs = u.resets;
  if (rs != null) {
    final exp = u.resetsExpireAt;
    out.add(
      rs > 0 && exp != null
          ? l.usageResetCardsExpiring(rs, _monthDay(exp), stale(u.resetsAt))
          : l.usageResetCards(rs, stale(u.resetsAt)),
    );
  }
  return out;
}

// 日期格式留给第 7 步（intl 的 DateFormat）
String _monthDay(DateTime d) => '${d.month}月${d.day}日';

List<AgentUsage> parseUsages(Map<String, Object?> res) => [
  for (final u in (res['usages'] as List?) ?? const [])
    AgentUsage.fromJson((u as Map).cast<String, Object?>()),
];

/// 窗口的名字，和桌面端 windowLabel 一致。
String windowLabel(AppLocalizations l, int minutes) {
  const day = 24 * 60;
  switch (WindowKind.of(minutes)) {
    case WindowKind.period:
      return l.usageWindowPeriod;
    case WindowKind.fiveHours:
      return l.usageWindowFiveHours;
    case WindowKind.week:
      return l.usageWindowWeek;
    case WindowKind.month:
      return l.usageWindowMonth;
    case WindowKind.other:
  }
  if (minutes >= day) return l.usageWindowDays((minutes + day ~/ 2) ~/ day);
  return l.usageWindowHours((minutes + 30) ~/ 60);
}

/// 套餐窗口的类别，逻辑按它判断，不比较显示的文字。各家报的长度有几分钟的出入。
enum WindowKind {
  other,
  period, // 不知道长度：本期
  fiveHours,
  week,
  month;

  static WindowKind of(int minutes) {
    const day = 24 * 60;
    if (minutes == 0) return period;
    if (minutes >= 295 && minutes <= 305) return fiveHours;
    if (minutes >= 7 * day - 10 && minutes <= 7 * day + 10) return week;
    if (minutes >= 27 * day && minutes <= 32 * day) return month;
    return other;
  }
}

/// "2 小时 15 分钟后重置"；超过一天写日期。
String resetText(DateTime? at, DateTime now) {
  if (at == null) return '';
  final d = at.difference(now);
  if (d <= Duration.zero) return '已重置';
  if (d < const Duration(hours: 1)) return '${d.inMinutes + 1} 分钟后重置';
  if (d < const Duration(hours: 24)) {
    final m = d.inMinutes % 60;
    return m == 0 ? '${d.inHours} 小时后重置' : '${d.inHours} 小时 $m 分钟后重置';
  }
  final hh = at.hour.toString().padLeft(2, '0');
  final mm = at.minute.toString().padLeft(2, '0');
  return '${at.month}月${at.day}日 $hh:$mm 重置';
}

String agoText(DateTime t, DateTime now) {
  final d = now.difference(t);
  if (d < const Duration(minutes: 1)) return '刚刚';
  if (d < const Duration(hours: 1)) return '${d.inMinutes} 分钟前';
  if (d < const Duration(days: 1)) return '${d.inHours} 小时前';
  return '${d.inDays} 天前';
}

String agentTitle(String agent) => switch (agent) {
  'claude' => 'Claude',
  'codex' => 'Codex',
  'grok' => 'Grok',
  'cursor' => 'Cursor',
  _ => agent,
};

/// 用到这里用红色：快要被限流了。
const usageUrgent = 80.0;

/// 显示剩余而不是已用，和桌面端各记各的，存在手机上。
final usageRemaining = ValueNotifier<bool>(false);
const _remainingKey = 'usage.remaining';

Future<void> loadUsageMode() async {
  final prefs = await SharedPreferences.getInstance();
  usageRemaining.value = prefs.getBool(_remainingKey) ?? false;
}

Future<void> setUsageRemaining(bool remaining) async {
  usageRemaining.value = remaining;
  final prefs = await SharedPreferences.getInstance();
  await prefs.setBool(_remainingKey, remaining);
}

/// 界面上写的数：已用，或者切到剩余时的 100 减已用。快用完的判断不随切换变。
double shownPercent(UsageWindow w, bool remaining) =>
    remaining ? 100 - w.percent : w.percent;

/// 首页工作区列表上面的用量卡片：每家一行，各窗口一条细进度条。点开看重置时间。
/// 电脑上没有读数、或者电脑上的 Posse 太旧没有这个接口时不显示。
class UsageCard extends StatefulWidget {
  const UsageCard({super.key, required this.model});

  final PosseAppModel model;

  @override
  State<UsageCard> createState() => _UsageCardState();
}

class _UsageCardState extends State<UsageCard> {
  List<AgentUsage> _usages = const [];
  Timer? _timer;
  Object? _rpc;
  String? _computer;
  // 这台电脑这次连上后拿到过新读数：之后才读出来的缓存不能再盖掉它
  bool _fresh = false;

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_onModel);
    _onModel();
    unawaited(loadUsageMode());
    // 读数只在电脑上的 agent 运行时变，一分钟取一次足够
    _timer = Timer.periodic(const Duration(minutes: 1), (_) => _load());
  }

  @override
  void dispose() {
    widget.model.removeListener(_onModel);
    _timer?.cancel();
    super.dispose();
  }

  void _onModel() {
    final id = widget.model.activeId;
    if (id != _computer) {
      _computer = id;
      _fresh = false;
      if (_usages.isNotEmpty) setState(() => _usages = const []);
      if (id != null) unawaited(_loadCached(id));
    }
    final rpc = widget.model.rpc;
    if (identical(rpc, _rpc)) return;
    _rpc = rpc;
    // 断线时留着上次的读数：卡片忽有忽无，下面的项目列表会跟着跳
    if (rpc != null) _load();
  }

  /// 上次连这台电脑时的读数先显示出来，连上取到新的再原地换掉，项目列表不会被后出来的卡片顶下去。
  Future<void> _loadCached(String id) async {
    final prefs = await SharedPreferences.getInstance();
    final raw = prefs.getString(_cacheKey(id));
    if (raw == null || !mounted || _computer != id || _fresh) {
      return;
    }
    try {
      final us = parseUsages((jsonDecode(raw) as Map).cast<String, Object?>());
      setState(() => _usages = us);
    } catch (_) {}
  }

  Future<void> _load() async {
    final rpc = widget.model.rpc;
    final id = _computer;
    if (rpc == null || id == null) return;
    try {
      final res = await rpc.call('usage.get', {});
      if (!mounted || !identical(rpc, widget.model.rpc)) return;
      _fresh = true;
      setState(() => _usages = parseUsages(res));
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString(_cacheKey(id), jsonEncode(res));
    } catch (e) {
      // 旧版电脑端没有这个接口：不显示卡片。超时、断线这类留着上次的读数
      if (!e.toString().contains('method_not_found')) return;
      if (mounted && identical(rpc, widget.model.rpc) && _usages.isNotEmpty) {
        setState(() => _usages = const []);
      }
      final prefs = await SharedPreferences.getInstance();
      await prefs.remove(_cacheKey(id));
    }
  }

  static String _cacheKey(String computer) => 'usage.cache.$computer';

  /// 详情里的刷新：电脑上各家马上取一次（Codex、Cursor 要联网，最长半分钟），首页卡片一起换上新读数。
  Future<List<AgentUsage>> _refresh() async {
    final rpc = widget.model.rpc;
    if (rpc == null) throw const _NotConnected();
    final res = await rpc.call(
      'usage.refresh',
      {},
      timeout: const Duration(seconds: 50),
    );
    final us = parseUsages(res);
    if (mounted && identical(rpc, widget.model.rpc)) {
      _fresh = true;
      setState(() => _usages = us);
      if (_computer case final id?) {
        final prefs = await SharedPreferences.getInstance();
        await prefs.setString(_cacheKey(id), jsonEncode(res));
      }
    }
    return us;
  }

  @override
  Widget build(BuildContext context) =>
      UsageCardView(usages: _usages, onRefresh: _refresh);
}

/// 刷新时电脑没连上，详情里按界面语言写。
class _NotConnected implements Exception {
  const _NotConnected();
}

/// 卡片的样子，和取数据分开，测试和截图直接给读数。
class UsageCardView extends StatelessWidget {
  const UsageCardView({super.key, required this.usages, this.onRefresh});

  final List<AgentUsage> usages;
  final Future<List<AgentUsage>> Function()? onRefresh;

  @override
  Widget build(BuildContext context) {
    final shown = usages.where((u) => u.windows.isNotEmpty).toList();
    if (shown.isEmpty) return const SizedBox.shrink();
    final c = PosseColors.of(context);
    return ValueListenableBuilder(
      valueListenable: usageRemaining,
      builder: (context, remaining, _) => _card(context, c, shown, remaining),
    );
  }

  Widget _card(
    BuildContext context,
    PosseColors c,
    List<AgentUsage> shown,
    bool remaining,
  ) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(10, 0, 10, 10),
      child: Material(
        color: c.card,
        borderRadius: BorderRadius.circular(Rd.card),
        clipBehavior: Clip.antiAlias,
        child: InkWell(
          onTap: () => showModalBottomSheet<void>(
            context: context,
            showDragHandle: true,
            isScrollControlled: true,
            useSafeArea: true,
            // 内容多时会铺满全屏：上面留一截遮罩能点掉，拖动把手也不会躲到状态栏下面
            constraints: BoxConstraints(
              maxHeight: MediaQuery.sizeOf(context).height * 0.85,
            ),
            builder: (_) => UsageSheet(usages: shown, onRefresh: onRefresh),
          ),
          child: Padding(
            padding: const EdgeInsets.fromLTRB(14, 10, 10, 10),
            child: Row(
              children: [
                Expanded(
                  child: Wrap(
                    spacing: 16,
                    runSpacing: 6,
                    children: [
                      for (final u in shown)
                        _UsageChip(usage: u, remaining: remaining),
                    ],
                  ),
                ),
                Icon(
                  Icons.chevron_right_rounded,
                  size: Ic.medium,
                  color: c.faint,
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// 卡片里的一家，和桌面状态栏一样只写一个窗口：图标、窗口简称、百分比。有 5 小时就是它。
class _UsageChip extends StatelessWidget {
  const _UsageChip({required this.usage, required this.remaining});

  final AgentUsage usage;
  final bool remaining;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final w = usage.headWindow;
    final urgent = w.percent >= usageUrgent;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        AgentTile(agent: usage.agent, size: Ic.small),
        const SizedBox(width: 6),
        Text(
          windowShort(context.l10n, w.minutes),
          style: TextStyle(fontSize: Fs.small, color: c.muted),
        ),
        const SizedBox(width: 4),
        Text(
          '${shownPercent(w, remaining).round()}%',
          style: TextStyle(
            fontSize: Fs.small,
            fontWeight: Fw.medium,
            color: urgent ? c.danger : c.text,
            fontFeatures: const [FontFeature.tabularFigures()],
          ),
        ),
      ],
    );
  }
}

/// 窗口简称，和桌面状态栏一致：5h、周、月。
String windowShort(AppLocalizations l, int minutes) =>
    switch (WindowKind.of(minutes)) {
      WindowKind.fiveHours => '5h',
      WindowKind.week => l.usageWindowWeekShort,
      WindowKind.month => l.usageWindowMonthShort,
      _ => windowLabel(l, minutes),
    };

/// 进度条：画已用或剩余的那一段，陶土色，快用完时变红。
class UsageBar extends StatelessWidget {
  const UsageBar({
    super.key,
    required this.percent,
    required this.urgent,
    this.height = 6,
  });

  final double percent;
  final bool urgent;
  final double height;

  @override
  Widget build(BuildContext context) {
    final c = PosseColors.of(context);
    final p = (percent / 100).clamp(0.0, 1.0);
    return ClipRRect(
      borderRadius: BorderRadius.circular(height / 2),
      child: Stack(
        children: [
          Container(height: height, color: c.fill),
          if (p > 0)
            FractionallySizedBox(
              widthFactor: p < 0.02 ? 0.02 : p,
              child: Container(
                height: height,
                color: urgent ? c.danger : c.accent,
              ),
            ),
        ],
      ),
    );
  }
}

const _segmentStyle = TextStyle(fontSize: Fs.body, fontWeight: Fw.medium);
const _percentStyle = TextStyle(
  fontSize: Fs.small,
  fontWeight: Fw.medium,
  fontFeatures: [FontFeature.tabularFigures()],
);

double _textWidth(BuildContext context, String text, TextStyle style) {
  final painter = TextPainter(
    text: TextSpan(
      text: text,
      style: DefaultTextStyle.of(context).style.merge(style),
    ),
    textDirection: TextDirection.ltr,
    textScaler: MediaQuery.textScalerOf(context),
  )..layout();
  final width = painter.width;
  painter.dispose();
  return width;
}

/// 点开卡片的详情：每家的套餐、各窗口的进度和重置时间、读数时间。
class UsageSheet extends StatefulWidget {
  const UsageSheet({super.key, required this.usages, this.onRefresh});

  final List<AgentUsage> usages;

  /// 顶栏的刷新按钮，没有时不显示；返回新的读数
  final Future<List<AgentUsage>> Function()? onRefresh;

  @override
  State<UsageSheet> createState() => _UsageSheetState();
}

class _UsageSheetState extends State<UsageSheet> {
  late List<AgentUsage> usages = widget.usages;
  bool _refreshing = false;
  String _error = '';

  Future<void> _refresh() async {
    setState(() {
      _refreshing = true;
      _error = '';
    });
    try {
      final us = await widget.onRefresh!();
      if (!mounted) return;
      setState(() => usages = us.where((u) => u.windows.isNotEmpty).toList());
    } catch (e) {
      if (!mounted) return;
      // 电脑上的 Posse 太旧没有这个接口时说清楚，别的错误照常写
      final l = context.l10n;
      final old = e.toString().contains('method_not_found');
      setState(
        () => _error = e is _NotConnected
            ? l.usageNotConnected
            : old
            ? l.usageOldPosse
            : friendlyError(e),
      );
    } finally {
      if (mounted) setState(() => _refreshing = false);
    }
  }

  @override
  Widget build(BuildContext context) => ValueListenableBuilder(
    valueListenable: usageRemaining,
    builder: (context, remaining, _) => _sheet(context, remaining),
  );

  Widget _sheet(BuildContext context, bool remaining) {
    final c = PosseColors.of(context);
    final l = context.l10n;
    final now = DateTime.now();
    // 两列宽度按当前语言最长的文字量，至少是中文排好的宽度，英文的 Remaining 不折行
    final segmentedWidth = math.max<double>(
      128.0,
      2 *
              math.max(
                _textWidth(context, l.usageUsed, _segmentStyle),
                _textWidth(context, l.usageRemaining, _segmentStyle),
              ) +
          28,
    );
    final percentWidth = math.max<double>(
      76.0,
      math.max(
            _textWidth(
              context,
              l.usageShownPercent(l.usageUsed, '100%'),
              _percentStyle,
            ),
            _textWidth(
              context,
              l.usageShownPercent(l.usageRemaining, '100%'),
              _percentStyle,
            ),
          ) +
          2,
    );
    // 滚动区铺到屏幕底边，内容底下垫出安全区：滚到底时最后一行在 Home 条上方，滚动时内容从它后面经过
    final bottom = MediaQuery.paddingOf(context).bottom;
    return SingleChildScrollView(
      padding: EdgeInsets.fromLTRB(20, 0, 20, 16 + bottom),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  l.usageTitle,
                  style: TextStyle(
                    fontSize: Fs.title,
                    fontWeight: Fw.bold,
                    color: c.text,
                  ),
                ),
              ),
              if (widget.onRefresh != null)
                IconButton(
                  tooltip: l.commonRefresh,
                  visualDensity: VisualDensity.compact,
                  onPressed: _refreshing ? null : _refresh,
                  icon: _refreshing
                      ? HexSpinner(color: c.muted)
                      : Icon(
                          Icons.refresh_rounded,
                          size: Ic.medium,
                          color: c.muted,
                        ),
                ),
              SizedBox(
                width: segmentedWidth,
                child: Segmented(
                  labels: [l.usageUsed, l.usageRemaining],
                  selected: remaining ? 1 : 0,
                  onChanged: (i) => setUsageRemaining(i == 1),
                ),
              ),
            ],
          ),
          if (_error.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                _error,
                style: TextStyle(fontSize: Fs.small, color: c.danger),
              ),
            ),
          for (final u in usages) ...[
            const SizedBox(height: 16),
            Row(
              children: [
                AgentTile(agent: u.agent, size: Ic.button),
                const SizedBox(width: 10),
                Text(
                  agentTitle(u.agent),
                  style: TextStyle(
                    fontSize: Fs.body,
                    fontWeight: Fw.medium,
                    color: c.text,
                  ),
                ),
                if (u.plan.isNotEmpty) ...[
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      planLabel(l, u.plan),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(fontSize: Fs.small, color: c.muted),
                    ),
                  ),
                ] else
                  const Spacer(),
                Text(
                  agoText(u.updatedAt, now),
                  style: TextStyle(fontSize: Fs.small, color: c.muted),
                ),
              ],
            ),
            for (final w in u.shownWindows) ...[
              const SizedBox(height: 12),
              Row(
                children: [
                  Expanded(
                    child: Text(
                      windowName(l, w),
                      style: TextStyle(fontSize: Fs.small, color: c.text),
                    ),
                  ),
                  Text(
                    // 分池和总额度不是同一份读数（Claude 的 Fable 只在电脑上看 /usage 时更新）
                    w.at != null &&
                            u.updatedAt.difference(w.at!) >
                                const Duration(minutes: 10)
                        ? l.usageReadingAge(
                            resetText(w.resetsAt, now),
                            agoText(w.at!, now),
                          )
                        : resetText(w.resetsAt, now),
                    style: TextStyle(fontSize: Fs.small, color: c.muted),
                  ),
                ],
              ),
              const SizedBox(height: 6),
              Row(
                children: [
                  Expanded(
                    child: UsageBar(
                      percent: shownPercent(w, remaining),
                      urgent: w.percent >= usageUrgent,
                    ),
                  ),
                  SizedBox(
                    width: percentWidth,
                    child: Text(
                      l.usageShownPercent(
                        remaining ? l.usageRemaining : l.usageUsed,
                        '${shownPercent(w, remaining).round()}%',
                      ),
                      textAlign: TextAlign.end,
                      style: TextStyle(
                        fontSize: Fs.small,
                        fontWeight: Fw.medium,
                        color: w.percent >= usageUrgent ? c.danger : c.text,
                        fontFeatures: const [FontFeature.tabularFigures()],
                      ),
                    ),
                  ),
                ],
              ),
            ],
            for (final f in usageFacts(l, u, now)) ...[
              const SizedBox(height: 8),
              Text(
                f,
                style: TextStyle(fontSize: Fs.small, color: c.muted),
              ),
            ],
          ],
          const SizedBox(height: 20),
          Text(
            l.usageSheetNote,
            style: TextStyle(
              fontSize: Fs.small,
              color: c.muted,
              height: Lh.body,
            ),
          ),
        ],
      ),
    );
  }
}
