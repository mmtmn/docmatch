plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.jvm) apply false
    alias(libs.plugins.kotlin.android) apply false
    alias(libs.plugins.kotlin.serialization) apply false
    alias(libs.plugins.kotlin.compose) apply false
}

// Commit resolved transitive versions as well as the direct version catalog. This makes
// clean builds reproducible and gives the local vulnerability scanner a complete inventory.
subprojects {
    dependencyLocking {
        lockAllConfigurations()
    }
    // AGP's Unified Test Platform pins older libraries separately for each host plugin.
    // Keep the compatible release lines patched, including the local emulator test tooling.
    configurations.configureEach {
        if (name.startsWith("_internal-unified-test-platform-")) {
            resolutionStrategy.eachDependency {
                when {
                    requested.group == "io.netty" && requested.version?.startsWith("4.1.") == true ->
                        useVersion("4.1.140.Final")
                    requested.group == "com.google.guava" && requested.name == "guava" ->
                        useVersion("33.8.0-jre")
                    requested.group == "com.google.protobuf" && requested.version?.startsWith("3.") == true ->
                        useVersion("3.25.9")
                }
            }
        }
    }
}
