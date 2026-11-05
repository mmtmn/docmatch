// docmatch para Android. :core é Kotlin puro (cliente da API, sessão, fluxo de conciliação,
// textos) e roda os testes na JVM sem o Android SDK; :app é a interface em Jetpack Compose.
pluginManagement {
    repositories {
        google()
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

rootProject.name = "docmatch"

include(":core")
// The app needs the Android SDK; `-Pdocmatch.coreOnly=true` builds and tests :core without it.
if (providers.gradleProperty("docmatch.coreOnly").orNull != "true") {
    include(":app")
}
