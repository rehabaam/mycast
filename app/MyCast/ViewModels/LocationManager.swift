//
//  LocationManager.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import CoreLocation
import Observation

@Observable
final class LocationManager {
    enum Status {
        case idle
        case authorized
        case denied
    }

    private(set) var coordinate: CLLocationCoordinate2D?
    private(set) var status: Status = .idle

    private var updatesTask: Task<Void, Never>?

    @MainActor
    func startUpdating() {
        guard updatesTask == nil else { return }
        updatesTask = Task {
            do {
                for try await update in CLLocationUpdate.liveUpdates() {
                    if let location = update.location {
                        coordinate = location.coordinate
                        status = .authorized
                    } else if update.authorizationDenied || update.authorizationDeniedGlobally {
                        status = .denied
                    }
                }
            } catch {
                status = .denied
            }
        }
    }

    deinit {
        updatesTask?.cancel()
    }
}
