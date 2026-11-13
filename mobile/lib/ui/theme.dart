import 'package:flutter/material.dart';

// 设计约定和桌面端 internal/app/style.go 一致：暖灰底色分顶栏、画布、卡片几层，靠明度差和 8% 透明细线分层；
// 单闭合边框（卡片里的按钮和输入不再加框，用填充底）；陶土色给主按钮、焦点、链接、图标，选中项是它的淡底加陶土字；
// 只有"待处理"用琥珀色加文字；状态用蜂巢格形状区分（实心工作中、空心带点待处理、空心完成）；
// 一行里的几项信息靠间距和图标分开，不用"A · B"中点串。各档的用途见 docs/mobile-design.md。

/// 界面文字只有四档（tiny 到 title），相邻差 2；heading、hero 是 Markdown 和配对页的例外。等宽字和正文用同一套：放在正文、元信息里的路径、命令、代码用 [small]。
/// 终端字号在设置里，不在这套里。
abstract final class Fs {
  static const tiny = 10.0; // 角标、徽标里的字
  static const small = 12.0; // 说明、计数、路径、时间、状态行
  static const body = 14.0; // 正文、列表、按钮、输入框
  static const title = 16.0; // 顶栏、卡片、弹窗和页面标题
  static const heading = 18.0; // 只给 Markdown h1，和 h2 的 [title] 分开层级
  static const hero = 28.0; // 只给配对页的大标题，不进别处
}

/// 字重三档：正文、列表标题和按钮、页面和卡片标题。已经比旁边大一档的不再加粗。
abstract final class Fw {
  static const body = FontWeight.w400;
  static const medium = FontWeight.w500;
  static const bold = FontWeight.w600;
}

/// 行高：标题、代码和紧凑多行、说明段落、长文阅读（会话正文、Markdown）。单行文字不设。
abstract final class Lh {
  static const title = 1.3;
  static const code = 1.45;
  static const body = 1.5;
  static const read = 1.6;
}

/// 间距只用 2 · 4 · 6 · 8 · 10 · 12 · 14 · 16 · 20 · 24 · 32（`test/design_rules_test.dart` 检查），
/// 写数字不起名字：上下叠放的两行字 2，图标和紧挨的文字 4 到 6，一行里的几项 8，页面左右 16。

/// 图标尺寸。文字旁的图标和那行字同号用 [inline]、[small]；其余按用途选档。
abstract final class Ic {
  static const glyph = 12.0; // 行尾、状态行里的状态图形（蜂巢格、转圈）
  static const inline = 14.0; // 跟在小字旁
  static const small = 16.0; // 跟在正文旁、行尾的小按钮、卡片里的紧凑按钮
  static const medium = 18.0; // 列表行首、菜单项、输入区里的按钮
  static const button = 22.0; // 顶栏和工具栏的图标按钮、两行列表行首的图标
  static const large = 28.0; // 单独的大图标、列表行首的 agent 色块
  static const hero = 36.0; // 整页加载动画、空状态的图标、视频的播放键
}

/// 圆角：外圆角 = 内圆角 + 内边距。按用途选档，不按数值。
abstract final class Rd {
  static const small = 4.0; // 小标签、行内代码、勾选框
  static const button = 8.0; // 列表行、菜单行、小按钮、芯片
  static const input = 10.0; // 按钮、输入框、分段控件、提示条
  static const code = 10.0; // 代码块、图片
  static const card = 14.0; // 卡片、浮层、弹出菜单
  static const sheet = 18.0; // 底部面板、对话框
  static const composer = 20.0; // 发送输入区
  static const pill = 999.0; // 胶囊、圆点、圆形按钮
}

/// 等宽字体。iOS 用 Menlo，中文落到苹方；安卓没有 Menlo，落到 monospace（Droid Sans Mono）。
/// 不能写 Courier：安卓把它映射成笔画很细的打字机字体 Cutive Mono，代码和终端看着灰蒙蒙的。
const monoFamily = 'Menlo';
// Symbols Nerd Font Mono 是 App 自带的，补上终端提示符里 Nerd Font 的图标（见 pubspec.yaml）
const monoFallback = ['monospace', 'Symbols Nerd Font Mono', 'PingFang SC'];

/// 会话页、终端页标题下面那一行（状态、上下文、延迟）的文字。写明字重：不写会继承顶栏标题的粗体。
TextStyle statusLineStyle(Color color) => TextStyle(
  fontSize: Fs.small,
  fontWeight: Fw.body,
  color: color,
  fontFeatures: const [FontFeature.tabularFigures()],
);

TextStyle mono(double size, {Color? color, double? height}) => TextStyle(
  fontFamily: monoFamily,
  fontFamilyFallback: monoFallback,
  fontSize: size,
  color: color,
  height: height,
);

@immutable
class PosseColors extends ThemeExtension<PosseColors> {
  const PosseColors({
    required this.dark,
    required this.accent,
    required this.text,
    required this.muted,
    required this.faint,
    required this.honey,
    required this.sage,
    required this.danger,
    required this.band,
    required this.canvas,
    required this.card,
    required this.raised,
    required this.line,
    required this.tint,
    required this.wash,
    required this.fill,
    required this.code,
    required this.sky,
    required this.teal,
    required this.plum,
    required this.gold,
    required this.cyan,
    required this.shadow,
  });

  final bool dark;
  final Color accent;
  final Color text;
  final Color muted;
  final Color faint; // 最弱的文字：占位、不重要的计数
  final Color honey; // 等你处理
  final Color sage; // 本轮完成、新增
  final Color danger;
  final Color band; // 顶栏
  final Color canvas; // 页面的底，卡片放在上面
  final Color card;
  final Color raised; // 浮层：比卡片再高一层
  final Color line; // 分隔线
  final Color tint; // 选中项、自己的消息：强调色淡底
  final Color wash; // 按下的底
  final Color fill; // 卡片里次级按钮、输入的底
  final Color code; // 代码块的底
  final Color sky; // 数据色：MCP、信息
  final Color teal; // 数据色：命令行程序
  final Color plum; // 数据色：网址
  final Color gold; // 数据色：文件类型（JS、配置、压缩包）
  final Color cyan; // 数据色：文件类型（Go、数据库）
  final Color shadow; // 浮层、提示条的投影

  /// 语义色的淡底（提醒、错误、强调的条和块）。
  Color soft(Color color) => color.withValues(alpha: dark ? 0.16 : 0.10);

  /// Git 未跟踪和冲突沿用桌面 letterColor 的 MyGo Danger，和通用危险色分开。
  Color get gitDanger =>
      dark ? const Color(0xFFEF4444) : const Color(0xFFDC2626);

  /// 盖在桌面画面上的工具条、提示：半透明卡片底。
  Color get glass => card.withValues(alpha: 0.94);

  /// 压在图片、视频、相机画面上的深色底；上面的字和图标用 [Colors.white]。
  static const scrim = Color(0x8C000000);

  /// 小图上的删除角标：比 [scrim] 实，压在亮色截图上白色的叉也看得清。
  static const scrimHeavy = Color(0xCC1A1A19);

  /// 压在 [scrim] 或相机画面上的次要白字。
  static const onScrimMuted = Color(0xB3FFFFFF);

  /// 相机画面上的按钮底：浅色半透明。
  static const scrimButton = Color(0x29FFFFFF);

  /// 拖动卡片时 Material 阴影的颜色：它按颜色的透明度画，[shadow] 在浅色下太淡。
  static const lift = Color(0x4D000000);

  /// 对话框背后的遮罩。
  static const barrier = Color(0x8A000000);

  static const light = PosseColors(
    dark: false,
    accent: Color(0xFFC15F3C),
    text: Color(0xFF1F1E1D),
    muted: Color(0xFF6B6966),
    faint: Color(0xFF999793),
    honey: Color(0xFFB5700E),
    sage: Color(0xFF3E8A4C),
    danger: Color(0xFFC4352B),
    band: Color(0xFFF8F7F5),
    canvas: Color(0xFFF3F2EF),
    card: Color(0xFFFFFFFF),
    raised: Color(0xFFFFFFFF),
    line: Color(0x14000000),
    tint: Color(0x1FC15F3C),
    wash: Color(0x0A000000),
    fill: Color(0x0F000000),
    code: Color(0xFFF5F4F1),
    sky: Color(0xFF3B6FD9),
    teal: Color(0xFF21866A),
    plum: Color(0xFF8E4BB8),
    gold: Color(0xFFA9771A),
    cyan: Color(0xFF2B8A8A),
    shadow: Color(0x1F000000),
  );

  static const darkColors = PosseColors(
    dark: true,
    accent: Color(0xFFD97757),
    text: Color(0xFFECEBE8),
    muted: Color(0xFFB4B2AC),
    faint: Color(0xFF7D7B76),
    honey: Color(0xFFF2AA2E),
    sage: Color(0xFF8DBB92),
    danger: Color(0xFFE5675C),
    band: Color(0xFF1C1C1B),
    canvas: Color(0xFF1A1A19),
    card: Color(0xFF262624),
    raised: Color(0xFF2E2E2B),
    line: Color(0x14FFFFFF),
    tint: Color(0x33D97757),
    wash: Color(0x0DFFFFFF),
    fill: Color(0x17FFFFFF),
    code: Color(0xFF141413),
    sky: Color(0xFF7AA2F7),
    teal: Color(0xFF5FBFA0),
    plum: Color(0xFFB36AD9),
    gold: Color(0xFFD9A441),
    cyan: Color(0xFF4BB3B3),
    shadow: Color(0x66000000),
  );

  /// 没装 Posse 主题（比如测试里的裸 MaterialApp）时按明暗取默认色板。
  static PosseColors of(BuildContext context) {
    final theme = Theme.of(context);
    return theme.extension<PosseColors>() ??
        (theme.brightness == Brightness.dark ? darkColors : light);
  }

  @override
  PosseColors copyWith() => this;

  @override
  PosseColors lerp(PosseColors? other, double t) =>
      other == null || t < 0.5 ? this : other;
}

/// agent 品牌色块（底、图形），和桌面端 agentIcon 一致。
abstract final class Brand {
  static const claude = Color(0xFFD97757);
  static const cursor = Color(0xFF6E56CF);
  static const codexEdge = Color(0x1F000000); // Codex 的白底在浅色卡片上会融掉，描一圈细边
}

/// 看桌面的画面区域不随主题变：画面是黑的，留空用很深的暖灰分开。
abstract final class DeskColors {
  static const letterbox = Color(0xFF2A2926);
  static const picture = Color(0xFF000000);
  static const onPicture = Color(0xFFC8C6C1);
}

ThemeData posseTheme(Brightness brightness) {
  final c = brightness == Brightness.dark
      ? PosseColors.darkColors
      : PosseColors.light;
  final scheme =
      ColorScheme.fromSeed(
        seedColor: c.accent,
        brightness: brightness,
      ).copyWith(
        primary: c.accent,
        onPrimary: Colors.white,
        secondary: c.accent,
        surface: c.card,
        onSurface: c.text,
        onSurfaceVariant: c.muted,
        error: c.danger,
        outline: c.line,
        outlineVariant: c.line,
        surfaceTint: Colors.transparent,
      );
  final buttonShape = RoundedRectangleBorder(
    borderRadius: BorderRadius.circular(Rd.input),
  );
  return ThemeData(
    useMaterial3: true,
    brightness: brightness,
    colorScheme: scheme,
    scaffoldBackgroundColor: c.canvas,
    canvasColor: c.canvas,
    dividerColor: c.line,
    splashFactory: NoSplash.splashFactory,
    highlightColor: c.wash,
    extensions: [c],
    textSelectionTheme: TextSelectionThemeData(
      cursorColor: c.accent,
      selectionColor: c.accent.withValues(alpha: 0.3),
      selectionHandleColor: c.accent,
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: c.canvas,
      foregroundColor: c.text,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      titleTextStyle: TextStyle(
        fontSize: Fs.title,
        fontWeight: Fw.bold,
        color: c.text,
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: c.accent,
        foregroundColor: Colors.white,
        disabledBackgroundColor: c.fill,
        disabledForegroundColor: c.faint,
        minimumSize: const Size(0, 44),
        shape: buttonShape,
        textStyle: const TextStyle(fontSize: Fs.body, fontWeight: Fw.medium),
        elevation: 0,
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(
        foregroundColor: c.text,
        backgroundColor: c.fill,
        minimumSize: const Size(0, 44),
        shape: buttonShape,
        textStyle: const TextStyle(fontSize: Fs.body, fontWeight: Fw.medium),
      ),
    ),
    iconButtonTheme: IconButtonThemeData(
      style: IconButton.styleFrom(foregroundColor: c.text, iconSize: Ic.button),
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: c.fill,
      hintStyle: TextStyle(color: c.faint),
      contentPadding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Rd.input),
        borderSide: BorderSide.none,
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Rd.input),
        borderSide: BorderSide.none,
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Rd.input),
        borderSide: BorderSide(color: c.accent.withValues(alpha: 0.6)),
      ),
    ),
    bottomSheetTheme: BottomSheetThemeData(
      backgroundColor: c.raised,
      surfaceTintColor: Colors.transparent,
      dragHandleColor: c.faint,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(Rd.sheet)),
      ),
    ),
    popupMenuTheme: PopupMenuThemeData(
      color: c.raised,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Rd.card),
      ),
    ),
    dialogTheme: DialogThemeData(
      backgroundColor: c.raised,
      surfaceTintColor: Colors.transparent,
      // Material 3 默认标题是 24 号，跳出字号阶梯；左右离屏幕边默认 40，内容被挤窄
      titleTextStyle: TextStyle(
        fontSize: Fs.title,
        fontWeight: Fw.bold,
        height: Lh.title,
        color: c.text,
      ),
      contentTextStyle: TextStyle(
        fontSize: Fs.body,
        height: Lh.body,
        color: c.muted,
      ),
      insetPadding: const EdgeInsets.all(24),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Rd.sheet),
      ),
    ),
    progressIndicatorTheme: ProgressIndicatorThemeData(
      color: c.accent,
      linearTrackColor: c.fill,
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: c.dark ? c.raised : const Color(0xFF2A2A27),
      contentTextStyle: const TextStyle(color: Colors.white, fontSize: Fs.body),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Rd.input),
      ),
    ),
  );
}

/// 系统要求少用动画。安卓的「移除动画」在 [MediaQueryData.disableAnimations]；iOS 的「减弱动态效果」
/// 不设那一项，只在 accessibilityFeatures.reduceMotion，两个都要看。
bool reduceMotionOf(BuildContext context) =>
    MediaQuery.disableAnimationsOf(context) ||
    View.of(context).platformDispatcher.accessibilityFeatures.reduceMotion;
