import DocmatchKit
import SwiftUI
import UIKit

private struct TextsKey: EnvironmentKey {
    static let defaultValue: Texts = {
        do {
            return try Texts(json: Data("{}".utf8))
        } catch {
            fatalError("textos vazios inválidos")
        }
    }()
}

extension EnvironmentValues {
    /// The pt-BR texts of packages/locales, the same file the site and Android read.
    var texts: Texts {
        get { self[TextsKey.self] }
        set { self[TextsKey.self] = newValue }
    }
}

struct LoadingView: View {
    let label: String

    var body: some View {
        VStack(spacing: DmSize.space2) {
            ProgressView()
            Text(label)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityElement(children: .combine)
    }
}

struct ErrorView: View {
    let message: String
    let retry: () -> Void
    @Environment(\.texts) private var texts

    init(message: String, retry: @escaping () -> Void) {
        self.message = message
        self.retry = retry
    }

    var body: some View {
        VStack(spacing: DmSize.space2) {
            Text(message)
                .multilineTextAlignment(.center)
                .foregroundStyle(Color(DmColors.statusDanger))
            Button(texts.t("mobile.retry"), action: retry)
                .buttonStyle(.borderedProminent)
        }
        .padding(DmSize.space3)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// The site's character counter, from 80% of the limit.
struct CounterText: View {
    let count: Int
    let max: Int
    @Environment(\.texts) private var texts

    init(count: Int, max: Int) {
        self.count = count
        self.max = max
    }

    var body: some View {
        if let text = Labels.counter(texts, count: count, max: max) {
            Text(text)
                .font(.caption)
                .foregroundStyle(count >= max ? Color(DmColors.statusDanger) : .secondary)
        }
    }
}

/// An exported report handed to the share sheet.
struct ShareItem: Identifiable {
    let id = UUID()
    let url: URL
}

struct ActivityView: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: [url], applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
