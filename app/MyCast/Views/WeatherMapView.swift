//
//  WeatherMapView.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import SwiftUI
import MapKit

struct WeatherMapView: View {
    let current: CurrentWeather?

    /// Info.plist keys for the station's fixed coordinate (Config/MyCast.xcconfig,
    /// with the real values in the git-ignored Config/Secrets.xcconfig).
    static let stationLatitudeInfoKey = "MyCastStationLatitude"
    static let stationLongitudeInfoKey = "MyCastStationLongitude"

    /// The weather station's own location, not the device's. The station is
    /// fixed in place, so there is nothing to track live.
    private let coordinate: CLLocationCoordinate2D
    @State private var cameraPosition: MapCameraPosition

    init(current: CurrentWeather?, bundle: Bundle = .main) {
        self.current = current
        coordinate = Self.stationCoordinate(bundle: bundle)
        _cameraPosition = State(
            initialValue: .region(
                MKCoordinateRegion(
                    center: coordinate,
                    span: MKCoordinateSpan(latitudeDelta: 0.05, longitudeDelta: 0.05)
                )
            )
        )
    }

    static func stationCoordinate(bundle: Bundle = .main) -> CLLocationCoordinate2D {
        guard
            let latString = bundle.object(forInfoDictionaryKey: stationLatitudeInfoKey) as? String,
            let lonString = bundle.object(forInfoDictionaryKey: stationLongitudeInfoKey) as? String,
            let lat = Double(latString), let lon = Double(lonString)
        else {
            return CLLocationCoordinate2D(latitude: 0, longitude: 0)
        }
        return CLLocationCoordinate2D(latitude: lat, longitude: lon)
    }

    var body: some View {
        Map(position: $cameraPosition) {
            Annotation("Current Conditions", coordinate: coordinate) {
                if let current {
                    WeatherPointAnnotation(current: current)
                }
            }
        }
        .mapStyle(.standard)
    }
}

private struct WeatherPointAnnotation: View {
    let current: CurrentWeather

    var body: some View {
        VStack(spacing: 6) {
            HStack(spacing: 8) {
                Text(current.outdoorTemp.formattedTemperature)
                    .font(.headline)
                Text("\(Int(current.outdoorHumidity))%")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(.thinMaterial, in: Capsule())

            WindArrow(speedKmh: current.windSpeed, directionDeg: current.windAngle)
        }
    }
}

#Preview {
    WeatherMapView(current: .preview)
}
