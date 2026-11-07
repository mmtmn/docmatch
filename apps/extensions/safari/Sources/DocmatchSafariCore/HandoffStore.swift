import Darwin
import Foundation

struct Handoff: Codable {
    let id: UUID
    let profile: String
    let state: String
    let challenge: String
    let createdAt: Date
    let expiresAt: Date
    var code: String?
    var codeExpiresAt: Date?
    var error: String?
}

enum HandoffError: Error { case unavailable, invalid, expired, profile, busy }

struct HandoffStore {
    let directory: URL
    let callbackURL: URL
    init(group: String, callbackURL: URL) throws {
        guard let root = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else {
            throw HandoffError.unavailable
        }
        try self.init(
            directory: root.appendingPathComponent("Authorization", isDirectory: true), callbackURL: callbackURL)
    }
    init(directory: URL, callbackURL: URL) throws {
        self.directory = directory
        self.callbackURL = callbackURL
        try FileManager.default.createDirectory(
            at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
    }
    static func valid(_ value: String) -> Bool {
        value.utf8.count == 43 && value.range(of: "^[A-Za-z0-9_-]{43}$", options: .regularExpression) != nil
    }
    func locked<T>(_ work: () throws -> T) throws -> T {
        let lock = directory.appendingPathComponent(".lock").path
        let fd = open(lock, O_CREAT | O_RDWR | O_NOFOLLOW, mode_t(S_IRUSR | S_IWUSR))
        guard fd >= 0 else { throw HandoffError.unavailable }
        defer { close(fd) }
        guard flock(fd, LOCK_EX) == 0 else { throw HandoffError.unavailable }
        defer { flock(fd, LOCK_UN) }
        return try work()
    }
    private func file(_ id: UUID) -> URL { directory.appendingPathComponent(id.uuidString + ".json") }
    private func load(_ id: UUID, now: Date) throws -> Handoff {
        let handoff = try JSONDecoder().decode(Handoff.self, from: Data(contentsOf: file(id)))
        guard handoff.id == id, handoff.expiresAt > now else { throw HandoffError.expired }
        return handoff
    }
    private func write(_ handoff: Handoff) throws {
        try JSONEncoder().encode(handoff).write(to: file(handoff.id), options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file(handoff.id).path)
    }
    func begin(profile: String, state: String, challenge: String, now: Date = Date()) throws -> Handoff {
        guard Self.valid(state), Self.valid(challenge), !profile.isEmpty else { throw HandoffError.invalid }
        return try locked {
            var pending = 0
            for url in try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            where url.pathExtension == "json" {
                guard let item = try? JSONDecoder().decode(Handoff.self, from: Data(contentsOf: url)),
                    item.expiresAt > now
                else {
                    try? FileManager.default.removeItem(at: url)
                    continue
                }
                if item.profile == profile { pending += 1 }
            }
            guard pending < 10 else { throw HandoffError.busy }
            let handoff = Handoff(
                id: UUID(), profile: profile, state: state, challenge: challenge, createdAt: now,
                expiresAt: now.addingTimeInterval(600))
            try write(handoff)
            return handoff
        }
    }
    func pending(_ id: UUID, now: Date = Date()) throws -> Handoff {
        try locked { try load(id, now: now) }
    }
    func forState(_ state: String, now: Date = Date()) throws -> Handoff {
        guard Self.valid(state) else { throw HandoffError.invalid }
        return try locked {
            let items = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
                .filter { $0.pathExtension == "json" }
                .compactMap { try? JSONDecoder().decode(Handoff.self, from: Data(contentsOf: $0)) }
                .filter { $0.state == state && $0.expiresAt > now && $0.code == nil && $0.error == nil }
            guard items.count == 1, let item = items.first else { throw HandoffError.invalid }
            return item
        }
    }
    func poll(_ id: UUID, profile: String, now: Date = Date()) throws -> Handoff {
        try locked {
            let handoff = try load(id, now: now)
            guard handoff.profile == profile else { throw HandoffError.profile }
            if let expires = handoff.codeExpiresAt, expires <= now { throw HandoffError.expired }
            return handoff
        }
    }
    func complete(_ id: UUID, callback: URL?, now: Date = Date()) throws {
        try locked {
            var handoff = try load(id, now: now)
            guard handoff.code == nil, handoff.error == nil else { return }
            guard let callback, let parts = URLComponents(url: callback, resolvingAgainstBaseURL: false),
                parts.scheme == callbackURL.scheme, parts.host == callbackURL.host, parts.path == callbackURL.path,
                parts.port == nil,
                parts.user == nil, parts.password == nil, parts.fragment == nil,
                parts.queryItems?.filter({ $0.name == "state" }).count == 1,
                parts.queryItems?.first(where: { $0.name == "state" })?.value == handoff.state
            else {
                throw HandoffError.invalid
            }
            if parts.queryItems?.contains(where: { $0.name == "error" }) == true {
                handoff.error = "cancelled"
            } else if let code = parts.queryItems?.first(where: { $0.name == "code" })?.value,
                parts.queryItems?.filter({ $0.name == "code" }).count == 1, Self.valid(code)
            {
                handoff.code = code
                handoff.codeExpiresAt = now.addingTimeInterval(120)
            } else {
                handoff.error = "invalid_callback"
            }
            try write(handoff)
        }
    }
    func cancel(_ id: UUID, profile: String, now: Date = Date()) throws {
        try locked {
            let handoff = try load(id, now: now)
            guard handoff.profile == profile else { throw HandoffError.profile }
            try FileManager.default.removeItem(at: file(id))
        }
    }
}
