import Foundation
import Observation

@MainActor
@Observable
public final class ProjectsModel {
    public private(set) var items: [SavedProject] = []
    public private(set) var next: String?
    public private(set) var busy = false
    public private(set) var error: String?
    public var archived = false
    @ObservationIgnored public let dm: Docmatch
    public init(dm: Docmatch) { self.dm = dm }
    public func load(more: Bool = false) async {
        guard !busy else { return }
        busy = true; error = nil
        defer { busy = false }
        do {
            let page = try await dm.api.projects(cursor: more ? next : nil, archived: archived)
            items = more ? items + page.items.filter { p in !items.contains { $0.id == p.id } } : page.items
            next = page.nextCursor
        } catch { fail(error) }
    }
    public func create(name: String) async -> SavedProject? {
        guard !busy else { return nil }
        busy = true; error = nil
        defer { busy = false }
        do { let item = try await dm.api.createProject(name: name); items.insert(item, at: 0); return item }
        catch { fail(error); return nil }
    }
    private func fail(_ e: any Error) {
        if e is CancellationError { return }
        if let e = e as? ApiError, [401,403].contains(e.status) { items = []; next = nil }
        error = (e as? ApiError)?.message ?? dm.texts.t("errors.unknown")
    }
}

public enum ProjectSaveState: Sendable { case saved, pending, saving, offline, conflict }

/// A server-saved project. Edits serialize by version; a conflict never overwrites another tab.
@MainActor
@Observable
public final class ProjectModel {
    public private(set) var project: SavedProject?
    public private(set) var runs: [Run] = []
    public private(set) var events: [ProjectEvent] = []
    public private(set) var name = ""
    public private(set) var settings = ProjectSettings()
    public private(set) var saveState: ProjectSaveState = .saved
    public private(set) var busy = false
    public private(set) var error: String?
    public private(set) var analysis: ProjectAnalysis?
    public private(set) var openedRun: String?
    public var reprocessReason = ""
    @ObservationIgnored public let dm: Docmatch
    @ObservationIgnored public let id: String
    @ObservationIgnored private var saveTask: Task<Void, Never>?
    @ObservationIgnored private var saving = false
    @ObservationIgnored private var dirty = false
    @ObservationIgnored private var approvalKey = ""

    public init(dm: Docmatch, id: String) { self.dm = dm.inProject(id); self.id = id }
    public var hasUnsavedChanges: Bool { dirty || saving }
    public var canEdit: Bool { project?.capabilities.reconcile == true && project?.archivedAt == nil }
    public func load(discardEdits: Bool = false) async {
        guard !busy, !saving, !dirty || discardEdits else { return }
        busy = true; error = nil
        defer { busy = false }
        do {
            let item = try await dm.api.project(id)
            project = item; name = item.name; settings = item.settings ?? ProjectSettings()
            dirty = false; saveState = .saved
            if item.capabilities.readResults { runs = try await dm.api.runs().items } else { runs = [] }
            events = try await dm.api.projectActivity(id).items
        } catch { fail(error) }
    }
    public func rename(_ value: String) { name = value; changed() }
    public func setSettings(_ value: ProjectSettings) { settings = value; changed() }
    private func changed() {
        guard canEdit else { return }
        dirty = true; analysis = nil
        if saveState == .conflict { return }
        saveState = .pending
        saveTask?.cancel()
        saveTask = Task { [weak self] in
            do { try await Task.sleep(nanoseconds: 600_000_000) } catch { return }
            // Saving itself must not be cancelled by another keystroke.
            guard let self else { return }
            self.saveTask = nil
            _ = await self.flush()
        }
    }
    @discardableResult public func flush() async -> Bool {
        guard !saving, saveState != .conflict else { return false }
        saving = true
        defer { saving = false }
        while dirty {
            guard let current = project else { return false }
            let sentName = name, sentSettings = settings
            saveState = .saving
            do {
                let saved = try await dm.api.updateProject(id, version: current.version, name: sentName, settings: sentSettings)
                project = saved
                dirty = name != sentName || settings != sentSettings
                saveState = dirty ? .pending : .saved
                error = nil
            } catch {
                fail(error)
                saveState = (error as? ApiError)?.status == 409 ? .conflict : .offline
                return false
            }
        }
        return true
    }
    public func upload(_ files: [LocalFile], replacesID: String? = nil) async {
        guard !busy, project?.capabilities.upload == true, await flush() else { return }
        busy = true; error = nil; analysis = nil
        defer { busy = false }
        do {
            for file in files {
                let uploaded = try await dm.reconciler.upload(file, replacesID: replacesID)
                var latest = try await dm.api.project(id)
                if let replacesID, let added = latest.documents?.first(where: { $0.uploadId == uploaded.id }) {
                    latest = try await dm.api.replaceProjectDocument(id, document: replacesID, replacement: added.id, version: latest.version)
                }
                project = latest
            }
        } catch { fail(error); if let latest = try? await dm.api.project(id) { project = latest } }
    }
    public func remove(_ doc: ProjectDocument) async {
        guard !busy, canEdit, await flush(), let current = project else { return }
        busy = true; error = nil; analysis = nil
        defer { busy = false }
        do { try await dm.api.removeProjectDocument(id, document: doc.id, version: current.version); project = try await dm.api.project(id) }
        catch { fail(error) }
    }
    public func prepare() async {
        guard !busy, canEdit, await flush(), let current = project else { return }
        busy = true; error = nil
        defer { busy = false }
        do {
            analysis = try await dm.api.estimateProject(id, revision: current.revision, reprocessReason: reprocessReason)
            approvalKey = UUID().uuidString.lowercased()
            if let run = analysis?.run { openedRun = run.id; analysis = nil }
        } catch { fail(error) }
    }
    public func approve() async {
        guard !busy, let analysis, let quote = analysis.quote, !dirty else { return }
        busy = true; error = nil
        defer { busy = false }
        do {
            let result = try await dm.api.reconcileProject(id, revision: analysis.revision, quote: quote, idempotencyKey: approvalKey, reprocessReason: reprocessReason)
            openedRun = result.run?.id; self.analysis = nil
        } catch { fail(error) }
    }
    public func dismissApproval() { analysis = nil }
    public func openedRunShown() { openedRun = nil }
    public func archive(_ value: Bool) async {
        guard !busy, await flush(), let current = project else { return }
        busy = true; defer { busy = false }
        do { project = try await dm.api.updateProject(id, version: current.version, archived: value) } catch { fail(error) }
    }
    private func fail(_ e: any Error) {
        if e is CancellationError { return }
        if let e = e as? ApiError, [401,403,404].contains(e.status) {
            project = nil; runs = []; events = []; name = ""; settings = ProjectSettings(); dirty = false; analysis = nil
        }
        error = (e as? ApiError)?.message ?? dm.texts.t("errors.unknown")
    }
}
