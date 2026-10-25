package com.dengbo.posse.mobile

import android.Manifest
import android.content.ContentValues
import android.content.pm.PackageManager
import android.media.MediaScannerConnection
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import java.io.File

class MainActivity : FlutterActivity() {
    // 安卓 9 及以下等写存储权限的那次调用
    private var pending: (() -> Unit)? = null
    private var pendingResult: MethodChannel.Result? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "posse/downloads")
            .setMethodCallHandler { call, result ->
                if (call.method != "save") {
                    result.notImplemented()
                    return@setMethodCallHandler
                }
                val path = call.argument<String>("path")!!
                val name = call.argument<String>("name")!!
                val run = { saveInBackground(File(path), name, result) }
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q ||
                    checkSelfPermission(Manifest.permission.WRITE_EXTERNAL_STORAGE) ==
                    PackageManager.PERMISSION_GRANTED
                ) {
                    run()
                } else {
                    pending = run
                    pendingResult = result
                    requestPermissions(arrayOf(Manifest.permission.WRITE_EXTERNAL_STORAGE), REQUEST_WRITE)
                }
            }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode != REQUEST_WRITE) return
        val run = pending
        val result = pendingResult
        pending = null
        pendingResult = null
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) {
            run?.invoke()
        } else {
            result?.error("denied", "storage permission denied", null)
        }
    }

    // 复制可能有几百 MB，不放在主线程；回复要回到主线程。
    private fun saveInBackground(src: File, name: String, result: MethodChannel.Result) {
        Thread {
            try {
                val saved = save(src, name)
                runOnUiThread { result.success(saved) }
            } catch (e: Exception) {
                runOnUiThread { result.error("failed", e.message, null) }
            }
        }.start()
    }

    // 存进"下载/Posse"，返回实际的文件名（重名时系统或这里会改成 "x (1).md"）。
    private fun save(src: File, name: String): String {
        val ext = name.substringAfterLast('.', "").lowercase()
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.RELATIVE_PATH, "${Environment.DIRECTORY_DOWNLOADS}/Posse")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: throw IllegalStateException("MediaStore insert failed")
            try {
                contentResolver.openOutputStream(uri)!!.use { out -> src.inputStream().use { it.copyTo(out) } }
                contentResolver.update(uri, ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }, null, null)
            } catch (e: Exception) {
                contentResolver.delete(uri, null, null)
                throw e
            }
            contentResolver.query(uri, arrayOf(MediaStore.Downloads.DISPLAY_NAME), null, null, null)?.use {
                if (it.moveToFirst()) return it.getString(0)
            }
            return name
        }
        @Suppress("DEPRECATION")
        val dir = File(Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS), "Posse")
        dir.mkdirs()
        val base = name.substringBeforeLast('.')
        val dot = if (ext.isEmpty()) "" else ".$ext"
        var dest = File(dir, name)
        var n = 1
        while (dest.exists()) dest = File(dir, "$base (${n++})$dot")
        src.copyTo(dest)
        MediaScannerConnection.scanFile(this, arrayOf(dest.path), arrayOf(mime), null)
        return dest.name
    }

    companion object {
        private const val REQUEST_WRITE = 7301
    }
}
