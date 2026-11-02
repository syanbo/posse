// ignore: unused_import
import 'package:intl/intl.dart' as intl;

import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for English (`en`).
class AppLocalizationsEn extends AppLocalizations {
  AppLocalizationsEn([String locale = 'en']) : super(locale);

  @override
  String get commonCancel => 'Cancel';

  @override
  String get commonClose => 'Close';

  @override
  String get commonCopy => 'Copy';

  @override
  String get commonDelete => 'Delete';

  @override
  String get commonLoading => 'Loading';

  @override
  String get commonMore => 'More';

  @override
  String get commonPaste => 'Paste';

  @override
  String get commonRefresh => 'Refresh';

  @override
  String get commonResetToDefault => 'Reset to Default';

  @override
  String get commonRetry => 'Retry';

  @override
  String get commonSave => 'Save';

  @override
  String get commonSelectAll => 'Select All';

  @override
  String get deskAddressRequired => 'Enter an address first';

  @override
  String get deskAlreadyUnlocked =>
      'The Mac is already unlocked. The password wasn’t sent.';

  @override
  String get deskBack => 'Back';

  @override
  String get deskCaptureFailed => 'Capture failed';

  @override
  String get deskChangePasswordTip => 'Change or Forget Password';

  @override
  String get deskConnecting => 'Connecting…';

  @override
  String get deskConnectionNote =>
      'By default the picture goes to the connected computer by subscription, and the computer picks the port. This changes the UDP address, for debugging.';

  @override
  String get deskConnectionSettings => 'Connection Settings';

  @override
  String get deskDebug => 'Debug';

  @override
  String deskDebugDecode(String ms) {
    return 'Decode $ms ms';
  }

  @override
  String deskDebugHeadline(String fps, String received) {
    return '$fps  received $received fps';
  }

  @override
  String deskDebugInput(String sent, String acked) {
    return 'Input: sent $sent, acked $acked';
  }

  @override
  String deskDebugKeyframes(String count) {
    return 'Keyframes $count';
  }

  @override
  String deskDebugLoss(String loss) {
    return 'Loss $loss';
  }

  @override
  String deskDebugPath(String path) {
    return 'Path $path';
  }

  @override
  String deskDebugRate(String bitrate, String bytes, String peak) {
    return 'Bitrate $bitrate ($bytes)  peak $peak';
  }

  @override
  String deskDebugView(String zoom, String x, String y) {
    return 'Zoom $zoom  pan $x, $y';
  }

  @override
  String get deskDisconnect => 'Disconnect';

  @override
  String get deskDisconnected => 'Disconnected';

  @override
  String deskDisplayNumbered(String number) {
    return 'Display $number';
  }

  @override
  String get deskEndGeneric => 'Connection ended';

  @override
  String get deskEndNoReport =>
      'The computer received no reports from the phone';

  @override
  String get deskEndStopped => 'Stopped on the Mac';

  @override
  String get deskEndUnsubscribed => 'Stopped viewing';

  @override
  String get deskEnterPassword => 'Enter Password';

  @override
  String get deskEntireScreen => 'Entire Screen';

  @override
  String deskEventClick(String point) {
    return 'Click $point';
  }

  @override
  String deskEventClicks(int count, String point) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count clicks',
      one: '$count click',
    );
    return '$_temp0 $point';
  }

  @override
  String deskEventDown(String point) {
    return 'Down $point';
  }

  @override
  String deskEventMove(String point) {
    return 'Drag $point';
  }

  @override
  String deskEventPoint(String point) {
    return 'Point $point';
  }

  @override
  String deskEventRightClick(String point) {
    return 'Right-click $point';
  }

  @override
  String deskEventScroll(String dx, String dy) {
    return 'Scroll $dx, $dy';
  }

  @override
  String deskEventScrollInertia(String dx, String dy) {
    return 'Scroll $dx, $dy inertia';
  }

  @override
  String deskEventText(String text) {
    return 'Text $text';
  }

  @override
  String deskEventUp(String point) {
    return 'Up $point';
  }

  @override
  String get deskFailBusy => 'Another phone is viewing';

  @override
  String get deskFailCodec =>
      'This computer can’t encode video this phone can play';

  @override
  String get deskFailDisconnected => 'Connection lost';

  @override
  String get deskFailGeneric => 'Couldn’t connect';

  @override
  String get deskFailLan =>
      'This phone and the Mac aren’t on the same local network';

  @override
  String get deskFailNoPlugin => 'This device can’t view the desktop';

  @override
  String get deskFailOutdated => 'Posse on this computer needs an update';

  @override
  String get deskFailRoute => 'Neither a direct connection nor the relay works';

  @override
  String get deskFailScreenPermission => 'Allow screen recording on the Mac';

  @override
  String get deskForgetPassword => 'Forget Password';

  @override
  String get deskFpsStill => 'Still';

  @override
  String get deskGestureClick => 'Click';

  @override
  String get deskGestureDismiss =>
      'Tap anywhere to close. Change the mode under Remote Desktop in Settings.';

  @override
  String get deskGestureDrag => 'Drag';

  @override
  String get deskGestureLongPressDrag =>
      'Long-press until it buzzes, then drag';

  @override
  String get deskGestureMovePointer => 'Move Pointer';

  @override
  String get deskGesturePan => 'Pan';

  @override
  String get deskGesturePinch => 'Pinch';

  @override
  String get deskGestureRightClick => 'Right-Click';

  @override
  String get deskGestureScroll => 'Scroll';

  @override
  String get deskGestureSlideOne => 'Slide one finger';

  @override
  String get deskGestureTap => 'Tap one finger';

  @override
  String get deskGestureTapAtPointer => 'Tap one finger; clicks at the pointer';

  @override
  String get deskGestureThreeFingerDrag =>
      'Slide three fingers, or tap then hold and slide';

  @override
  String get deskGestureTitle => 'Gestures';

  @override
  String get deskGestureTwoFingerDrag => 'Drag two fingers';

  @override
  String get deskGestureTwoFingerTap => 'Tap two fingers';

  @override
  String get deskGestureZoom => 'Zoom';

  @override
  String get deskGestureZoomedDrag => 'Drag one finger when zoomed in';

  @override
  String get deskHost => 'Host';

  @override
  String get deskIndirect =>
      'The picture can’t get through the tunnel. On the same Wi-Fi, scan this Mac’s LAN code, or use Tailscale.';

  @override
  String get deskKeyboard => 'Keyboard';

  @override
  String get deskListingWindows => 'Listing windows…';

  @override
  String get deskMacLocked => 'Mac is locked';

  @override
  String get deskMainDisplay => 'Main Display';

  @override
  String get deskMissionControl => 'Mission Control';

  @override
  String get deskModeDirect => 'Direct Touch Mode';

  @override
  String get deskModeTrackpad => 'Trackpad Mode';

  @override
  String get deskNoAccessibility =>
      'View only. Turn on Accessibility for Posse on the Mac to control it.';

  @override
  String get deskNoComputer => 'No computer connected';

  @override
  String get deskPasswordForgotten => 'Saved password forgotten';

  @override
  String get deskPasswordHint => 'Login Password';

  @override
  String get deskPasswordSaveFailed => 'Couldn’t save the password';

  @override
  String get deskPasswordSaved =>
      'Password saved. Next time the Mac locks, tap “Unlock”.';

  @override
  String get deskPathLan => 'Direct';

  @override
  String get deskPathPunched => 'Direct (Hole-Punched)';

  @override
  String get deskPathRelay => 'Relay';

  @override
  String get deskPaused => 'Paused (in background)';

  @override
  String get deskPort => 'Port';

  @override
  String get deskPortRange => 'Port must be from 1 to 65535';

  @override
  String get deskReconnect => 'Reconnect';

  @override
  String get deskRemember => 'Remember';

  @override
  String deskResolutionMain(String width, String height) {
    return '$width × $height · Main';
  }

  @override
  String get deskScreenPermissionHint =>
      'On the Mac, turn on Posse in System Settings › Privacy & Security › Screen & System Audio Recording, then retry.';

  @override
  String get deskShortcuts => 'Shortcuts';

  @override
  String get deskSpaceLeft => '◀ Space';

  @override
  String get deskSpaceRight => 'Space ▶';

  @override
  String get deskToLandscape => 'Switch to Landscape';

  @override
  String get deskToPortrait => 'Switch to Portrait';

  @override
  String get deskUdpAddress => 'UDP Address';

  @override
  String get deskUnlock => 'Unlock';

  @override
  String get deskUnlockNote =>
      'Enter the Mac’s login password. It’s typed on the lock screen, then Return is pressed.';

  @override
  String get deskUnlockNoteRemember =>
      'Enter the Mac’s login password. It’s typed on the lock screen, then Return is pressed. “Remember” stores it on this phone only.';

  @override
  String get deskUse => 'Use';

  @override
  String get deskViewTip => 'Choose Screen or Window';

  @override
  String get deskWaitingPicture => 'Connected. Waiting for the picture…';

  @override
  String get deskWindowGone =>
      'The window closed or left this space. Showing the entire screen.';

  @override
  String get deskWindows => 'Windows';

  @override
  String get deskWindowsEmpty =>
      'No windows in this space can be viewed on their own';

  @override
  String get deskWindowsError => 'Couldn’t list windows. Try again later.';

  @override
  String get errorBadPairingCode =>
      'The pairing code isn’t valid. Scan it again or copy it again.';

  @override
  String get errorBinaryFile =>
      'This is a binary file and can’t be opened as text.';

  @override
  String get errorCantConnect =>
      'Can’t connect to the Mac. Make sure Posse is open on the Mac and both are on the same network.';

  @override
  String get errorConnectionLost =>
      'Lost the connection to the Mac. Reconnecting…';

  @override
  String get errorFileTooLarge => 'The file is too large to open on the phone.';

  @override
  String get errorFirstConnect =>
      'Can’t connect to the Mac. Make sure Posse is open on the Mac and both are on the same network, and allow Posse to use wireless data and the local network in the phone’s Settings.';

  @override
  String get errorNoNewTerminal => 'The Mac didn’t return the new terminal';

  @override
  String get errorNotAFile => 'This isn’t a file.';

  @override
  String get errorNotConnected => 'Not connected to the Mac yet';

  @override
  String get errorOldDesktop =>
      'Posse on the Mac is too old for this. Update Posse on the Mac.';

  @override
  String get errorOutsideWorkspace => 'The path isn’t in the workspace.';

  @override
  String get errorTimeout =>
      'The Mac isn’t responding. It may be asleep, or the network is down.';

  @override
  String get homeAddressDirect => 'Direct · QUIC';

  @override
  String get homeAddressRelay => 'Via Relay · QUIC';

  @override
  String get homeCantConnect => 'Can’t connect to this Mac.';

  @override
  String get homeComputerFallback => 'Mac';

  @override
  String get homeConnecting => 'Connecting to the Mac…';

  @override
  String get homeDisconnect => 'Disconnect';

  @override
  String get homeDisconnected => 'Disconnected';

  @override
  String homeDisconnectedList(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: 'Disconnected · $count Macs available',
      one: 'Disconnected · $count Mac available',
    );
    return '$_temp0';
  }

  @override
  String homeDisconnectedNamed(String name) {
    return '$name · Disconnected';
  }

  @override
  String get homeFailed => 'Can’t Connect';

  @override
  String homeFailedNamed(String name) {
    return '$name · Can’t Connect';
  }

  @override
  String get homeForget => 'Forget';

  @override
  String get homeForgetBody =>
      'You’ll need to scan the code again to connect to it.';

  @override
  String get homeForgetComputer => 'Forget This Mac';

  @override
  String homeForgetConfirm(String name) {
    return 'Forget $name?';
  }

  @override
  String get homeHistoryTip => 'History';

  @override
  String get homeLongPressHint => 'Long-press a Mac to rename or forget it';

  @override
  String get homeNoRunning => 'No running sessions';

  @override
  String get homeNoWorkspaces => 'Posse on the Mac has no workspaces yet';

  @override
  String get homeReconnecting => 'Connection lost, reconnecting…';

  @override
  String get homeRemoteDesktop => 'Remote Desktop';

  @override
  String get homeRename => 'Rename';

  @override
  String get homeRenameHelper =>
      'Only changes the name shown on this phone. Leave empty to use the Mac’s own name.';

  @override
  String get homeRenameHint => 'Name';

  @override
  String get homeRenameTitle => 'Rename Mac';

  @override
  String get homeScanToAdd => 'Scan to Add a Mac';

  @override
  String get homeSettingsTip => 'Settings';

  @override
  String get homeTabProjects => 'Projects';

  @override
  String get homeTabRunning => 'Running';

  @override
  String homeWaitingBanner(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count sessions need you',
      one: '$count session needs you',
    );
    return '$_temp0';
  }

  @override
  String get homeWaitingConnection => 'Waiting to connect';

  @override
  String get keysAltHint => 'e.g. b, f, .';

  @override
  String get keysAltWhich => 'Alt plus which key';

  @override
  String get keysBar => 'Key Bar';

  @override
  String get keysBarNote =>
      'The keyboard toggle on the far left stays put; the rest go left to right in the order listed here. Tap a key you added to edit or delete it.';

  @override
  String get keysBuiltin => 'Built-in';

  @override
  String get keysClear => 'Clear Input';

  @override
  String get keysCtrlC => 'Ctrl + C: stop the program';

  @override
  String get keysCtrlHint => 'e.g. r, d, z, l';

  @override
  String get keysCtrlInvalid =>
      'Ctrl works only with a letter or @ [ \\ ] ^ _ ?';

  @override
  String get keysCtrlWhich => 'Ctrl plus which key';

  @override
  String get keysDown => 'Arrow key: down';

  @override
  String get keysEdit => 'Edit Shortcut';

  @override
  String get keysEnter => 'Return';

  @override
  String get keysEnterAfter => 'Press Return After Typing';

  @override
  String get keysEsc => 'Esc: interrupt, exit';

  @override
  String get keysExit => 'Exit Agent (/exit)';

  @override
  String get keysImage => 'Send File (photos, videos, files)';

  @override
  String get keysKindSpecial => 'Special';

  @override
  String get keysKindText => 'Text';

  @override
  String get keysLabel => 'Button Label';

  @override
  String keysLabelHint(String shown) {
    return 'Leave empty to use $shown';
  }

  @override
  String get keysLeft => 'Arrow key: left';

  @override
  String get keysNew => 'New Shortcut';

  @override
  String get keysNewNote =>
      'Frequently used text, a Ctrl combination, or a special key like PgUp';

  @override
  String get keysPaste => 'Paste Clipboard';

  @override
  String get keysResetOrder => 'Restore Default Order';

  @override
  String get keysResetOrderNote =>
      'Keys you added are kept and placed after Send File and Paste';

  @override
  String get keysRight => 'Arrow key: right';

  @override
  String get keysSendNow => 'Ctrl + Enter: send queued messages to Claude now';

  @override
  String get keysShiftTab => 'Shift + Tab: switch mode';

  @override
  String get keysSpecialWhich => 'Which Key';

  @override
  String get keysTab => 'Tab: complete';

  @override
  String get keysText => 'Text to Type';

  @override
  String get keysTextHint => 'e.g. /compact';

  @override
  String get keysTitle => 'Terminal Shortcuts';

  @override
  String keysTypeText(String text) {
    return 'Type “$text”';
  }

  @override
  String keysTypeTextEnter(String text) {
    return 'Type “$text” and press Return';
  }

  @override
  String get keysUp => 'Arrow key: up';

  @override
  String mermaidCantDraw(String error) {
    return 'Can’t draw the diagram: $error';
  }

  @override
  String get mermaidNoSize => 'The diagram has no size';

  @override
  String get mermaidNoSvg => 'Pele didn’t draw an SVG';

  @override
  String get mermaidTimeout => 'Drawing the diagram timed out';

  @override
  String get pairAddNote =>
      'You can pair several Macs and connect to one at a time. Switch between them at the top of the home screen.';

  @override
  String get pairAddTitle => 'Add a Mac';

  @override
  String get pairCameraDenied =>
      'Posse needs camera access to scan. Allow it in Settings, or paste the pairing code.';

  @override
  String get pairCameraFailed =>
      'Can’t open the camera. You can paste the pairing code instead.';

  @override
  String get pairClipboardEmpty => 'The clipboard is empty.';

  @override
  String get pairClipboardNotCode =>
      'The clipboard doesn’t contain a pairing code.';

  @override
  String get pairConnect => 'Connect';

  @override
  String get pairConnectTitle => 'Connect to Posse';

  @override
  String pairConnected(String name) {
    return 'Connected to $name';
  }

  @override
  String pairConnectedAt(String name, String address) {
    return 'Connected to $name at $address';
  }

  @override
  String get pairConnecting => 'Connecting…';

  @override
  String get pairHideManual => 'Hide';

  @override
  String get pairIntro =>
      'Follow your agents’ progress, reply, and approve requests from your phone.';

  @override
  String get pairManual => 'Enter Manually';

  @override
  String get pairPasteCode => 'Paste Pairing Code';

  @override
  String get pairScanAim => 'Point at the pairing code in Posse on your Mac';

  @override
  String get pairScanButton => 'Scan to Pair';

  @override
  String get pairScanClipboardEmpty => 'The clipboard is empty';

  @override
  String get pairScanClipboardNotCode =>
      'The clipboard doesn’t contain a pairing code';

  @override
  String get pairStepOpenDetail =>
      'Click the phone icon at the bottom left of the window';

  @override
  String get pairStepOpenTitle => 'Open Posse on your Mac';

  @override
  String get pairStepScanDetail =>
      'Or copy the pairing code on the Mac and paste it here';

  @override
  String get pairStepScanTitle => 'Scan the QR code';

  @override
  String get sessionCloseSession => 'Close Session';

  @override
  String sessionCloseSessionBody(String agent) {
    return '$agent will be stopped and unfinished work interrupted. The conversation stays in the Mac’s session history and can be resumed on the Mac later.';
  }

  @override
  String get sessionCloseSessionConfirm => 'Close This Session?';

  @override
  String get sessionCloseTerminal => 'Close Terminal';

  @override
  String sessionCloseTerminalBody(String title) {
    return 'Programs running in “$title” will be stopped.';
  }

  @override
  String get sessionCloseTerminalConfirm => 'Close This Terminal?';

  @override
  String get sessionDeskLayout => 'Desktop Layout';

  @override
  String get sessionDeskLayoutNote =>
      'Shrinks to the screen width; pinch to zoom, then drag to scroll';

  @override
  String sessionDetailApprove(String tool) {
    return 'Waiting for you to approve $tool';
  }

  @override
  String get sessionDetailDone => 'Turn finished';

  @override
  String get sessionDetailLost => 'Not Restored: starts when you open it';

  @override
  String get sessionDetailTerminalOnly => 'Terminal only';

  @override
  String sessionDetailUsing(String tool) {
    return 'Using $tool';
  }

  @override
  String get sessionDetailWaiting => 'Waiting for you';

  @override
  String get sessionMedia => 'Images & Videos';

  @override
  String get sessionMediaNote =>
      'Screenshots the agent took, generated images and videos, and pasted images';

  @override
  String get sessionNew => 'New Session';

  @override
  String get sessionNoWorkspaces => 'No workspaces yet';

  @override
  String get sessionOpenTerminal => 'Open Terminal';

  @override
  String get sessionOpenWith => 'Open With';

  @override
  String get sessionPickWorkspace => 'Which Workspace';

  @override
  String get sessionPromptHint =>
      'First message (optional; you can say it later)';

  @override
  String sessionStartChat(String agent) {
    return 'Start Chat with $agent';
  }

  @override
  String get sessionStarting => 'Starting…';

  @override
  String get sessionStateDone => 'Done';

  @override
  String get sessionStateIdle => 'Idle';

  @override
  String get sessionStateStarting => 'Starting';

  @override
  String get sessionStateTasks => 'Waiting on Tasks';

  @override
  String get sessionStateUnread => 'Turn finished, not seen yet';

  @override
  String get sessionStateWaiting => 'Needs You';

  @override
  String get sessionStateWorking => 'Working';

  @override
  String get sessionTerminal => 'Terminal';

  @override
  String get sessionTools => 'Skills & Tools';

  @override
  String get sessionToolsNote =>
      'Skills, MCP servers, and command-line tools used';

  @override
  String get sessionWorkspace => 'Workspace';

  @override
  String get settingsAbout => 'About';

  @override
  String get settingsAppearance => 'Appearance';

  @override
  String get settingsChat => 'Chat';

  @override
  String get settingsCheckFailed => 'Check failed. Try again later.';

  @override
  String get settingsCheckUpdate => 'Check for Updates';

  @override
  String get settingsComputer => 'Mac';

  @override
  String get settingsDeskDirect => 'Direct Touch';

  @override
  String get settingsDeskDirectNote =>
      'Tap where you want to click, long-press then move to drag, and drag one finger to pan a zoomed-in view.';

  @override
  String get settingsDeskNote =>
      'Tap the Mac’s name on the home screen and choose Remote Desktop to see the Mac’s screen and control it with your fingers.';

  @override
  String get settingsDeskPasswords => 'Saved Mac Passwords';

  @override
  String settingsDeskPasswordsCount(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: 'Login passwords saved for $count Macs',
      one: 'Login password saved for $count Mac',
    );
    return '$_temp0';
  }

  @override
  String get settingsDeskPasswordsForget => 'Forget All';

  @override
  String get settingsDeskPasswordsNone => 'No saved passwords';

  @override
  String get settingsDeskTrackpad => 'Trackpad';

  @override
  String get settingsDeskTrackpadNote =>
      'Use the phone screen as a trackpad: slide one finger to move the pointer; taps and right-clicks land on the pointer; slide three fingers to drag.';

  @override
  String get settingsExpandTools => 'Expand Tool Calls by Default';

  @override
  String get settingsFontNote =>
      'On the phone, the terminal is laid out for the phone’s screen. Smaller text fits more on a line.';

  @override
  String get settingsFontSample => 'the agent is running Claude Code';

  @override
  String get settingsFontSize => 'Font Size';

  @override
  String get settingsHome => 'Home';

  @override
  String get settingsKeys => 'Shortcuts';

  @override
  String settingsKeysCustom(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count custom keys',
      one: '$count custom key',
    );
    return '$_temp0';
  }

  @override
  String get settingsKeysNote =>
      'Reorder the key bar above the keyboard and add your own keys';

  @override
  String get settingsLanguageSystem => 'System';

  @override
  String get settingsLanguageTitle => 'Language';

  @override
  String get settingsNotConnected => 'Not Connected';

  @override
  String get settingsOpenChat => 'Chat';

  @override
  String get settingsOpenChatNote =>
      'You can switch to the terminal at the top right of the chat. Agents without a saved session and shells always open in the terminal.';

  @override
  String get settingsOpenFirst => 'Open Sessions In';

  @override
  String get settingsOpenTerminal => 'Terminal';

  @override
  String get settingsOpenTerminalNote =>
      'You can switch to the chat at the top right of the terminal. Chat and terminal are two views of the same session; switching replaces one with the other.';

  @override
  String settingsPairedCount(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count Macs paired',
      one: '$count Mac paired',
    );
    return '$_temp0';
  }

  @override
  String get settingsProjectMeta => 'Show Branch and Path for Projects';

  @override
  String get settingsShowReasoning => 'Show Thinking';

  @override
  String get settingsTerminal => 'Terminal';

  @override
  String get settingsThemeDark => 'Dark';

  @override
  String get settingsThemeLight => 'Light';

  @override
  String get settingsThemeSystem => 'System';

  @override
  String get settingsTitle => 'Settings';

  @override
  String get settingsUpToDate => 'Up to Date';

  @override
  String settingsUpdateTo(String version, String build) {
    return 'Update to $version ($build)';
  }

  @override
  String get settingsWorkspaceMeta => 'Show Branch for Workspaces';

  @override
  String terminalCantOpenLink(String url) {
    return 'Can’t open this link: $url';
  }

  @override
  String get terminalClipboardEmpty => 'The clipboard has no text or image';

  @override
  String get terminalExited => 'The terminal has exited';

  @override
  String get terminalKeyClear => 'Clear';

  @override
  String get terminalNoAgent => 'No agent is running in this terminal';

  @override
  String get terminalNotInWorkspace =>
      'This file isn’t in the current workspace and can’t be opened on the phone';

  @override
  String get terminalSendFile => 'Send File';

  @override
  String get terminalSendNow => 'Send Now';

  @override
  String get terminalSwitchToChat => 'Switch to Chat';

  @override
  String get updateAndroidNote =>
      'When the download finishes, the system asks you to confirm installing over the current app. Your pairings are kept. The first time, allow Posse to install apps in Settings.';

  @override
  String get updateBadPackage =>
      'Can’t open the installer: the package isn’t in the update folder';

  @override
  String updateBanner(String version, String build) {
    return 'New version $version ($build)';
  }

  @override
  String updateBuild(String build) {
    return 'Build $build';
  }

  @override
  String get updateButton => 'Update';

  @override
  String get updateCantInstall => 'Can’t open the installer';

  @override
  String updateCurrent(String version, String build) {
    return 'Current $version ($build)';
  }

  @override
  String get updateDownloadFailed => 'Download failed. Try again later.';

  @override
  String get updateDownloading => 'Downloading…';

  @override
  String updateDownloadingPercent(int percent) {
    return 'Downloading $percent%';
  }

  @override
  String get updateInstallDenied =>
      'Allow Posse to install apps in Settings to update';

  @override
  String get updateIosNote =>
      'After you tap Update, it downloads in the background and the Home Screen icon shows the progress. When it’s installed, the app switches to the new version. Your pairings are kept.';

  @override
  String get updateOpening => 'Opening installer…';

  @override
  String get updateRequesting => 'Requesting installation…';

  @override
  String get updateSkip => 'Don’t remind me about this version';

  @override
  String updateTitle(String version) {
    return 'New Version $version';
  }

  @override
  String uploadSendingFile(int count, int index) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: 'Sending file $index/$count',
      one: 'Sending file',
      zero: 'Sending file',
    );
    return '$_temp0';
  }

  @override
  String uploadSendingImage(int count, int index) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: 'Sending image $index/$count',
      one: 'Sending image',
      zero: 'Sending image',
    );
    return '$_temp0';
  }

  @override
  String get usageCursorModels => 'Cursor Models';

  @override
  String usageIncludedSpend(String used, String limit, String percent) {
    return 'Included usage $used / $limit ($percent)';
  }

  @override
  String get usageListSeparator => ', ';

  @override
  String usageNamedWindow(String name, String window) {
    return '$name $window';
  }

  @override
  String get usageNotConnected => 'The computer isn’t connected';

  @override
  String get usageOldPosse =>
      'Posse on the computer is too old. Update it to refresh.';

  @override
  String usageOnDemand(String used, String stale) {
    return 'On-demand usage $used$stale';
  }

  @override
  String usageOnDemandLimited(String used, String limit, String stale) {
    return 'On-demand usage $used, limit $limit$stale';
  }

  @override
  String get usageOtherModels => 'Other Models';

  @override
  String get usagePerMonth => '/mo';

  @override
  String usageReadingAge(String reset, String time) {
    return '$reset, reading from $time';
  }

  @override
  String get usageRemaining => 'Remaining';

  @override
  String usageResetCards(int count, String stale) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count reset cards',
      one: '$count reset card',
    );
    return '$_temp0$stale';
  }

  @override
  String usageResetCardsExpiring(int count, String date, String stale) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count reset cards',
      one: '$count reset card',
    );
    return '$_temp0, earliest expires $date$stale';
  }

  @override
  String usageShares(String shares, String stale) {
    return 'This week’s usage: $shares$stale';
  }

  @override
  String get usageSheetNote =>
      'Usage updates as agents run on the computer. Cursor usage and Codex reset cards aren’t stored locally; the computer checks them after each one runs.';

  @override
  String usageShownPercent(String kind, String percent) {
    return '$kind $percent';
  }

  @override
  String usageStaleReading(String time) {
    return ' (reading from $time)';
  }

  @override
  String get usageTitle => 'Plan Usage';

  @override
  String get usageUsed => 'Used';

  @override
  String usageWindowDays(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count Days',
      one: '$count Day',
    );
    return '$_temp0';
  }

  @override
  String get usageWindowFiveHours => '5 Hours';

  @override
  String usageWindowHours(int count) {
    String _temp0 = intl.Intl.pluralLogic(
      count,
      locale: localeName,
      other: '$count Hours',
      one: '$count Hour',
    );
    return '$_temp0';
  }

  @override
  String get usageWindowMonth => 'Monthly';

  @override
  String get usageWindowMonthShort => 'mo';

  @override
  String get usageWindowPeriod => 'Current Period';

  @override
  String get usageWindowWeek => 'Weekly';

  @override
  String get usageWindowWeekShort => 'wk';

  @override
  String get workspaceMain => 'Main Workspace';

  @override
  String get workspaceNoSessions => 'No Sessions';

  @override
  String get workspaceTabChanges => 'Changes';

  @override
  String get workspaceTabFiles => 'Files';

  @override
  String get workspaceTabHistory => 'History';
}
