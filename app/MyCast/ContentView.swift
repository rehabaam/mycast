//
//  ContentView.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import SwiftUI

struct ContentView: View {
    @State private var viewModel = WeatherViewModel()
    @AppStorage("appColorScheme") private var appColorScheme: AppColorScheme = .system

    var body: some View {
        TabView {
            Tab("Forecast", systemImage: "list.bullet") {
                NavigationStack {
                    content
                        .navigationTitle("MyCast")
                        .toolbar { themeToggleButton }
                }
            }
            Tab("Map", systemImage: "map") {
                NavigationStack {
                    WeatherMapView(current: viewModel.current)
                        .navigationTitle("Map")
                        .toolbar { themeToggleButton }
                }
            }
        }
        .task {
            await viewModel.loadAll()
        }
        .preferredColorScheme(appColorScheme.colorScheme)
    }

    @ToolbarContentBuilder
    private var themeToggleButton: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                appColorScheme = appColorScheme.next
            } label: {
                Image(systemName: appColorScheme.symbolName)
            }
            .accessibilityLabel(appColorScheme.accessibilityLabel)
        }
    }

    @ViewBuilder
    private var content: some View {
        if let forecast = viewModel.forecast {
            forecastList(forecast)
        } else {
            switch viewModel.forecastLoadState {
            case .idle, .loading, .loaded:
                ProgressView("Loading forecast…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            case .failed(let message):
                ContentUnavailableView {
                    Label("Unable to Load Forecast", systemImage: "cloud.slash")
                } description: {
                    Text(message)
                } actions: {
                    Button("Try Again") {
                        Task { await viewModel.loadAll() }
                    }
                }
            }
        }
    }

    private func forecastList(_ forecast: WeatherForecastResponse) -> some View {
        List {
            Section("Now") {
                currentConditions
            }
            Section {
                ForEach(forecast.days) { day in
                    NavigationLink {
                        DayDetailView(day: day)
                    } label: {
                        DayForecastRow(day: day)
                    }
                }
            } header: {
                Text("Forecast")
            } footer: {
                HStack(spacing: 4) {
                    Image(systemName: "info.circle")
                    Text("\(forecast.model) · Updated \(forecast.generatedAt, style: .relative) ago")
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            }
        }
        .listStyle(.plain)
        .refreshable {
            await viewModel.loadAll()
        }
        .safeAreaInset(edge: .top) {
            if forecast.stale {
                staleBanner
            }
        }
    }

    private var staleBanner: some View {
        Label("Forecast data may be outdated", systemImage: "exclamationmark.triangle.fill")
            .font(.footnote.weight(.semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity)
            .background(Color.orange)
    }

    @ViewBuilder
    private var currentConditions: some View {
        if let current = viewModel.current {
            CurrentConditionsCard(current: current)
        } else {
            switch viewModel.currentLoadState {
            case .idle, .loading, .loaded:
                ProgressView()
            case .failed(let message):
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

private enum AppColorScheme: String {
    case system, light, dark

    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light: .light
        case .dark: .dark
        }
    }

    var symbolName: String {
        switch self {
        case .system: "circle.lefthalf.filled"
        case .light: "sun.max.fill"
        case .dark: "moon.fill"
        }
    }

    var accessibilityLabel: String {
        switch self {
        case .system: "Appearance: System. Tap to switch to Light."
        case .light: "Appearance: Light. Tap to switch to Dark."
        case .dark: "Appearance: Dark. Tap to switch to System."
        }
    }

    var next: AppColorScheme {
        switch self {
        case .system: .light
        case .light: .dark
        case .dark: .system
        }
    }
}

#Preview {
    ContentView()
}
