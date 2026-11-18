// The Kotlin of the native module droidvpn: an Android library, which mygo
// build adds to the app's Gradle build. The build sets its SDK levels, Java
// and Kotlin, and its dependency on MyGo's host library (dev.mygo), which
// brings coroutines: add only what the implementation uses.
plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "eggbox.internal.phone.droidvpn"
}

dependencies {
    // The person's consent to the VPN is asked through the activity's result
    // registry; the same version as MyGo's host library.
    implementation("androidx.activity:activity:1.10.1")
}
