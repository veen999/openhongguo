#!/bin/bash
exec > ~/workspace/toolchain-dl/aar2.log 2>&1
set -xe
export PATH=~/workspace/toolchain/go1.26.0/bin:~/workspace/toolchain/gopath/bin:$PATH
export GOPATH=~/workspace/toolchain/gopath GOCACHE=~/workspace/toolchain/gocache GOMODCACHE=~/workspace/toolchain/gopath/pkg/mod
export JAVA_HOME=~/workspace/toolchain/jdk17; export PATH=$JAVA_HOME/bin:$PATH
export LANG=C.UTF-8 LC_ALL=C.UTF-8 JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8
export ANDROID_HOME=~/workspace/android-sdk ANDROID_SDK_ROOT=~/workspace/android-sdk ANDROID_NDK_HOME=~/workspace/android-sdk/ndk
H=~/workspace/hongguo-sdk-fork/bindings/mobile
rm -rf $H/build; mkdir -p $H/build/android
(cd $H/core && gomobile bind -target android -androidapi 21 -javapkg dev.openhongguo -trimpath -ldflags="-s -w" -o $H/build/android/core.aar .)
(cd $H/build/android && unzip -q core.aar classes.jar 'jni/*' && mv classes.jar core.jar)
rm -rf $H/android/libs/core.jar $H/android/src/main/jniLibs; mkdir -p $H/android/libs $H/android/src/main/jniLibs
cp $H/build/android/core.jar $H/android/libs/core.jar
cp -R $H/build/android/jni/. $H/android/src/main/jniLibs/
(cd $H/android && ~/workspace/toolchain/gradle-9.3.1/bin/gradle --quiet test assembleRelease)
ls -lh $H/android/build/outputs/aar/
echo AAR2_DONE
