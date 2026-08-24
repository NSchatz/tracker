plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "com.nschatz.tracker"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.nschatz.tracker"
        // minSdk is pinned to Android 10 (API 29) — the release that split
        // ACCESS_BACKGROUND_LOCATION out as its own runtime permission. The whole product
        // is built around the background-location model that begins here (two-step
        // "Allow all the time" grant on 30+, foreground-service `type=location` on 34), so
        // the floor is set where that model exists rather than dragging a separate legacy
        // background-location path below it. See android/README.md.
        minSdk = 29
        targetSdk = 34
        versionCode = 1
        versionName = "0.0.1"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        vectorDrawables { useSupportLibrary = true }
    }

    buildTypes {
        debug {
            // Location, tokens and PII must never reach release logs (roadmap §7 / C3).
            // The flag is wired now so collection code added later reads it instead of
            // inventing its own switch; the scaffold logs nothing sensitive.
            buildConfigField("boolean", "VERBOSE_LOGGING", "true")
        }
        release {
            isMinifyEnabled = false
            buildConfigField("boolean", "VERBOSE_LOGGING", "false")
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = libs.versions.composeCompiler.get()
    }

    lint {
        // lint is part of the gate (see the umbrella Makefile `android` target). A lint
        // ERROR fails the build; the scaffold must be clean. warningsAsErrors stays off so
        // an advisory SDK deprecation does not wedge the gate, but abortOnError (default
        // true) means a real error still stops it.
        abortOnError = true
        checkDependencies = true
        // Written so a first failure is inspectable in CI artifacts.
        htmlReport = true
        xmlReport = true
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.work.runtime.ktx)
    implementation(libs.material)
    // C1: the fused location provider that drives the foreground service's continuous updates.
    implementation(libs.play.services.location)
    // ALERT-2: the FCM client, the first-party receive path for a pushed geofence crossing.
    //
    // Note what is NOT here: `com.google.gms.google-services`, the plugin that reads a per-project
    // `google-services.json`. Applying it would make a Firebase PROJECT a build-time requirement, and
    // that file is deployment-specific and cannot be committed - so the gate would only be green on a
    // machine that had one, or on a checkout carrying a placeholder that is worse than nothing. The
    // library alone compiles, assembles, lints and unit-tests without any project configuration; the
    // absence is handled at runtime, where the app reports it has no usable push configuration
    // instead of failing to start.
    implementation(libs.firebase.messaging)

    testImplementation(libs.junit)

    // The instrumented UI suite (S0056). These are androidTest-only: they never reach the shipped
    // APK, and `make check` — assemble + lint + JVM unit tests — is unchanged by their presence.
    // The suite they support runs on a booted emulator via `make verify-ui-android`, which refuses
    // loudly rather than skipping when there is none.
    androidTestImplementation(libs.junit)
    androidTestImplementation(libs.androidx.test.core)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.rules)
    androidTestImplementation(libs.androidx.test.ext.junit)
    androidTestImplementation(libs.androidx.test.uiautomator)
    androidTestImplementation(platform(libs.androidx.compose.bom))
    androidTestImplementation(libs.androidx.compose.ui.test.junit4)
    androidTestImplementation(libs.accessibility.test.framework)
    androidTestImplementation(libs.guava)
    debugImplementation(libs.androidx.compose.ui.test.manifest)
}
