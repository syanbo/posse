package com.dengbo.posse.mobile

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.core.content.FileProvider
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.MethodChannel
import java.io.File

/// 单独一个子类：插件里也可能声明 androidx 的 FileProvider，同名的 provider 合并清单时会冲突。
class UpdateFileProvider : FileProvider()

/// 安卓的应用内更新：Dart 把 latest.json 指的 APK 下载到 dir 里，这里交给系统安装器覆盖安装。
/// 同一证书、同一包名覆盖安装不清数据，配对还在。
class AppUpdate(private val activity: Activity, messenger: BinaryMessenger) {
    private val dir = File(activity.cacheDir, "update")
    // 去系统设置允许安装未知应用的那次 install，回到 App 时接着装
    private var waiting: Pair<File, MethodChannel.Result>? = null

    init {
        cleanup()
        MethodChannel(messenger, "posse/update").setMethodCallHandler { call, result ->
            when (call.method) {
                "dir" -> {
                    dir.mkdirs()
                    result.success(dir.path)
                }
                "install" -> {
                    val file = File(call.arguments as String)
                    if (file.parentFile?.canonicalPath != dir.canonicalPath || !file.isFile) {
                        result.error("bad_args", "the APK is not in the update directory", null)
                        return@setMethodCallHandler
                    }
                    install(file, result)
                }
                else -> result.notImplemented()
            }
        }
    }

    private fun install(file: File, result: MethodChannel.Result) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !activity.packageManager.canRequestPackageInstalls()) {
            waiting?.second?.success("denied")
            waiting = file to result
            activity.startActivity(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${activity.packageName}")),
            )
            return
        }
        try {
            launch(file)
            result.success("installer")
        } catch (e: Exception) {
            result.error("failed", e.message, null)
        }
    }

    fun resumed() {
        val (file, result) = waiting ?: return
        waiting = null
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !activity.packageManager.canRequestPackageInstalls()) {
            result.success("denied")
            return
        }
        try {
            launch(file)
            result.success("installer")
        } catch (e: Exception) {
            result.error("failed", e.message, null)
        }
    }

    private fun launch(file: File) {
        val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.update", file)
        activity.startActivity(
            Intent(Intent.ACTION_VIEW)
                .setDataAndType(uri, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK),
        )
    }

    /// 装好新版后旧包就没用了。文件名带构建号（Posse-<构建号>.apk），不比现在新的删掉；
    /// 还没装的新包留着：安装器可能还开着、正在读它。
    private fun cleanup() {
        val current = try {
            val info = activity.packageManager.getPackageInfo(activity.packageName, 0)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) info.longVersionCode else @Suppress("DEPRECATION") info.versionCode.toLong()
        } catch (e: Exception) {
            return
        }
        dir.listFiles()?.forEach { f ->
            val build = f.name.removePrefix("Posse-").substringBefore('.').toLongOrNull()
            if (build == null || build <= current) f.delete()
        }
    }
}
