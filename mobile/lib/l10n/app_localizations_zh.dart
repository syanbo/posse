// ignore: unused_import
import 'package:intl/intl.dart' as intl;

import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for Chinese (`zh`).
class AppLocalizationsZh extends AppLocalizations {
  AppLocalizationsZh([String locale = 'zh']) : super(locale);

  @override
  String get chatAllow => '允许';

  @override
  String chatAllowTitle(String tool) {
    return '允许 $tool？';
  }

  @override
  String get chatClipboardEmpty => '剪贴板里没有文字或图片';

  @override
  String get chatCode => '代码';

  @override
  String chatCompactBody(int percent, String agent) {
    return '上下文已用 $percent%。$agent 会把之前的对话压成一份摘要（/compact），腾出空间接着聊。';
  }

  @override
  String get chatCompactButton => '压缩';

  @override
  String get chatCompactConfirm => '压缩上下文？';

  @override
  String get chatConnected => '已连接电脑';

  @override
  String get chatConnecting => '正在连接电脑…';

  @override
  String chatContextUsed(int percent) {
    return '上下文已用 $percent%';
  }

  @override
  String get chatDeny => '拒绝';

  @override
  String get chatDisconnected => '和电脑的连接断开了';

  @override
  String get chatEarlierMessages => '更早的消息';

  @override
  String get chatEndedNote => '这个会话已经结束，记录还在。要继续请在首页新建会话，或者在电脑的历史页恢复。';

  @override
  String get chatFileEmpty => '文件是空的';

  @override
  String get chatFileNotAccepted => '电脑没有接受这个文件';

  @override
  String get chatFileTooLargeForOldPosse =>
      '电脑上的 Posse 太旧，传不了这么大的文件，先更新电脑上的 Posse';

  @override
  String get chatImage => '图片';

  @override
  String chatImageCount(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 张图片',
    );
    return '$_temp0';
  }

  @override
  String get chatImageEmpty => '图片是空的';

  @override
  String chatImageLimit(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '一次最多 $count 张',
    );
    return '$_temp0';
  }

  @override
  String get chatImageNotAccepted => '电脑没有接受这张图片';

  @override
  String get chatImageNotSaved => '电脑没有保存这张图片';

  @override
  String chatLatencyLabel(int ms) {
    return '和电脑的延迟 $ms 毫秒';
  }

  @override
  String get chatLatencyUnresponsive => '无响应';

  @override
  String get chatLatencyUnresponsiveLabel => '电脑没有响应';

  @override
  String get chatMediaCodeOutput => '代码产出';

  @override
  String get chatMediaGenerated => '生成的图片';

  @override
  String chatMediaImageLabel(String name) {
    return '图片 $name';
  }

  @override
  String get chatMediaMadeHeader => 'Agent 生成';

  @override
  String get chatMediaNone => '这个会话还没有图片或视频';

  @override
  String get chatMediaOldPosse => '电脑上的 posse 版本太旧，更新后才能在手机上看会话里的图片和视频';

  @override
  String get chatMediaPasted => '粘贴';

  @override
  String get chatMediaPastedHeader => '粘贴的';

  @override
  String chatMediaRecentOnly(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '只列出最近 $count 项',
    );
    return '$_temp0';
  }

  @override
  String get chatMediaTitle => '图片与视频';

  @override
  String chatMediaVideoLabel(String name) {
    return '视频 $name';
  }

  @override
  String get chatNewMessages => '新消息';

  @override
  String get chatNoCameraAccess => '没有相机权限，在系统设置里允许 Posse 使用相机';

  @override
  String get chatNoPhotoAccess => '没有照片权限，在系统设置里允许 Posse 访问照片';

  @override
  String get chatNoTranscript => '这个会话没有原生存档';

  @override
  String get chatOtherAnswer => '其他回答';

  @override
  String get chatPasteFromClipboard => '从剪贴板粘贴';

  @override
  String get chatPickFromFiles => '从「文件」选择';

  @override
  String get chatPickFromLibrary => '从相册选择照片或视频';

  @override
  String get chatPickFromLibraryMulti => '从相册选择照片或视频，可以多选';

  @override
  String get chatReadFailed => '读会话失败';

  @override
  String get chatReconnecting => '连接断开，正在重连…';

  @override
  String get chatRemoveImage => '不发这张';

  @override
  String chatReplyHint(String agent) {
    return '回复 $agent';
  }

  @override
  String get chatRoleReasoning => '思考';

  @override
  String get chatRoleSystem => '系统';

  @override
  String get chatRoleTool => '工具';

  @override
  String get chatRoleYou => '你';

  @override
  String get chatRouteDirect => '直连';

  @override
  String get chatRouteRelay => '中转';

  @override
  String get chatSend => '发送';

  @override
  String get chatSendImage => '发送图片';

  @override
  String get chatSendRefused => '没写进去';

  @override
  String get chatSendUnknown => '未确认，先看聊天再重试';

  @override
  String get chatSending => '发送中…';

  @override
  String get chatSent => '已发送';

  @override
  String get chatStateDone => '本轮完成';

  @override
  String get chatStateEnded => '会话已经结束';

  @override
  String get chatStateStarting => '启动中';

  @override
  String get chatStateTasks => '等待任务';

  @override
  String chatStateUsingTool(String tool) {
    return '正在用 $tool';
  }

  @override
  String get chatStateWaiting => '等你处理';

  @override
  String get chatStateWorking => '工作中';

  @override
  String get chatStreaming => '正在输出';

  @override
  String get chatSubmitAnswer => '提交';

  @override
  String get chatSwitchToTerminal => '切换到终端';

  @override
  String get chatTakePhoto => '拍照';

  @override
  String get chatTerminal => '终端';

  @override
  String get chatThisAction => '操作';

  @override
  String chatToolCalls(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 次',
    );
    return '工具调用 $_temp0';
  }

  @override
  String get chatToolFailed => '工具失败';

  @override
  String get chatToolsCliOther => '其他命令行';

  @override
  String get chatToolsCliSystem => '系统自带';

  @override
  String chatToolsCopied(String name) {
    return '已复制 $name';
  }

  @override
  String get chatToolsCopyCommand => '复制命令';

  @override
  String get chatToolsCopyUrl => '复制网址';

  @override
  String get chatToolsNoFile => '还没找到会话文件，agent 开始对话后会出现';

  @override
  String get chatToolsNoSkills => '这个会话还没用到技能和 MCP';

  @override
  String get chatToolsNoWeb => '这个会话还没访问网址';

  @override
  String get chatToolsNone => '这个会话还没用到技能、MCP、命令行工具和网址';

  @override
  String get chatToolsOldPosse => '电脑上的 posse 版本太旧，更新后才能在手机上看会话用到的技能和工具';

  @override
  String get chatToolsOpenInBrowser => '在浏览器中打开';

  @override
  String chatToolsSearchVia(String via) {
    return '搜索 · $via';
  }

  @override
  String chatToolsServerFailed(String server) {
    return '$server · 失败';
  }

  @override
  String get chatToolsSkill => '技能';

  @override
  String get chatToolsSkillsMcp => '技能和 MCP';

  @override
  String get chatToolsTimeline => '时间线';

  @override
  String get chatToolsTitle => '技能与工具';

  @override
  String get chatToolsViaUser => '你发的';

  @override
  String get chatToolsWeb => '网址';

  @override
  String chatToolsWebSearches(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '网址  搜索 $count 次',
    );
    return '$_temp0';
  }

  @override
  String chatUsingTool(String tool) {
    return '正在用 $tool…';
  }

  @override
  String get chatWorking => '工作中…';

  @override
  String get commonAll => '全部';

  @override
  String get commonCancel => '取消';

  @override
  String get commonClose => '关闭';

  @override
  String get commonCopy => '复制';

  @override
  String get commonDelete => '删除';

  @override
  String get commonLoading => '正在加载';

  @override
  String get commonMore => '更多';

  @override
  String get commonPaste => '粘贴';

  @override
  String get commonRefresh => '刷新';

  @override
  String get commonResetToDefault => '恢复默认';

  @override
  String get commonRetry => '重试';

  @override
  String get commonSave => '保存';

  @override
  String get commonSelectAll => '全选';

  @override
  String get deskAddressRequired => '先填地址';

  @override
  String get deskAlreadyUnlocked => 'Mac 已经解锁，密码没有发送';

  @override
  String get deskBack => '返回';

  @override
  String get deskCaptureFailed => '采集失败';

  @override
  String get deskChangePasswordTip => '换密码或忘掉';

  @override
  String get deskConnecting => '正在连接';

  @override
  String get deskConnectionNote =>
      '默认走订阅。画面发到现在连着的电脑，端口由电脑分配。这里改的是 UDP 地址，留着调试。';

  @override
  String get deskConnectionSettings => '连接设置';

  @override
  String get deskDebug => '调试';

  @override
  String deskDebugDecode(String ms) {
    return '解码 $ms ms';
  }

  @override
  String deskDebugHeadline(String fps, String received) {
    return '$fps  收到 $received fps';
  }

  @override
  String deskDebugInput(String sent, String acked) {
    return '输入：已发 $sent、已确认 $acked';
  }

  @override
  String deskDebugKeyframes(String count) {
    return '关键帧 $count';
  }

  @override
  String deskDebugLoss(String loss) {
    return '丢包 $loss';
  }

  @override
  String deskDebugPath(String path) {
    return '线路 $path';
  }

  @override
  String deskDebugRate(String bitrate, String bytes, String peak) {
    return '码率 $bitrate（$bytes）  峰值 $peak';
  }

  @override
  String deskDebugView(String zoom, String x, String y) {
    return '缩放 $zoom  平移 $x, $y';
  }

  @override
  String get deskDisconnect => '断开';

  @override
  String get deskDisconnected => '已断开';

  @override
  String deskDisplayNumbered(String number) {
    return '显示器 $number';
  }

  @override
  String get deskEndGeneric => '连接已结束';

  @override
  String get deskEndNoReport => '电脑没有收到画面报告';

  @override
  String get deskEndStopped => '已在 Mac 上停止';

  @override
  String get deskEndUnsubscribed => '已停止观看';

  @override
  String get deskEnterPassword => '输入密码';

  @override
  String get deskEntireScreen => '整块屏';

  @override
  String deskEventClick(String point) {
    return '单击 $point';
  }

  @override
  String deskEventClicks(int count, String point) {
    return '$count 击 $point';
  }

  @override
  String deskEventDown(String point) {
    return '按下 $point';
  }

  @override
  String deskEventMove(String point) {
    return '拖动 $point';
  }

  @override
  String deskEventPoint(String point) {
    return '指向 $point';
  }

  @override
  String deskEventRightClick(String point) {
    return '右键 $point';
  }

  @override
  String deskEventScroll(String dx, String dy) {
    return '滚动 $dx, $dy';
  }

  @override
  String deskEventScrollInertia(String dx, String dy) {
    return '滚动 $dx, $dy 惯性';
  }

  @override
  String deskEventText(String text) {
    return '文字 $text';
  }

  @override
  String deskEventUp(String point) {
    return '松开 $point';
  }

  @override
  String get deskFailBusy => '另一台手机正在看';

  @override
  String get deskFailCodec => '这台电脑不支持手机的视频编码';

  @override
  String get deskFailDisconnected => '连接已断开';

  @override
  String get deskFailGeneric => '连接失败';

  @override
  String get deskFailLan => '手机和这台 Mac 不在同一个局域网';

  @override
  String get deskFailNoPlugin => '这台设备不能看桌面';

  @override
  String get deskFailOutdated => '这台电脑的 Posse 需要更新';

  @override
  String get deskFailRoute => '直连和中转都不通';

  @override
  String get deskFailScreenPermission => '请到 Mac 上允许屏幕录制';

  @override
  String get deskForgetPassword => '忘掉密码';

  @override
  String get deskFpsStill => '静止';

  @override
  String get deskQuality => '清晰度';

  @override
  String get deskQualityAuto => '自动';

  @override
  String get deskFrameRate => '帧率';

  @override
  String get deskFrameRateAuto => '自动';

  @override
  String get deskGestureClick => '单击';

  @override
  String get deskGestureDismiss => '点任意处关掉，模式在设置「看桌面」里换';

  @override
  String get deskGestureDrag => '拖动';

  @override
  String get deskGestureLongPressDrag => '长按到震一下，再拖';

  @override
  String get deskGestureMovePointer => '移动指针';

  @override
  String get deskGesturePan => '移动画面';

  @override
  String get deskGesturePinch => '双指捏合';

  @override
  String get deskGestureRightClick => '右键';

  @override
  String get deskGestureScroll => '滚动';

  @override
  String get deskGestureSlideOne => '单指滑动';

  @override
  String get deskGestureTap => '单指轻点';

  @override
  String get deskGestureTapAtPointer => '单指轻点，点在指针处';

  @override
  String get deskGestureThreeFingerDrag => '三指滑动';

  @override
  String get deskGestureTitle => '手势';

  @override
  String get deskGestureTwoFingerDrag => '双指拖动';

  @override
  String get deskGestureTwoFingerTap => '双指轻点';

  @override
  String get deskGestureZoom => '缩放';

  @override
  String get deskGestureZoomedDrag => '放大后单指拖动';

  @override
  String get deskHost => '主机';

  @override
  String get deskIndirect => '画面过不了隧道，请在同一个 Wi-Fi 下扫这台 Mac 的局域网码，或改用 Tailscale';

  @override
  String get deskKeyboard => '键盘';

  @override
  String get deskListingWindows => '正在列出窗口…';

  @override
  String get deskMacLocked => 'Mac 已锁屏';

  @override
  String get deskMainDisplay => '主显示器';

  @override
  String get deskMissionControl => '调度中心';

  @override
  String get deskModeDirect => '直接点按模式';

  @override
  String get deskModeTrackpad => '触控板模式';

  @override
  String get deskNoAccessibility => '能看不能操作，请到 Mac 上打开辅助功能';

  @override
  String get deskNoComputer => '没有连着的电脑';

  @override
  String get deskPasswordForgotten => '已忘掉记住的密码';

  @override
  String get deskPasswordHint => '登录密码';

  @override
  String get deskPasswordSaveFailed => '没能记住密码';

  @override
  String get deskPasswordSaved => '已记住密码，下次锁屏点「解锁」即可';

  @override
  String get deskPathLan => '直连';

  @override
  String get deskPathPunched => '打洞直连';

  @override
  String get deskPathRelay => '中转';

  @override
  String get deskPaused => '已暂停（后台）';

  @override
  String get deskPort => '端口';

  @override
  String get deskPortRange => '端口要在 1 到 65535';

  @override
  String get deskReconnect => '重新连接';

  @override
  String get deskRemember => '记住';

  @override
  String deskResolutionMain(String width, String height) {
    return '$width × $height · 主屏';
  }

  @override
  String get deskScreenPermissionHint =>
      'Mac 上打开「系统设置 › 隐私与安全性 › 录屏与系统录音」里的 Posse，再重试';

  @override
  String get deskShortcuts => '快捷键';

  @override
  String get deskSpaceLeft => '◀ 空间';

  @override
  String get deskSpaceRight => '空间 ▶';

  @override
  String get deskToLandscape => '切到横屏';

  @override
  String get deskToPortrait => '切到竖屏';

  @override
  String get deskUdpAddress => 'UDP 地址';

  @override
  String get deskUnlock => '解锁';

  @override
  String get deskUnlocking => '正在解锁…';

  @override
  String get deskUnlockNote => '输入 Mac 的登录密码，发到锁屏界面后自动回车';

  @override
  String get deskUnlockNoteRemember => '输入 Mac 的登录密码，发到锁屏界面后自动回车。勾「记住」只存在这台手机上';

  @override
  String get deskUse => '使用';

  @override
  String get deskViewTip => '画面';

  @override
  String get deskWaitingPicture => '已连上，等画面';

  @override
  String get deskWindowGone => '窗口关掉了或不在这个空间，回到整块屏';

  @override
  String get deskWindows => '窗口';

  @override
  String get deskWindowsEmpty => '这个空间里没有可以单独看的窗口';

  @override
  String get deskWindowsError => '列不出窗口，稍后再试';

  @override
  String get errorBadPairingCode => '配对码不对，重新扫一次或者重新复制';

  @override
  String get errorBinaryFile => '这是二进制文件，没法当文本打开';

  @override
  String get errorCantConnect => '连不上电脑：确认电脑上的 Posse 开着，手机和电脑在同一个网络';

  @override
  String get errorConnectionLost => '和电脑的连接断开了，正在重连';

  @override
  String get errorFileTooLarge => '文件太大，手机上打不开';

  @override
  String get errorFirstConnect =>
      '连不上电脑：确认电脑上的 Posse 开着、手机和电脑在同一个网络，并在手机设置里允许 Posse 使用无线数据和本地网络';

  @override
  String get errorNoNewTerminal => '电脑没有返回新终端';

  @override
  String get errorNotAFile => '这不是文件';

  @override
  String get errorNotConnected => '还没连上电脑';

  @override
  String get errorDesktopTooOld => '电脑上的 Posse 是旧版，连不上这个 App：在电脑上更新 Posse';

  @override
  String get errorOldDesktop => '电脑上的 Posse 是旧版，不支持这个功能：在电脑上更新 Posse';

  @override
  String get errorOutsideWorkspace => '路径不在工作区里';

  @override
  String get errorTimeout => '电脑没有回应，可能已经休眠或者网络断了';

  @override
  String get filesBinary => '二进制文件，手机上看不了内容';

  @override
  String filesCantOpenLink(String url) {
    return '打不开这个链接：$url';
  }

  @override
  String get filesChangesAndFiles => '改动和文件';

  @override
  String get filesDetails => '详细信息';

  @override
  String get filesDownloadDisconnected => '和电脑的连接断了，下好的部分留着';

  @override
  String get filesDownloadOldPosse => '电脑上的 posse 版本太旧，更新后才能下载';

  @override
  String get filesDownloadSlow => '网络太慢，没下完，下好的部分留着';

  @override
  String get filesEmptyFolder => '目录是空的';

  @override
  String get filesFetching => '正在从电脑取文件…';

  @override
  String get filesFetchingVideo => '正在从电脑取视频…';

  @override
  String filesFetchingVideoProgress(String done, String total) {
    return '正在从电脑取视频  $done / $total';
  }

  @override
  String get filesFindButton => '按文件名或路径找';

  @override
  String get filesFindHint => '文件名或路径';

  @override
  String get filesNoAlbumAccess => '没有相册权限：在系统设置里允许 Posse 访问照片';

  @override
  String get filesNoHighlight => '文件较大，不着色';

  @override
  String get filesNoSpace => '手机空间不够，没存进相册';

  @override
  String get filesNoStorageAccess => '没有存储权限：在系统设置里允许 Posse 访问存储';

  @override
  String get filesNotInWorkspace => '这个文件不在当前工作区里，手机上打不开';

  @override
  String get filesPause => '暂停';

  @override
  String get filesPlay => '播放';

  @override
  String get filesPreview => '预览';

  @override
  String get filesResumeDownload => '接着下载';

  @override
  String get filesSaveToAlbum => '存到相册';

  @override
  String get filesSaveToAlbumFailed => '没存进相册，稍后再试';

  @override
  String get filesSaveToAlbumNote => '下载到手机，放进相册的「Posse」相簿';

  @override
  String get filesSaveToDownloads => '存到下载';

  @override
  String get filesSaveToDownloadsFailed => '没存进下载目录，稍后再试';

  @override
  String get filesSaveToDownloadsNote => '下载到手机，放进「下载/Posse」，文件管理里能找到';

  @override
  String get filesSavedToAlbum => '已存到相册的「Posse」相簿';

  @override
  String filesSavedToDownloads(String name) {
    return '已存到「下载/Posse」：$name';
  }

  @override
  String get filesShare => '分享';

  @override
  String get filesShareNote => '下载到手机，用系统分享存到\"文件\"或发给别的 App';

  @override
  String get filesShareOtherNote => '下载到手机，发给别的 App';

  @override
  String get filesSource => '源码';

  @override
  String get filesTitle => '文件';

  @override
  String get filesTooLarge => '文件超过 500 MB，不往手机上下载';

  @override
  String get filesTruncated => '文件太大，只显示前 512 KB';

  @override
  String filesVideoCantPlay(String error) {
    return '这个视频手机上放不了：$error';
  }

  @override
  String get filesVideoOldPosse => '电脑上的 posse 版本太旧，更新后才能在手机上播放视频';

  @override
  String get filesVideoSlow => '网络太慢，视频没下完，下好的部分留着';

  @override
  String get filesVideoTooLarge => '视频超过 500 MB，手机上不下载';

  @override
  String gitAhead(int count) {
    return '领先 $count';
  }

  @override
  String gitBehind(int count) {
    return '落后 $count';
  }

  @override
  String get gitBranchesTitle => '切换分支';

  @override
  String get gitBranchesPickRepo => '在哪个仓库切换分支';

  @override
  String get gitBranchesSearch => '搜索分支';

  @override
  String get gitBranchesLocal => '本地';

  @override
  String get gitBranchesRemote => '远程';

  @override
  String get gitBranchesNoMatch => '没有匹配的分支';

  @override
  String get gitBranchCurrent => '当前分支';

  @override
  String gitBranchInWorkspace(String workspace) {
    return '在 $workspace 里检出着';
  }

  @override
  String gitBranchInWorktree(String dir) {
    return '在 worktree $dir 里检出着';
  }

  @override
  String gitBranchesBusy(String operation) {
    return '$operation 还没做完，不能切换分支';
  }

  @override
  String gitCheckoutAgentTitle(String agent) {
    return '$agent 正在这个工作区里工作';
  }

  @override
  String get gitCheckoutAgentBody => '切换分支会改动工作目录里的文件，可能打乱它正在做的修改。';

  @override
  String get gitCheckoutAnyway => '仍然切换';

  @override
  String gitSwitchedTo(String branch) {
    return '已切换到 $branch';
  }

  @override
  String get gitBinaryDiff => '二进制文件，手机上看不了差异';

  @override
  String gitChangedFiles(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 个文件有改动',
    );
    return '$_temp0';
  }

  @override
  String get gitChanges => '改动';

  @override
  String get gitClean => '工作区是干净的，没有改动';

  @override
  String get gitConflicts => '冲突';

  @override
  String get gitDeleted => '已删除';

  @override
  String get gitDetachedHead => '分离头指针';

  @override
  String get gitDiffTruncated => '差异太长，只显示前面一部分';

  @override
  String get gitFlatView => '平铺';

  @override
  String gitHunkLine(int line) {
    return '第 $line 行';
  }

  @override
  String get gitImageBefore => '改之前';

  @override
  String get gitImageNewFile => '新文件，之前没有';

  @override
  String get gitImageNow => '现在';

  @override
  String get gitImageTooLarge => '图片超过 10 MB，手机上不显示';

  @override
  String get gitImageUndrawable => '这张图手机上画不出来';

  @override
  String get gitNoDifferences => '没有差异';

  @override
  String get gitStatsBinary => '二进制';

  @override
  String get gitStatsImage => '图片';

  @override
  String get gitStatsNewFile => '新文件';

  @override
  String gitTracking(String upstream) {
    return '跟踪 $upstream';
  }

  @override
  String get gitTreeView => '按目录';

  @override
  String get gitUnversioned => '未跟踪';

  @override
  String get gitViewFullFile => '查看完整文件';

  @override
  String get historyAlreadyOpen => '这个会话已经在电脑上开着';

  @override
  String get historyAnyWordNotice => '没有同时含所有词的会话，下面是含其中任一个词的';

  @override
  String get historyByFolderTip => '按文件夹分组，点按时间';

  @override
  String get historyByTimeTip => '按时间分组，点按文件夹';

  @override
  String get historyClearSearch => '清除';

  @override
  String get historyContinueOnComputer => '在电脑上继续';

  @override
  String get historyEmptyComputer => '电脑上还没有会话历史';

  @override
  String historyEmptyComputerAgent(String agent) {
    return '电脑上还没有 $agent 的会话历史';
  }

  @override
  String get historyEmptyFolder => '这个目录还没有会话历史';

  @override
  String historyEmptyFolderAgent(String agent) {
    return '这个目录还没有 $agent 的会话历史';
  }

  @override
  String get historyLast30Days => '最近 30 天';

  @override
  String get historyLast7Days => '最近 7 天';

  @override
  String get historyNoResults => '没有找到';

  @override
  String get historyNotYetOpen => '电脑上的会话还没出现，稍后在首页找它';

  @override
  String get historyOldPosse => '电脑上的 Posse 太旧，更新后才能看历史';

  @override
  String get historyOlder => '1 个月前';

  @override
  String get historyOpening => '正在打开…';

  @override
  String get historyResumed => '已在电脑上接着这个会话';

  @override
  String get historySearchHint => '搜索提问、回复和工具输出';

  @override
  String get historyToday => '今天';

  @override
  String get historyUnknownFolder => '未知文件夹';

  @override
  String get historyUntitled => '（没有标题）';

  @override
  String get historyViewOnPhone => '在手机上看';

  @override
  String get historyYesterday => '昨天';

  @override
  String get homeAddressDirect => '直连 · QUIC';

  @override
  String get homeAddressRelay => '经中转 · QUIC';

  @override
  String get homeCantConnect => '连不上这台电脑。';

  @override
  String get homeComputerFallback => '电脑';

  @override
  String homeComputerWithHost(String name, String host) {
    return '$name（$host）';
  }

  @override
  String get homeConnecting => '正在连接电脑…';

  @override
  String get homeDisconnect => '断开连接';

  @override
  String get homeDisconnected => '已断开';

  @override
  String homeDisconnectedList(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '已断开 · 可以连接的电脑 $count 台',
    );
    return '$_temp0';
  }

  @override
  String homeDisconnectedNamed(String name) {
    return '$name · 已断开';
  }

  @override
  String get homeFailed => '连不上';

  @override
  String homeFailedNamed(String name) {
    return '$name · 连不上';
  }

  @override
  String get homeForget => '忘掉';

  @override
  String get homeForgetBody => '之后要重新扫码才能连上它。';

  @override
  String get homeForgetComputer => '忘掉这台电脑';

  @override
  String homeForgetConfirm(String name) {
    return '忘掉 $name';
  }

  @override
  String get homeHistoryTip => '历史';

  @override
  String get homeLongPressHint => '长按一台电脑可以改名或忘掉它';

  @override
  String get homeNoRunning => '没有运行中的会话';

  @override
  String get homeNoWorkspaces => '电脑上的 Posse 还没有工作区';

  @override
  String get homeReconnecting => '连接断开，正在重连…';

  @override
  String get homeRemoteDesktop => '看桌面';

  @override
  String get homeRename => '重命名';

  @override
  String get homeRenameHelper => '只改手机上显示的名字，清空就用电脑自己的名字';

  @override
  String get homeRenameHint => '名字';

  @override
  String get homeRenameTitle => '给电脑改名';

  @override
  String get homeScanToAdd => '扫码添加电脑';

  @override
  String get homeSettingsTip => '设置';

  @override
  String get homeTabProjects => '项目';

  @override
  String get homeTabRunning => '运行中';

  @override
  String homeWaitingBanner(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 个会话等你处理',
    );
    return '$_temp0';
  }

  @override
  String get homeWaitingConnection => '等连上电脑';

  @override
  String get keysAltHint => '例如 b、f、.';

  @override
  String get keysAltWhich => 'Alt 加哪个键';

  @override
  String get keysBar => '快捷键栏';

  @override
  String get keysBarNote => '最左边的键盘开关固定不动，其余按这里从上到下的顺序从左往右排。自己加的键点开可以修改或删除。';

  @override
  String get keysBuiltin => '内置';

  @override
  String get keysClear => '清空输入框';

  @override
  String get keysCtrlC => 'Ctrl + C：结束程序';

  @override
  String get keysCtrlHint => '例如 r、d、z、l';

  @override
  String get keysCtrlInvalid => 'Ctrl 只能加字母或 @ [ \\ ] ^ _ ?';

  @override
  String get keysCtrlWhich => 'Ctrl 加哪个键';

  @override
  String get keysDown => '方向键：下';

  @override
  String get keysEdit => '编辑快捷键';

  @override
  String get keysEnter => '回车';

  @override
  String get keysEnterAfter => '输入后回车';

  @override
  String get keysEsc => 'Esc：中断、退出';

  @override
  String get keysExit => '退出 agent（/exit）';

  @override
  String get keysImage => '发送文件（照片、视频、文件）';

  @override
  String get keysKindSpecial => '特殊键';

  @override
  String get keysKindText => '文字';

  @override
  String get keysLabel => '按钮上的字';

  @override
  String keysLabelHint(String shown) {
    return '不填就用 $shown';
  }

  @override
  String get keysLabelHintEmpty => '不填就用要输入的文字';

  @override
  String get keysLeft => '方向键：左';

  @override
  String get keysNew => '新建快捷键';

  @override
  String get keysNewNote => '一段常用的文字、Ctrl 组合键或 PgUp 这类特殊键';

  @override
  String get keysPaste => '粘贴剪贴板';

  @override
  String get keysResetOrder => '恢复默认顺序';

  @override
  String get keysResetOrderNote => '自己加的键保留，排回图片、粘贴后面';

  @override
  String get keysRight => '方向键：右';

  @override
  String get keysSendNow => 'Ctrl + Enter：排队的消息马上交给 Claude';

  @override
  String get keysShiftTab => 'Shift + Tab：切换模式';

  @override
  String get keysSpecialWhich => '哪个键';

  @override
  String get keysTab => 'Tab：补全';

  @override
  String get keysText => '输入的文字';

  @override
  String get keysTextHint => '例如 /compact';

  @override
  String get keysTitle => '终端快捷键';

  @override
  String keysTypeText(String text) {
    return '输入「$text」';
  }

  @override
  String keysTypeTextEnter(String text) {
    return '输入「$text」并回车';
  }

  @override
  String get keysUp => '方向键：上';

  @override
  String mermaidCantDraw(String error) {
    return '画不出图：$error';
  }

  @override
  String get mermaidNoSize => '图的尺寸为空';

  @override
  String get mermaidNoSvg => 'Pele 没有画出 SVG';

  @override
  String get mermaidTimeout => '画图超时';

  @override
  String get pairAddNote => '可以配对多台电脑，同一时间连其中一台，在首页顶部切换。';

  @override
  String get pairAddTitle => '添加电脑';

  @override
  String get pairCameraDenied => '需要相机权限才能扫码：在系统设置里允许 Posse 使用相机，也可以把配对码贴进来。';

  @override
  String get pairCameraFailed => '打不开相机，可以把配对码贴进来。';

  @override
  String get pairClipboardEmpty => '剪贴板是空的。';

  @override
  String get pairClipboardNotCode => '剪贴板里不是配对码。';

  @override
  String get pairConnect => '连接';

  @override
  String get pairConnectTitle => '连接 Posse';

  @override
  String pairConnected(String name) {
    return '已连上 $name';
  }

  @override
  String pairConnectedAt(String name, String address) {
    return '已连上 $name，地址 $address';
  }

  @override
  String get pairConnecting => '正在连接…';

  @override
  String get pairHideManual => '收起';

  @override
  String get pairIntro => '在手机上看 agent 的进展、回复和批准请求。';

  @override
  String get pairManual => '手动输入';

  @override
  String get pairPasteCode => '粘贴配对码';

  @override
  String get pairScanAim => '对准电脑上 Posse 的配对码';

  @override
  String get pairScanButton => '扫码配对';

  @override
  String get pairScanClipboardEmpty => '剪贴板是空的';

  @override
  String get pairScanClipboardNotCode => '剪贴板里不是配对码';

  @override
  String get pairStepOpenDetail => '点窗口左下角的手机图标';

  @override
  String get pairStepOpenTitle => '在电脑上打开 Posse';

  @override
  String get pairStepScanDetail => '或者在电脑上复制配对码，回来粘贴';

  @override
  String get pairStepScanTitle => '扫描二维码';

  @override
  String get sessionCloseSession => '关闭会话';

  @override
  String sessionCloseSessionBody(String agent) {
    return '$agent 会被结束，还没做完的事会中断。对话记录留在电脑的会话历史里，之后可以在桌面端恢复。';
  }

  @override
  String get sessionCloseSessionConfirm => '关闭这个会话？';

  @override
  String get sessionCloseTerminal => '关闭终端';

  @override
  String sessionCloseTerminalBody(String title) {
    return '「$title」里正在运行的程序会被结束。';
  }

  @override
  String get sessionCloseTerminalConfirm => '关闭这个终端？';

  @override
  String get sessionDeskLayout => '电脑布局';

  @override
  String get sessionDeskLayoutNote => '缩到屏幕宽度，双指放大后可以滑动';

  @override
  String sessionDetailApprove(String tool) {
    return '等你批准 $tool';
  }

  @override
  String get sessionDetailDone => '本轮完成';

  @override
  String get sessionDetailLost => '未恢复，点开时拉起';

  @override
  String get sessionDetailTerminalOnly => '只能看终端';

  @override
  String sessionDetailUsing(String tool) {
    return '正在用 $tool';
  }

  @override
  String get sessionDetailWaiting => '等你处理';

  @override
  String get sessionMedia => '图片与视频';

  @override
  String get sessionMediaNote => 'agent 截的图、生成的图片和视频，粘贴过的图片';

  @override
  String get sessionNew => '新建会话';

  @override
  String get sessionNoWorkspaces => '还没有工作区';

  @override
  String get sessionOpenTerminal => '打开终端';

  @override
  String get sessionOpenWith => '用什么开';

  @override
  String get sessionPickWorkspace => '在哪个工作区';

  @override
  String get sessionPromptHint => '第一句话（可以不写，进去再说）';

  @override
  String sessionStartChat(String agent) {
    return '开始和 $agent 对话';
  }

  @override
  String get sessionStarting => '正在启动…';

  @override
  String get sessionStateIdle => '空闲';

  @override
  String get sessionStateStarting => '启动中';

  @override
  String get sessionStateTasks => '等待任务';

  @override
  String get sessionStateUnread => '本轮完成，还没看';

  @override
  String get sessionStateWaiting => '待处理';

  @override
  String get sessionStateWorking => '工作中';

  @override
  String get sessionTerminal => '终端';

  @override
  String get sessionTools => '技能与工具';

  @override
  String get sessionToolsNote => '用到的技能、MCP 和命令行程序';

  @override
  String get sessionWorkspace => '工作区';

  @override
  String get settingsAbout => '关于';

  @override
  String get settingsAppearance => '外观';

  @override
  String get settingsChat => '对话';

  @override
  String get settingsCheckFailed => '检查失败，稍后再试';

  @override
  String get settingsCheckUpdate => '检查更新';

  @override
  String get settingsComputer => '电脑';

  @override
  String get settingsDeskDirect => '直接点按';

  @override
  String get settingsDeskDirectNote => '点画面上哪里就按哪里，长按后拖动，单指拖动平移放大后的画面。';

  @override
  String get settingsDeskNote => '点首页的电脑名，菜单里的「看桌面」能看 Mac 的屏幕，用手指操作它。';

  @override
  String get settingsDeskPasswords => '记住的 Mac 密码';

  @override
  String settingsDeskPasswordsCount(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '记着 $count 台电脑的登录密码',
    );
    return '$_temp0';
  }

  @override
  String get settingsDeskPasswordsForget => '全部忘掉';

  @override
  String get settingsDeskPasswordsNone => '没有记住的密码';

  @override
  String get settingsDeskTrackpad => '触控板';

  @override
  String get settingsDeskTrackpadNote =>
      '把手机屏幕当触控板：单指滑动移动指针，点按、右键都落在指针上，三指滑动拖动。';

  @override
  String get settingsExpandTools => '工具调用默认展开';

  @override
  String get settingsFontNote => '手机看终端时终端按手机屏幕排版，字越小一行放得越多。';

  @override
  String get settingsFontSample => 'agent 正在运行 Claude Code';

  @override
  String get settingsFontSize => '字号';

  @override
  String get settingsHome => '首页';

  @override
  String get settingsKeys => '快捷键';

  @override
  String settingsKeysCustom(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '自定义 $count 个',
    );
    return '$_temp0';
  }

  @override
  String get settingsKeysNote => '调整键盘上方快捷键栏的顺序，加自己的键';

  @override
  String get settingsLanguageSystem => '跟随系统';

  @override
  String get settingsLanguageTitle => '语言';

  @override
  String get settingsNotConnected => '没有连接';

  @override
  String get settingsOpenChat => '对话';

  @override
  String get settingsOpenChatNote =>
      '对话页右上角可以切到终端。没有会话存档的 agent 和 shell 总是开终端。';

  @override
  String get settingsOpenFirst => '点开会话时先看';

  @override
  String get settingsOpenTerminal => '终端';

  @override
  String get settingsOpenTerminalNote =>
      '终端页右上角可以切到对话。对话和终端是同一个会话的两种看法，切换时互相替换。';

  @override
  String settingsPairedCount(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '共配对 $count 台',
    );
    return '$_temp0';
  }

  @override
  String get settingsProjectMeta => '项目显示分支和路径';

  @override
  String get settingsPushDenied => '已开启的通知或角标被 iOS 限制，请在系统设置中允许。';

  @override
  String get settingsPushDone => 'agent 完成时';

  @override
  String get settingsPushFailed => '没能开启通知，重新连上电脑后会再试。';

  @override
  String get settingsPushNoRelay => '这台电脑没配外网中转，发不出通知。在电脑 Posse 的手机页「外网中转」里配好。';

  @override
  String get settingsPushOff => '不推送通知。';

  @override
  String get settingsPushOldComputer => '更新这台电脑上的 Posse，才能独立控制通知和角标。';

  @override
  String get settingsPushOn => 'Posse 在后台、Mac 没人用时，电脑会推送到这台手机。';

  @override
  String get settingsPushBadge => '角标';

  @override
  String get settingsPushOldRelay => '中转尚未确认支持只更新角标，请更新中转或重连后再试。已开启的通知不受此限制。';

  @override
  String get settingsPushSystemSettings => '打开系统设置';

  @override
  String get settingsPushTitle => '通知';

  @override
  String get settingsBackgroundTitle => '后台';

  @override
  String get settingsBackgroundKeep => '后台保持连接';

  @override
  String get settingsBackgroundKeepOff => '切到别的 App 后连接还会留约 30 秒，这期间回来不用重连。';

  @override
  String get settingsBackgroundKeepOn =>
      '播放和其他 App 混音的静音音频，系统不会挂起 Posse，终端在后台一直收着输出，更费电；系统内存紧张时仍可能关掉 App。';

  @override
  String get settingsPushUnknown => '连上电脑后生效。';

  @override
  String get settingsPushWaiting => 'agent 等你处理时';

  @override
  String get settingsShowReasoning => '显示思考过程';

  @override
  String get settingsTerminal => '终端';

  @override
  String get settingsThemeDark => '深色';

  @override
  String get settingsThemeLight => '浅色';

  @override
  String get settingsThemeSystem => '跟随系统';

  @override
  String get settingsTitle => '设置';

  @override
  String get settingsUpToDate => '已是最新';

  @override
  String settingsUpdateTo(String version, String build) {
    return '更新到 $version（$build）';
  }

  @override
  String settingsVersionBuild(String version, String build) {
    return '$version（$build）';
  }

  @override
  String get settingsWorkspaceMeta => '工作区显示分支';

  @override
  String terminalCantOpenLink(String url) {
    return '打不开这个链接：$url';
  }

  @override
  String get terminalClipboardEmpty => '剪贴板里没有文字或图片';

  @override
  String get terminalExited => '终端已经退出';

  @override
  String get terminalKeyClear => '清空';

  @override
  String get terminalNoAgent => '这个终端里没有在运行的 agent';

  @override
  String get terminalNotInWorkspace => '这个文件不在当前工作区里，手机上打不开';

  @override
  String get terminalSendFile => '发送文件';

  @override
  String get terminalSendNow => '立即发送';

  @override
  String get terminalSwitchToChat => '切换到对话';

  @override
  String timeDaysAgo(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 天前',
    );
    return '$_temp0';
  }

  @override
  String timeHoursAgo(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 小时前',
    );
    return '$_temp0';
  }

  @override
  String get timeJustNow => '刚刚';

  @override
  String timeMinutesAgo(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 分钟前',
    );
    return '$_temp0';
  }

  @override
  String get updateAndroidNote =>
      '下载完系统会弹出安装确认，覆盖安装，配对不会丢。第一次更新要在系统设置里允许 Posse 安装应用。';

  @override
  String get updateBadPackage => '打不开安装：安装包不在更新目录里';

  @override
  String updateBanner(String version, String build) {
    return '新版本 $version（$build）';
  }

  @override
  String updateBuild(String build) {
    return '构建 $build';
  }

  @override
  String get updateButton => '更新';

  @override
  String get updateCantInstall => '打不开安装，稍后再试';

  @override
  String updateCurrent(String version, String build) {
    return '当前 $version（$build）';
  }

  @override
  String get updateDownloadFailed => '下载失败，稍后再试';

  @override
  String get updateDownloading => '正在下载…';

  @override
  String updateDownloadingPercent(int percent) {
    return '正在下载 $percent%';
  }

  @override
  String get updateInstallDenied => '要在系统设置里允许 Posse 安装应用，才能更新';

  @override
  String get updateIosNote => '点安装后在后台下载，回到主屏幕能在图标上看到进度，装好后 App 会自动换成新版，配对不会丢。';

  @override
  String get updateOpening => '正在打开安装…';

  @override
  String get updateRequesting => '正在请求安装…';

  @override
  String get updateSkip => '不再提示这个版本';

  @override
  String updateTitle(String version) {
    return '新版本 $version';
  }

  @override
  String uploadSendingFile(int count, int index) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '正在发送文件 $index/$count',
      one: '正在发送文件',
      zero: '正在发送文件',
    );
    return '$_temp0';
  }

  @override
  String uploadSendingImage(int count, int index) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '正在发送图片 $index/$count',
      one: '正在发送图片',
      zero: '正在发送图片',
    );
    return '$_temp0';
  }

  @override
  String get usageCursorModels => 'Cursor 模型';

  @override
  String usageIncludedSpend(String used, String limit, String percent) {
    return '套餐内用量 $used / $limit（$percent）';
  }

  @override
  String get usageListSeparator => '，';

  @override
  String usageNamedWindow(String name, String window) {
    return '$name $window';
  }

  @override
  String get usageNotConnected => '电脑没有连上';

  @override
  String get usageOldPosse => '电脑上的 Posse 太旧，更新后才能刷新';

  @override
  String usageOnDemand(String used, String stale) {
    return '按需用量 $used$stale';
  }

  @override
  String usageOnDemandLimited(String used, String limit, String stale) {
    return '按需用量 $used，上限 $limit$stale';
  }

  @override
  String get usageOtherModels => '其他模型';

  @override
  String get usagePerMonth => '/月';

  @override
  String get usageOldValue => '旧值';

  @override
  String get usagePendingUpdate => '待更新';

  @override
  String get usageReading => '正在更新…';

  @override
  String get usageQueryDisabled => '在线查询已关闭，显示上次读数';

  @override
  String get usageCodexLoginRequired =>
      '请在电脑上运行 codex login，用 ChatGPT 账号重新登录后刷新用量。';

  @override
  String get usageCodexFileStorageRequired =>
      'Codex 用量查询只支持 file 登录存储。请在电脑上将 cli_auth_credentials_store 设为 file，再运行 codex login 登录。';

  @override
  String get usageCodexDetailsFailed => '用量已更新，但重置卡明细读取失败。显示上次重置卡读数，请稍后刷新重试。';

  @override
  String get usageCodexReadFailed => 'Codex 用量更新失败，请稍后重试。';

  @override
  String usageOldReading(String time) {
    return '旧值 · $time';
  }

  @override
  String usageReadingFrom(String time) {
    return '$time的读数';
  }

  @override
  String get usageRemaining => '剩余';

  @override
  String usageResetCards(int count, String stale) {
    return '重置卡 $count 张$stale';
  }

  @override
  String usageResetCardsExpiring(int count, String date, String stale) {
    return '重置卡 $count 张，最早一张 $date 过期$stale';
  }

  @override
  String get usageResetDone => '已重置';

  @override
  String usageResetsAt(String date) {
    return '$date 重置';
  }

  @override
  String usageResetsInHours(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 小时后重置',
    );
    return '$_temp0';
  }

  @override
  String usageResetsInHoursMinutes(int hours, int minutes) {
    return '$hours 小时 $minutes 分钟后重置';
  }

  @override
  String usageResetsInMinutes(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count 分钟后重置',
    );
    return '$_temp0';
  }

  @override
  String usageShares(String shares, String stale) {
    return '本周用量用在 $shares$stale';
  }

  @override
  String get usageSheetNote =>
      '电脑上的 agent 运行时顺带更新；开启在线查询时，打开详情也会补查 Codex；窗口过期后等待新读数。';

  @override
  String usageShownPercent(String kind, String percent) {
    return '$kind $percent';
  }

  @override
  String usageStaleReading(String time) {
    return '（$time的读数）';
  }

  @override
  String get usageTitle => '套餐用量';

  @override
  String get usageUsed => '已用';

  @override
  String usageWindowDays(int count) {
    return '$count 天';
  }

  @override
  String get usageWindowFiveHours => '5 小时';

  @override
  String usageWindowHours(int count) {
    return '$count 小时';
  }

  @override
  String get usageWindowMonth => '每月';

  @override
  String get usageWindowMonthShort => '月';

  @override
  String get usageWindowPeriod => '本期';

  @override
  String get usageWindowWeek => '每周';

  @override
  String get usageWindowWeekShort => '周';

  @override
  String get workspaceMain => '主工作区';

  @override
  String get workspaceTabChanges => '改动';

  @override
  String get workspaceTabFiles => '文件';

  @override
  String get workspaceTabHistory => '历史';

  @override
  String get discTitle => '讨论';

  @override
  String get discNew => '新建讨论';

  @override
  String get discJoin => '加入讨论';

  @override
  String get discEmpty => '还没有讨论';

  @override
  String get discArchived => '已归档';

  @override
  String get discName => '名称';

  @override
  String get discDescription => '说明';

  @override
  String get discWorkspace => '这台电脑的工作区';

  @override
  String get discSave => '保存';

  @override
  String get discCancel => '取消';

  @override
  String get discRetry => '重试';

  @override
  String get discClose => '关闭';

  @override
  String get discInviteCode => '邀请';

  @override
  String get discSearch => '搜索消息';

  @override
  String get discDetails => '讨论详情';

  @override
  String get discMembers => 'Agent 成员';

  @override
  String get discAddMember => '添加 Agent';

  @override
  String get discRole => '职责';

  @override
  String get discAgent => 'Agent';

  @override
  String get discBase => '基准分支（可选）';

  @override
  String get discApplyNow => '用这些设置重启 worker';

  @override
  String get discRemove => '移除';

  @override
  String get discRestart => '重新启动并继续';

  @override
  String get discResume => '继续已领取的请求';

  @override
  String get discKey => '关键消息';

  @override
  String get discMarkKey => '标为关键';

  @override
  String get discUnmarkKey => '取消关键标记';

  @override
  String get discQuote => '引用';

  @override
  String get discAnswer => '回答';

  @override
  String get discSend => '发送';

  @override
  String get discMessageHint => '在讨论中发言…';

  @override
  String get discLater => '加载更新的消息';

  @override
  String get discLatest => '跳到最新';

  @override
  String get discReadOnly => '此讨论已归档，只能阅读';

  @override
  String get discOffline => '连接已断开，草稿已保留。';

  @override
  String get discPending => '等待房主批准';

  @override
  String get discRevoked => '你已无法参与此讨论';

  @override
  String get discPeople => '参与的人';

  @override
  String get discShare => '在局域网分享';

  @override
  String get discUnshare => '停止局域网分享';

  @override
  String get discInvite => '生成邀请';

  @override
  String get discLeave => '退出讨论';

  @override
  String get discApprove => '批准';

  @override
  String get discReject => '拒绝';

  @override
  String get discRename => '编辑讨论';

  @override
  String get discArchive => '归档';

  @override
  String get discRestore => '恢复';

  @override
  String get discStop => '停止执行';

  @override
  String get discConfirm => '仍在同一目录执行';

  @override
  String get discIsolate => '改用独立 worktree';

  @override
  String get discAbandon => '放弃并重新发起';

  @override
  String get discAbandonWarning => '这一轮可能已修改文件。请核实已有影响，再重新发起。';

  @override
  String get discConcurrentWarning => '其他 Agent 可能正在修改此目录的文件，请确认不会冲突后继续。';

  @override
  String get discOpenWorker => '打开 worker 终端';

  @override
  String get discAddFile => '添加文件';

  @override
  String get discAddPhoto => '添加图片';

  @override
  String get discFileLarge => '每个附件不能超过 20 MiB';

  @override
  String get discFileLimit => '每条消息最多 10 个附件';

  @override
  String get discDownloadError => '附件未下载完整';

  @override
  String get discUnknown => '结果尚未确认。请重试同一请求，避免重复操作。';

  @override
  String get discEditDraft => '作为新消息编辑';

  @override
  String get discEditDraftWarning => '上一条消息可能已经发出。请先查看讨论，再发送新消息。';

  @override
  String get discOnline => '在线';

  @override
  String get discPersonOffline => '离线';

  @override
  String get discNoMessages => '还没有消息';

  @override
  String get discWaiting => '等待你回答';

  @override
  String get discStateQueued => '排队中';

  @override
  String get discStateHeld => '等待确认';

  @override
  String get discStateBlocked => '受阻';

  @override
  String get discStateStarting => '启动中';

  @override
  String get discStateRunning => '执行中';

  @override
  String get discStateWaiting => '等待回答';

  @override
  String get discStateDone => '已完成';

  @override
  String get discStateFailed => '失败';

  @override
  String get discStateInterrupted => '已中断';

  @override
  String get discStateStopped => '已停止';

  @override
  String get discStateCancelled => '已取消';

  @override
  String get discStateClaimed => '已领取';

  @override
  String get discStateRevoked => '资格已撤销';

  @override
  String get discDraftFilesExpired => '请重新选择附件后重试，消息请求 ID 会保留。';

  @override
  String get discReport => '查看汇报文件';

  @override
  String get discNewRequest => '重新填写';

  @override
  String get discNewRequestWarning => '上一次请求可能已经添加成员或创建 worktree。请核实结果后再发起新请求。';

  @override
  String discAtOthersAgent(String owner) {
    return '提醒 $owner 安排';
  }

  @override
  String get discAtYou => '@你';

  @override
  String get discAccessLabel => '谁能 @ 它';

  @override
  String get discAccessOnlyMe => '只有我';

  @override
  String get discAccessAll => '所有人';

  @override
  String get discAccessSome => '指定的人';

  @override
  String get discAccessNobody => '还没有别人加入。';

  @override
  String get discAccessEveryone => '所有人都能 @ 它';

  @override
  String discAccessPeople(String names) {
    return '$names 能 @ 它';
  }

  @override
  String get discYou => '你';

  @override
  String get discHost => '房主';

  @override
  String get discCopy => '复制';

  @override
  String discOnComputer(String name) {
    return '在 $name 的电脑上';
  }

  @override
  String get profileTitle => '名字和头像';

  @override
  String profileNote(String computer) {
    return '改的是当前连接的电脑（$computer）的个人资料，它的所有讨论都用这一份；Agent 的名字和图标不受影响。';
  }

  @override
  String profileRowNote(String computer) {
    return '$computer 上的所有讨论都用这一份';
  }

  @override
  String get profileName => '名字';

  @override
  String get profileNameInvalid => '名字要 1 到 60 个字';

  @override
  String get profileChoose => '选择图片';

  @override
  String get profileRemove => '移除头像';

  @override
  String get profileCrop => '保存后从中间裁成正方形。';

  @override
  String get profileSave => '保存';

  @override
  String get profilePendingTitle => '还没更新';

  @override
  String profilePending(String title, String reason) {
    return '“$title”还没更新：$reason';
  }

  @override
  String get profileOffline => '房主上线后自动发送';

  @override
  String get profileUnsupported => '房主的 Posse 版本太旧';

  @override
  String get profileImageTooLarge => '图片太大，请换一张小一点的。';

  @override
  String get discEditMember => '编辑 Agent';

  @override
  String get discAdd => '添加';

  @override
  String get discNoAgents => '这台电脑上没有可用的 Agent';

  @override
  String get discRoleHint => '它负责什么';

  @override
  String get discModelLabel => '模型';

  @override
  String get discEffortLabel => '思考深度';

  @override
  String get discDefault => '默认';

  @override
  String get discWorkDir => '工作目录';

  @override
  String get discExistingWorkspace => '已有工作区';

  @override
  String get discNewWorktreeMode => '新建 worktree';

  @override
  String get discNoHits => '没有找到相关消息';

  @override
  String get discNoMembers => '还没有 Agent';

  @override
  String get discAnswerHint => '输入回答…';

  @override
  String get discAtButton => '@ 提及';

  @override
  String discAnswerTo(String name) {
    return '回答 $name';
  }

  @override
  String get discWorktreeName => 'worktree 名称';

  @override
  String get discCreate => '创建';

  @override
  String get discJoinConfirm => '加入';

  @override
  String get discShareConfirm => '分享';

  @override
  String get discUnshareConfirm => '停止分享';

  @override
  String get discSummary => '摘要';

  @override
  String get discSummarize => '更新摘要';

  @override
  String get discSummaryClear => '清除摘要';

  @override
  String get discSummarizeRequest =>
      '总结这个讨论到目前为止的内容，并保存为讨论摘要：已经定下的结论、还没解决的问题、谁在做什么。';

  @override
  String get discStateMerged => '已并入上一条请求';

  @override
  String get discGuest => '只读';

  @override
  String get discInviteGuest => '邀请只读访客';

  @override
  String get discGuestNote => '你是以只读身份加入的，不能在这里发言。';
}
