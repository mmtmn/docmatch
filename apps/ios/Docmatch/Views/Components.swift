import SwiftUI

struct DmCard<Content: View>: View {
    private let content: Content
    init(@ViewBuilder content: () -> Content) { self.content = content() }
    var body: some View {
        VStack(alignment: .leading, spacing: 14) { content }
            .padding(16).frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(DmColors.bgCanvas), in: RoundedRectangle(cornerRadius: DmSize.radiusLg))
            .overlay(RoundedRectangle(cornerRadius: DmSize.radiusLg).stroke(Color(DmColors.borderDefault), lineWidth: 1))
    }
}

struct DmPage<Content: View>: View {
    private let content: Content
    init(@ViewBuilder content: () -> Content) { self.content = content() }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) { content }
                .frame(maxWidth: 720, alignment: .leading).padding(20)
                .frame(maxWidth: .infinity)
        }
        .background(Color(DmColors.bgCanvas))
        .scrollDismissesKeyboard(.interactively)
    }
}

struct DmButtonStyle: ButtonStyle {
    enum Kind { case primary, secondary, destructive }
    let kind: Kind
    @Environment(\.isEnabled) private var enabled
    init(_ kind: Kind = .primary) { self.kind = kind }
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold)).multilineTextAlignment(.center)
            .padding(.horizontal, 16).padding(.vertical, 12)
            .frame(maxWidth: .infinity, minHeight: 48)
            .foregroundStyle(Color(kind == .primary ? DmColors.brandOnPrimary : kind == .destructive ? DmColors.statusDanger : DmColors.textPrimary))
            .background(Color(kind == .primary ? DmColors.brandPrimary : DmColors.bgCanvas), in: RoundedRectangle(cornerRadius: DmSize.radiusMd))
            .overlay(RoundedRectangle(cornerRadius: DmSize.radiusMd).stroke(Color(kind == .primary ? DmColors.brandPrimary : DmColors.borderDefault), lineWidth: 1))
            .opacity(!enabled ? 0.5 : configuration.isPressed ? 0.75 : 1)
    }
}

struct DmBrand: View {
    @Environment(\.texts) private var texts
    var body: some View { Text(texts.t("meta.site_name")).font(.title3.bold()).tracking(-0.6).foregroundStyle(Color(DmColors.textPrimary)) }
}

struct DmHeading: View {
    let title: String
    var subtitle: String? = nil
    var hero = false
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title).font(hero ? .largeTitle.bold() : .title2.bold()).tracking(-0.6)
                .foregroundStyle(Color(DmColors.textPrimary)).fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            if let subtitle { Text(subtitle).font(.subheadline).lineSpacing(4).foregroundStyle(Color(DmColors.textMuted)) }
        }
    }
}

struct DmIconTile: View {
    let name: String
    var body: some View {
        Image(systemName: name).font(.system(size: 24, weight: .medium))
            .frame(width: 48, height: 48).foregroundStyle(Color(DmColors.brandPrimary))
            .background(Color(DmColors.brandSubtle), in: RoundedRectangle(cornerRadius: 12))
            .accessibilityHidden(true)
    }
}

struct DmNotice: View {
    let message: String
    var error = false
    var body: some View {
        Text(message).font(.subheadline).frame(maxWidth: .infinity, alignment: .leading).padding(12)
            .foregroundStyle(Color(error ? DmColors.statusDanger : DmColors.textMuted))
            .background(Color(error ? DmColors.statusDangerSoft : DmColors.bgSurface), in: RoundedRectangle(cornerRadius: DmSize.radiusMd))
    }
}

struct DmDisclosure<Content: View>: View {
    let title: String
    private let content: Content
    @State private var expanded = false
    init(_ title: String, @ViewBuilder content: () -> Content) { self.title = title; self.content = content() }
    var body: some View {
        DmCard {
            DisclosureGroup(isExpanded: $expanded) {
                if expanded { VStack(alignment: .leading, spacing: 14) { content }.padding(.top, 12) }
            } label: {
                Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(Color(DmColors.textPrimary))
                    .padding(.vertical, 6)
            }
        }
    }
}

struct DmUploadTarget: View {
    var compact = false
    let pick: () -> Void
    @Environment(\.texts) private var texts
    var body: some View {
        Button(action: pick) {
            VStack(spacing: 10) {
                Image(systemName: "square.and.arrow.up").font(.title2).foregroundStyle(Color(DmColors.brandPrimary))
                Text(texts.t("mobile.pick_files")).font(.headline).foregroundStyle(Color(DmColors.textPrimary))
                Text(texts.t("mobile.pick_hint")).font(.subheadline).foregroundStyle(Color(DmColors.textMuted))
                if !compact { Text(texts.t("dropzone.accepted")).font(.caption).lineSpacing(3).foregroundStyle(Color(DmColors.textMuted)) }
            }
            .padding(20).frame(maxWidth: .infinity, minHeight: compact ? 112 : 180)
            .background(Color(DmColors.bgSurface), in: RoundedRectangle(cornerRadius: 16))
            .overlay(RoundedRectangle(cornerRadius: 16).stroke(Color(DmColors.borderStrong), style: StrokeStyle(lineWidth: 1.5, dash: [6, 4])))
        }.buttonStyle(.plain)
            .accessibilityLabel(texts.t("mobile.pick_files"))
            .accessibilityHint(texts.t("mobile.pick_hint"))
            .accessibilityIdentifier("pick-files")
    }
}

struct DmProcessingScene: View {
    let label: String
    @State private var floating = false
    @Environment(\.texts) private var texts
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        VStack(spacing: 22) {
            HStack(spacing: 22) {
                DmIconTile(name: "doc.text").offset(y: reduceMotion ? 0 : floating ? -4 : 4)
                Image(systemName: "arrow.right").foregroundStyle(Color(DmColors.borderStrong))
                DmIconTile(name: "checkmark.seal").offset(y: reduceMotion ? 0 : floating ? 4 : -4)
            }
            .accessibilityHidden(true)
            .animation(reduceMotion ? nil : .easeInOut(duration: 1.6).repeatForever(autoreverses: true), value: floating)
            Text(texts.t("mobile.process_title")).font(.title2.bold()).multilineTextAlignment(.center)
            Text(texts.t("mobile.process_text")).font(.subheadline).foregroundStyle(Color(DmColors.textMuted)).multilineTextAlignment(.center)
            DmCard {
                HStack(spacing: 12) {
                    ProgressView().accessibilityHidden(true)
                    Text(label).font(.system(.subheadline, design: .monospaced)).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .padding(.vertical, 24).frame(maxWidth: .infinity)
        .onAppear { floating = true }
    }
}

private struct DmField: ViewModifier {
    func body(content: Content) -> some View {
        content.padding(14).frame(minHeight: 48)
            .background(Color(DmColors.bgCanvas), in: RoundedRectangle(cornerRadius: DmSize.radiusMd))
            .overlay(RoundedRectangle(cornerRadius: DmSize.radiusMd).stroke(Color(DmColors.borderStrong), lineWidth: 1))
    }
}

extension View {
    func dmField() -> some View { modifier(DmField()) }
    func dmNavigation(_ title: String) -> some View {
        navigationTitle("").navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Color(DmColors.bgCanvas), for: .navigationBar).toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) { DmBrand().fixedSize() }
                ToolbarItem(placement: .topBarTrailing) { Text(title).font(.caption).foregroundStyle(Color(DmColors.textMuted)) }
            }
    }
}
