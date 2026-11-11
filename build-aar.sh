#!/bin/bash
exec > ~/workspace/toolchain-dl/aar-build.log 2>&1
set -x
export PATH=~/workspace/toolchain/go1.26.0/bin:$PATH
export GOPATH=~/workspace/toolchain/gopath GOCACHE=~/workspace/toolchain/gocache GOMODCACHE=~/workspace/toolchain/gopath/pkg/mod
export JAVA_HOME=~/workspace/toolchain/jdk17
export PATH=$JAVA_HOME/bin:$PATH
export LANG=C.UTF-8 LC_ALL=C.UTF-8 JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8
export ANDROID_HOME=~/workspace/android-sdk ANDROID_SDK_ROOT=~/workspace/android-sdk
export ANDROID_NDK_HOME=~/workspace/android-sdk/ndk
export GRADLE_OPTS="$(cat ~/workspace/toolchain/proxy-opts.txt)"
go version
# gomobile already installed
export PATH=$GOPATH/bin:$PATH
gomobile init 2>&1 | tail -2
cd ~/workspace/hongguo-sdk-fork/bindings/mobile
./build.sh android 2>&1 | tail -30
ls -lh android/build/outputs/aar/ 2>/dev/null
echo AAR_BUILD_DONE
