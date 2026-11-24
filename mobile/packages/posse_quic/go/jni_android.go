//go:build android

package main

/*
#include <jni.h>
#include <stdint.h>
#include <pthread.h>

static pthread_mutex_t g_ts_jni_mu = PTHREAD_MUTEX_INITIALIZER;
static JavaVM *g_ts_jvm = NULL;
static jclass g_ts_cls = NULL;
static jmethodID g_ts_bind_method = NULL;

static int ts_jni_init(JNIEnv *env, jclass cls) {
	pthread_mutex_lock(&g_ts_jni_mu);
	if (g_ts_jvm == NULL) {
		if ((*env)->GetJavaVM(env, &g_ts_jvm) != JNI_OK) {
			pthread_mutex_unlock(&g_ts_jni_mu);
			return -1;
		}
	}
	if (g_ts_cls == NULL) {
		jclass globalCls = (jclass)(*env)->NewGlobalRef(env, cls);
		if (globalCls == NULL) {
			pthread_mutex_unlock(&g_ts_jni_mu);
			return -2;
		}
		jmethodID mid = (*env)->GetStaticMethodID(env, globalCls, "bindSocketToActiveNetwork", "(I)I");
		if (mid == NULL) {
			if ((*env)->ExceptionCheck(env)) {
				(*env)->ExceptionClear(env);
			}
			(*env)->DeleteGlobalRef(env, globalCls);
			pthread_mutex_unlock(&g_ts_jni_mu);
			return -3;
		}
		g_ts_cls = globalCls;
		g_ts_bind_method = mid;
	}
	pthread_mutex_unlock(&g_ts_jni_mu);
	return 0;
}

static int ts_jni_bind_socket(int32_t fd) {
	pthread_mutex_lock(&g_ts_jni_mu);
	JavaVM *jvm = g_ts_jvm;
	jclass cls = g_ts_cls;
	jmethodID mid = g_ts_bind_method;
	pthread_mutex_unlock(&g_ts_jni_mu);
	if (jvm == NULL || cls == NULL || mid == NULL) {
		return 0; // 未初始化时跳过，不阻塞正常发包
	}
	JNIEnv *env = NULL;
	int attached = 0;
	jint getRes = (*jvm)->GetEnv(jvm, (void **)&env, JNI_VERSION_1_6);
	if (getRes == JNI_EDETACHED) {
		if ((*jvm)->AttachCurrentThread(jvm, &env, NULL) != JNI_OK || env == NULL) {
			return -1;
		}
		attached = 1;
	} else if (getRes != JNI_OK || env == NULL) {
		return -2;
	}
	jint rc = (*env)->CallStaticIntMethod(env, cls, mid, (jint)fd);
	if ((*env)->ExceptionCheck(env)) {
		(*env)->ExceptionClear(env);
		rc = -3;
	}
	if (attached) {
		(*jvm)->DetachCurrentThread(jvm);
	}
	return (int)rc;
}
*/
import "C"

import (
	"fmt"
	"log"
	"unsafe"

	"posse/internal/tslink"
)

// 安卓 App 的 MainActivity.quicReset，在 Flutter 引擎销毁前调，见 hq_reset。
//
//export Java_com_neolabsapp_posse_mobile_MainActivity_quicReset
func Java_com_neolabsapp_posse_mobile_MainActivity_quicReset(env, cls unsafe.Pointer) {
	hq_reset()
}

// 安卓 App 的 MainActivity.tsInit，在引擎初始化时注册底层网络绑定钩子（Network.bindSocket）。
//
//export Java_com_neolabsapp_posse_mobile_MainActivity_tsInit
func Java_com_neolabsapp_posse_mobile_MainActivity_tsInit(env, cls unsafe.Pointer) {
	if rc := C.ts_jni_init((*C.JNIEnv)(env), (C.jclass)(cls)); rc != 0 {
		log.Printf("posse_quic: ts_jni_init failed (rc=%d)", int(rc))
		return
	}
	tslink.SetAndroidBindToNetwork(func(fd int) error {
		if rc := C.ts_jni_bind_socket(C.int32_t(fd)); rc != 0 {
			return fmt.Errorf("android bindSocket failed (rc=%d)", int(rc))
		}
		return nil
	})
}
