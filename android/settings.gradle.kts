// tracker Android client — Gradle settings.
//
// Repositories are declared centrally (FAIL_ON_PROJECT_REPOS): the app modules may not
// add their own, so every dependency resolves from exactly these two hosts. Under the
// umbrella's egress lockdown those hosts (Google's Maven + Maven Central) are the ones the
// CI/gate env must allow-list — see android/README.md.
pluginManagement {
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "tracker"
include(":app")
