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
        busy = true
        error = nil
        defer { busy = false }
        do {
            let page = try await dm.api.projects(cursor: more ? next : nil, archived: archived)
            items = more ? items + page.items.filter { p in !items.contains { $0.id == p.id } } : page.items
            next = page.nextCursor
        } catch { fail(error) }
    }
    public func create(name: String) async -> SavedProject? {
        guard !busy else { return nil }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let item = try await dm.api.createProject(name: name)
            items.insert(item, at: 0)
            return item
        } catch {
            fail(error)
            return nil
        }
    }
    private func fail(_ e: any Error) {
        if e is CancellationError { return }
        if let e = e as? ApiError, [401, 403].contains(e.status) {
            items = []
            next = nil
        }
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
    public private(set) var deleted = false
    public private(set) var nextRuns: String?
    public var reprocessReason = ""
    @ObservationIgnored public let dm: Docmatch
    @ObservationIgnored public let id: String
    @ObservationIgnored private var saveTask: Task<Void, Never>?
    @ObservationIgnored private var saving = false
    @ObservationIgnored private var dirty = false
    @ObservationIgnored private var approvalKey = ""

    public init(dm: Docmatch, id: String) {
        self.dm = dm.inProject(id)
        self.id = id
    }
    public var hasUnsavedChanges: Bool { dirty || saving }
    public var canEdit: Bool { project?.capabilities.reconcile == true && project?.archivedAt == nil }
    public func load(discardEdits: Bool = false) async {
        guard !busy, !saving, !dirty || discardEdits else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let item = try await dm.api.project(id)
            project = item
            name = item.name
            settings = item.settings ?? ProjectSettings()
            dirty = false
            saveState = .saved
            if item.capabilities.readResults {
                let page = try await dm.api.runs()
                runs = page.items
                nextRuns = page.nextCursor
            } else {
                runs = []
                nextRuns = nil
            }
            events = try await dm.api.projectActivity(id).items
        } catch { fail(error) }
    }
    public func validateAccess() async {
        guard !busy, !saving else { return }
        do {
            let latest = try await dm.api.project(id)
            if dirty {
                if latest.version != project?.version {
                    saveState = .conflict
                    saveTask?.cancel()
                }
            } else {
                project = latest
                name = latest.name
                settings = latest.settings ?? ProjectSettings()
            }
            if !latest.capabilities.readResults {
                runs = []
                events = []
                analysis = nil
            }
        } catch { fail(error) }
    }
    public func discardPendingEdits() {
        guard !saving else { return }
        saveTask?.cancel()
        saveTask = nil
        dirty = false
        saveState = .saved
        name = project?.name ?? ""
        settings = project?.settings ?? ProjectSettings()
    }
    public func rename(_ value: String) {
        name = NewRunModel.cut(value, Limits.Text.projectName)
        changed()
    }
    public func setSettings(_ value: ProjectSettings) {
        settings = value
        changed()
    }
    private func changed() {
        guard canEdit else { return }
        dirty = true
        analysis = nil
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
            let sentName = name
            let sentSettings = settings
            saveState = .saving
            do {
                let saved = try await dm.api.updateProject(
                    id, version: current.version, name: sentName, settings: sentSettings)
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
    public func add(_ picked: [PickedFile], replacesID: String? = nil) async {
        guard !busy else { return }
        busy = true
        let (files, problems) = await NewRunModel.checkedFiles(picked, texts: dm.texts)
        busy = false
        // A partial set requires the user to correct it before any transfer starts.
        guard problems.isEmpty else {
            error = problems.joined(separator: "\n")
            return
        }
        await upload(files, replacesID: replacesID)
    }
    public func applyReplacement(_ doc: ProjectDocument) async {
        guard !busy, canEdit, let old = doc.pendingReplacementId, await flush(), let current = project else { return }
        busy = true
        defer { busy = false }
        do {
            project = try await dm.api.replaceProjectDocument(
                id, document: old, replacement: doc.id, version: current.version)
        } catch { fail(error) }
    }
    public func upload(_ files: [LocalFile], replacesID: String? = nil) async {
        guard !busy, project?.capabilities.upload == true, await flush() else { return }
        busy = true
        error = nil
        analysis = nil
        defer { busy = false }
        do {
            for file in files {
                let uploaded = try await dm.reconciler.upload(file, replacesID: replacesID)
                var latest = try await dm.api.project(id)
                if let replacesID, let added = latest.documents?.first(where: { $0.uploadId == uploaded.id }) {
                    latest = try await dm.api.replaceProjectDocument(
                        id, document: replacesID, replacement: added.id, version: latest.version)
                }
                project = latest
            }
        } catch {
            fail(error)
            if let latest = try? await dm.api.project(id) { project = latest }
        }
    }
    public func remove(_ doc: ProjectDocument) async {
        guard !busy, canEdit, await flush(), let current = project else { return }
        busy = true
        error = nil
        analysis = nil
        defer { busy = false }
        do {
            try await dm.api.removeProjectDocument(id, document: doc.id, version: current.version)
            project = try await dm.api.project(id)
        } catch { fail(error) }
    }
    public func prepare() async {
        guard !busy, canEdit, await flush(), let current = project else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            analysis = try await dm.api.estimateProject(
                id, revision: current.revision, reprocessReason: reprocessReason)
            approvalKey = UUID().uuidString.lowercased()
            if let run = analysis?.run {
                openedRun = run.id
                analysis = nil
            }
        } catch { fail(error) }
    }
    public func approve() async {
        guard !busy, let analysis, let quote = analysis.quote, !dirty else { return }
        busy = true
        error = nil
        defer { busy = false }
        do {
            let result = try await dm.api.reconcileProject(
                id, revision: analysis.revision, quote: quote, idempotencyKey: approvalKey,
                reprocessReason: reprocessReason)
            openedRun = result.run?.id
            self.analysis = nil
        } catch { fail(error) }
    }
    public func dismissApproval() { analysis = nil }
    public func openedRunShown() { openedRun = nil }
    public func archive(_ value: Bool) async {
        guard !busy, await flush(), let current = project else { return }
        busy = true
        defer { busy = false }
        do { project = try await dm.api.updateProject(id, version: current.version, archived: value) } catch {
            fail(error)
        }
    }
    public func moreRuns() async {
        guard !busy, let nextRuns else { return }
        busy = true
        defer { busy = false }
        do {
            let page = try await dm.api.runs(cursor: nextRuns)
            runs += page.items.filter { run in !runs.contains { $0.id == run.id } }
            self.nextRuns = page.nextCursor
        } catch { fail(error) }
    }
    public func share(visibility: String, users: [String]) async {
        guard !busy, await flush(), let current = project else { return }
        busy = true
        defer { busy = false }
        do {
            project = try await dm.api.shareProject(id, version: current.version, visibility: visibility, users: users)
        } catch { fail(error) }
    }
    public func delete() async {
        guard !busy, await flush(), let current = project else { return }
        busy = true
        defer { busy = false }
        do {
            try await dm.api.deleteProject(id, version: current.version)
            deleted = true
        } catch { fail(error) }
    }
    private func fail(_ e: any Error) {
        if e is CancellationError { return }
        if let e = e as? ApiError, [401, 403, 404].contains(e.status) {
            project = nil
            runs = []
            events = []
            name = ""
            settings = ProjectSettings()
            dirty = false
            analysis = nil
        }
        error = (e as? ApiError)?.message ?? dm.texts.t("errors.unknown")
    }
}
