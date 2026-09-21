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

    @State private var locationManager = LocationManager()
    @State private var cameraPosition: MapCameraPosition = .automatic
    @State private var hasCenteredOnLocation = false

    var body: some View {
        Group {
            if let coordinate = locationManager.coordinate {
                Map(position: $cameraPosition) {
                    Annotation("Current Conditions", coordinate: coordinate) {
                        if let current {
                            WeatherPointAnnotation(current: current)
                        }
                    }
                }
                .mapStyle(.standard)
                .onAppear {
                    guard !hasCenteredOnLocation else { return }
                    hasCenteredOnLocation = true
                    cameraPosition = .region(
                        MKCoordinateRegion(
                            center: coordinate,
                            span: MKCoordinateSpan(latitudeDelta: 0.05, longitudeDelta: 0.05)
                        )
                    )
                }
            } else if locationManager.status == .denied {
                ContentUnavailableView {
                    Label("Location Unavailable", systemImage: "location.slash")
                } description: {
                    Text("Allow location access in Settings to see conditions on the map.")
                } actions: {
                    Button("Open Settings") {
                        if let url = URL(string: UIApplication.openSettingsURLString) {
                            UIApplication.shared.open(url)
                        }
                    }
                }
            } else {
                ProgressView("Finding your location…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task {
            locationManager.startUpdating()
        }
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
