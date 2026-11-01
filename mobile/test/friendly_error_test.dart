import 'package:flutter_test/flutter_test.dart';
import 'package:posse_mobile/app_state.dart';
import 'package:posse_mobile/protocol/rpc.dart';

void main() {
  test('新版电脑端的中文说明原样显示，不带错误码', () {
    expect(
      friendlyError(RpcException('not_found', '找不到工作区 foo')),
      '找不到工作区 foo',
    );
  });

  test('旧版电脑端只给英文时按错误码换成中文', () {
    expect(
      friendlyError(RpcException('binary_file', 'binary_file')),
      '这是二进制文件，没法当文本打开',
    );
    expect(
      friendlyError(
        RpcException('method_not_found', 'Unknown method: git.diff'),
      ),
      '电脑上的 Posse 是旧版，不支持这个功能：在电脑上更新 Posse',
    );
  });
}
