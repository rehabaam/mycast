//
//  WeatherService.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

enum WeatherServiceError: LocalizedError {
    case invalidURL
    case invalidResponse
    case serverError(Int)

    var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "The weather service URL is invalid."
        case .invalidResponse:
            return "Received an unexpected response from the weather service."
        case .serverError(let statusCode):
            return "The weather service returned an error (status \(statusCode))."
        }
    }
}

struct WeatherService {
    var baseURL = URL(string: "http://localhost:8080")!

    func fetchForecast() async throws -> WeatherForecastResponse {
        let data = try await get("forecast")
        return try JSONDecoder.weatherForecast.decode(WeatherForecastResponse.self, from: data)
    }

    func fetchCurrent() async throws -> CurrentWeather {
        let data = try await get("current")
        return try JSONDecoder.currentWeather.decode(CurrentWeather.self, from: data)
    }

    private func get(_ path: String) async throws -> Data {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw WeatherServiceError.invalidURL
        }

        let (data, response) = try await URLSession.shared.data(from: url)

        guard let httpResponse = response as? HTTPURLResponse else {
            throw WeatherServiceError.invalidResponse
        }
        guard (200..<300).contains(httpResponse.statusCode) else {
            throw WeatherServiceError.serverError(httpResponse.statusCode)
        }

        return data
    }
}

extension JSONDecoder {
    static var weatherForecast: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let string = try container.decode(String.self)
            if let date = ISO8601DateFormatter.weatherWithFractionalSeconds.date(from: string) {
                return date
            }
            if let date = ISO8601DateFormatter.weatherStandard.date(from: string) {
                return date
            }
            throw DecodingError.dataCorruptedError(
                in: container,
                debugDescription: "Expected an ISO 8601 date string, got \(string)"
            )
        }
        return decoder
    }

    static var currentWeather: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .secondsSince1970
        return decoder
    }
}

private extension ISO8601DateFormatter {
    static let weatherStandard = ISO8601DateFormatter()

    static let weatherWithFractionalSeconds: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()
}
