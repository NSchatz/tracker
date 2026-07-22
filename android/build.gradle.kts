// Root build script. Plugins are declared here `apply false` so the version catalog pins
// them once; the app module applies them.
plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.android) apply false
}
