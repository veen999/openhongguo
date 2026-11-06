pluginManagement {
    repositories {
        maven { url = uri("file:///home/hatch/workspace/toolchain/local-maven") }
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositories {
        maven { url = uri("file:///home/hatch/workspace/toolchain/local-maven") }
        google()
        mavenCentral()
    }
}

rootProject.name = "openhongguo"
