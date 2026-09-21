//
//  WeatherViewModel.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation
import Observation

@Observable
final class WeatherViewModel {
    enum LoadState {
        case idle
        case loading
        case loaded
        case failed(String)
    }

    private(set) var forecast: WeatherForecastResponse?
    private(set) var forecastLoadState: LoadState = .idle

    private(set) var current: CurrentWeather?
    private(set) var currentLoadState: LoadState = .idle

    private let service: WeatherService

    init(service: WeatherService = WeatherService()) {
        self.service = service
    }

    @MainActor
    func loadAll() async {
        async let forecast: Void = loadForecast()
        async let current: Void = loadCurrent()
        _ = await (forecast, current)
    }

    @MainActor
    func loadForecast() async {
        forecastLoadState = .loading
        do {
            forecast = try await service.fetchForecast()
            forecastLoadState = .loaded
        } catch {
            forecastLoadState = .failed(error.localizedDescription)
        }
    }

    @MainActor
    func loadCurrent() async {
        currentLoadState = .loading
        do {
            current = try await service.fetchCurrent()
            currentLoadState = .loaded
        } catch {
            currentLoadState = .failed(error.localizedDescription)
        }
    }
}
