import Foundation

public struct ProjectDraftScope: Codable, Sendable, Equatable {
    public let origin: String
    public let userID: String
    public let accountID: String
    public let projectID: String
    public init(origin: String, userID: String, accountID: String, projectID: String) {
        self.origin = origin; self.userID = userID; self.accountID = accountID; self.projectID = projectID
    }
}

public struct ProjectDraft: Codable, Sendable, Equatable, Identifiable {
    public let id: String
    public let revision: String
    public let scope: ProjectDraftScope
    public let baseVersion: Int64
    public let name: String
    public let settings: ProjectSettings
    public let savedAt: Int64
}

/// One journal per app session, backed by a separate device-only Keychain item. No cookies or
/// document bytes are stored here. Main-actor serialization keeps multiple windows independent.
@MainActor
public final class ProjectDraftJournal {
    public nonisolated static let ttl: Int64 = 30 * 24 * 60 * 60 * 1000
    private let store: any SecretStore
    private let now: @Sendable () -> Int64
    private var generation = 0
    private var records: [ProjectDraft]

    public init(store: any SecretStore, now: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) }) {
        self.store = store
        self.now = now
        records = store.read().flatMap { try? JSONDecoder().decode([ProjectDraft].self, from: $0) } ?? []
        records.removeAll { $0.savedAt <= now() - Self.ttl }
    }

    public func editor(scope: ProjectDraftScope) -> ProjectDraftEditor {
        ProjectDraftEditor(journal: self, scope: scope, generation: generation)
    }

    /// Invalidates existing editors before removing storage, so delayed saves cannot revive it.
    @discardableResult public func clear() -> Bool {
        generation += 1
        records = []
        store.write(nil)
        return store.read() == nil
    }

    fileprivate func list(_ editor: ProjectDraftEditor) -> [ProjectDraft] {
        guard editor.generation == generation else { return [] }
        return records.filter { $0.scope == editor.scope && $0.savedAt > now() - Self.ttl }
            .sorted { $0.savedAt > $1.savedAt }
    }

    fileprivate func save(_ editor: ProjectDraftEditor, version: Int64, name: String, settings: ProjectSettings) -> Bool {
        guard editor.generation == generation, version > 0 else { return false }
        let draft = ProjectDraft(id: editor.id, revision: UUID().uuidString, scope: editor.scope,
            baseVersion: version, name: name, settings: settings, savedAt: now())
        guard let encoded = try? JSONEncoder().encode(draft), encoded.count <= 64 * 1024 else { return false }
        var next = records.filter { $0.id != editor.id && $0.savedAt > now() - Self.ttl }
        guard next.count < 50 else { return false }
        next.append(draft)
        return persist(next)
    }

    fileprivate func remove(_ editor: ProjectDraftEditor, draft: ProjectDraft?) -> Bool {
        guard editor.generation == generation else { return false }
        let id = draft?.id ?? editor.id
        if let record = records.first(where: { $0.id == id }) {
            guard record.scope == editor.scope else { return false }
            if let draft, record.revision != draft.revision { return false }
        }
        return persist(records.filter { $0.id != id && $0.savedAt > now() - Self.ttl })
    }

    private func persist(_ next: [ProjectDraft]) -> Bool {
        guard let data = try? JSONEncoder().encode(next), data.count <= 256 * 1024 else { return false }
        store.write(data)
        guard store.read() == data else { return false }
        records = next
        return true
    }
}

@MainActor
public final class ProjectDraftEditor {
    fileprivate let journal: ProjectDraftJournal
    fileprivate let scope: ProjectDraftScope
    fileprivate let generation: Int
    fileprivate let id = UUID().uuidString
    fileprivate init(journal: ProjectDraftJournal, scope: ProjectDraftScope, generation: Int) {
        self.journal = journal; self.scope = scope; self.generation = generation
    }
    public func list() -> [ProjectDraft] { journal.list(self) }
    @discardableResult public func save(version: Int64, name: String, settings: ProjectSettings) -> Bool {
        journal.save(self, version: version, name: name, settings: settings)
    }
    @discardableResult public func remove(_ draft: ProjectDraft? = nil) -> Bool { journal.remove(self, draft: draft) }
}
