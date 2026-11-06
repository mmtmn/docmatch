import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
}

// Everything that changes per environment comes from Gradle properties (-P or
// ~/.gradle/gradle.properties). Debug defaults to the emulator's view of `make local` (10.0.2.2 is
// the host machine); a release build has no default and must be told its API origin
// (make release reads it from the Terraform outputs, like the site's deploy).
fun prop(name: String, default: String): String = providers.gradleProperty(name).getOrElse(default)

val apiBaseUrl: String? = providers.gradleProperty("docmatch.apiBaseUrl").orNull?.trim()?.ifEmpty { null }

android {
    namespace = "br.com.docmatch.app"
    compileSdk = 36

    defaultConfig {
        applicationId = "br.com.docmatch"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "1.0.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        // Web (server) client id of Google sign-in; empty hides the Google button.
        buildConfigField("String", "GOOGLE_SERVER_CLIENT_ID", "\"${prop("docmatch.googleServerClientId", "")}\"")
    }

    signingConfigs {
        // The Play upload key (Play App Signing keeps the app key). Path and passwords come from the
        // environment of the machine that builds the release, never from the repository.
        providers.environmentVariable("DOCMATCH_UPLOAD_KEYSTORE").orNull?.let { store ->
            create("upload") {
                storeFile = file(store)
                storePassword = providers.environmentVariable("DOCMATCH_UPLOAD_KEYSTORE_PASSWORD").orNull
                keyAlias = providers.environmentVariable("DOCMATCH_UPLOAD_KEY_ALIAS").orNull
                keyPassword = providers.environmentVariable("DOCMATCH_UPLOAD_KEY_PASSWORD").orNull
            }
        }
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
            versionNameSuffix = "-debug"
            buildConfigField("String", "API_BASE_URL", "\"${apiBaseUrl ?: "http://10.0.2.2:8080"}\"")
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = signingConfigs.findByName("upload")
            buildConfigField("String", "API_BASE_URL", "\"${apiBaseUrl.orEmpty()}\"")
        }
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    sourceSets["main"].apply {
        // The same pt-BR texts as the site, read at runtime by br.com.docmatch.core.Texts.
        assets.srcDir(rootProject.file("../../packages/locales"))
    }
    sourceSets["androidTest"].apply {
        // The public Vantara twin and its answer key, the same files the site's local e2e uses.
        assets.srcDir(providers.gradleProperty("docmatch.testDataDir").orNull?.let { file(it) }
            ?: rootProject.file("../../testdata/golden/vantara-twin"))
    }

    testOptions {
        // make e2e-local: a headless emulator Gradle manages (KVM), against `make local`.
        managedDevices {
            localDevices {
                create("pixel6Api34") {
                    device = "Pixel 6"
                    apiLevel = 34
                    systemImageSource = "aosp-atd"
                }
            }
        }
    }

    packaging {
        resources.excludes += setOf("/META-INF/{AL2.0,LGPL2.1}", "/META-INF/versions/9/OSGI-INF/MANIFEST.MF")
    }

    lint {
        warningsAsErrors = false
        abortOnError = true
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_17)
    }
}

dependencies {
    implementation(project(":core"))
    implementation(libs.coroutines.android)
    implementation(libs.core.ktx)
    implementation(libs.activity.compose)
    implementation(libs.lifecycle.viewmodel.compose)
    implementation(libs.lifecycle.runtime.compose)
    implementation(libs.navigation.compose)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.ui.tooling.preview)
    implementation(libs.compose.material3)
    implementation(libs.compose.material.icons)
    implementation(libs.credentials)
    implementation(libs.credentials.play)
    implementation(libs.googleid)
    debugImplementation(libs.compose.ui.tooling)
    debugImplementation(libs.compose.ui.test.manifest)
    testImplementation(libs.junit)
    androidTestImplementation(platform(libs.compose.bom))
    androidTestImplementation(libs.compose.ui.test.junit4)
    androidTestImplementation(libs.androidx.test.core)
    androidTestImplementation(libs.androidx.test.rules)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.junit)
    androidTestImplementation(libs.espresso.intents)
    androidTestImplementation(kotlin("test"))
}

// A release build must name the API it talks to; failing here beats an app with no server.
tasks.matching { it.name == "preReleaseBuild" }.configureEach {
    val url = apiBaseUrl
    doFirst {
        check(url != null && url.startsWith("https://")) {
            "release: passe -Pdocmatch.apiBaseUrl=https://<origem da API> (make release lê dos outputs do Terraform)"
        }
    }
}
