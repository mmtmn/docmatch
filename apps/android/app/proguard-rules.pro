# kotlinx.serialization: keep the generated serializers of the API models.
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.AnnotationsKt
-keep,includedescriptorclasses class br.com.docmatch.core.**$$serializer { *; }
-keepclassmembers class br.com.docmatch.core.** {
    *** Companion;
}
-keepclasseswithmembers class br.com.docmatch.core.** {
    kotlinx.serialization.KSerializer serializer(...);
}
# OkHttp ships its own rules; these silence optional platform classes.
-dontwarn org.bouncycastle.**
-dontwarn org.conscrypt.**
-dontwarn org.openjsse.**
