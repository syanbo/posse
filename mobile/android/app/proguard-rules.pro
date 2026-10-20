# shared_preferences_android 2.5 起由 Dart 经 JNI 直接调 Kotlin（Pigeon JNI），按名字找类、按完整签名找方法，
# 插件自己没带保留规则（到 2.5.1 都没有）。release 构建的 R8 会把这些名字改掉（实测签名里的 Continuation
# 成了 zd、CoroutineSingletons 被删），冷启动读偏好就抛 NoSuchMethodError / ClassNotFoundException，
# 存着的配对和设置都读不出来、也写不进去。
-keep class io.flutter.plugins.sharedpreferences.** { *; }
-keep class kotlin.jvm.internal.DefaultConstructorMarker
# package:jni 处理挂起函数时按名字用到的 Kotlin 类和成员（jni 1.1.0 的 lib 里写死的字符串：
# Continuation、Unit.INSTANCE、Result.value、Result$Failure.exception、IntrinsicsKt.getCOROUTINE_SUSPENDED、
# CoroutineSingletons）。IntrinsicsKt 是多文件门面，整个包保住
-keep interface kotlin.coroutines.Continuation { *; }
-keep class kotlin.Unit { *; }
-keep class kotlin.Result { *; }
-keep class kotlin.Result$Failure { *; }
-keep class kotlin.coroutines.intrinsics.** { *; }
