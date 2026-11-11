import 'package:flutter/material.dart';
import 'package:posse_mobile/git/git.dart';
import 'package:posse_mobile/git/status.dart';
import 'package:posse_mobile/ui/widgets.dart';

/// 测试用：改动页（GitPane）单独套上顶栏打开，像工作区页里那样看。App 里改动页只在工作区页里。
class GitScreen extends StatelessWidget {
  const GitScreen({
    super.key,
    required this.title,
    required this.loadStatus,
    required this.loadDiff,
    this.onOpenFile,
  });

  final String title;
  final Future<GitStatusView> Function() loadStatus;
  final Future<GitDiffView> Function(GitFileChange file) loadDiff;
  final void Function(String path)? onOpenFile;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(title)),
      body: SideSafe(
        child: GitPane(
          loadStatus: loadStatus,
          loadDiff: loadDiff,
          onOpenFile: onOpenFile,
        ),
      ),
    );
  }
}
