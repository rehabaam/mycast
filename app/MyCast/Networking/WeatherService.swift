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
    case unauthorized
    case serverError(Int)

    var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "The weather service URL is invalid."
        case .invalidResponse:
            return "Received an unexpected response from the weather service."
        case .unauthorized:
            return "The weather service rejected the API token. Check MYCAST_API_TOKEN in Config/Secrets.xcconfig."
        case .serverError(let statusCode):
            return "The weather service returned an error (status \(statusCode))."
        }
    }
}

struct WeatherService {
    /// Info.plist key that overrides the server address (e.g. a LAN address
    /// when running on a device).
    static let baseURLInfoKey = "MyCastAPIBaseURL"
    static let fallbackBaseURL = URL(string: "http://localhost:8080")!

    /// Info.plist key holding the bearer token a deployed server requires.
    /// Both values come from build settings (Config/MyCast.xcconfig, with
    /// local overrides in the git-ignored Config/Secrets.xcconfig).
    static let apiTokenInfoKey = "MyCastAPIToken"

    var baseURL: URL = Self.configuredBaseURL()

    /// Sent as `Authorization: Bearer ...` when present. A local server
    /// started without a token needs none.
    var apiToken: String? = Self.configuredAPIToken()

    /// The token from the bundle, or nil when none is configured. An
    /// unexpanded "$(MYCAST_API_TOKEN)" counts as none.
    static func configuredAPIToken(bundle: Bundle = .main) -> String? {
        guard let value = bundle.object(forInfoDictionaryKey: apiTokenInfoKey) as? String else { return nil }
        let token = value.trimmingCharacters(in: .whitespacesAndNewlines)
        return token.isEmpty || token.hasPrefix("$(") ? nil : token
    }

    static func configuredBaseURL(bundle: Bundle = .main) -> URL {
        guard let string = bundle.object(forInfoDictionaryKey: baseURLInfoKey) as? String,
              let url = URL(string: string), url.scheme != nil, url.host != nil
        else { return fallbackBaseURL }
        return url
    }

    func fetchForecast() async throws -> WeatherForecastResponse {
        let data = try await get("forecast")
        return try JSONDecoder.weatherForecast.decode(WeatherForecastResponse.self, from: data)
    }

    func fetchCurrent() async throws -> CurrentWeather {
        let data = try await get("current")
        return try JSONDecoder.currentWeather.decode(CurrentWeather.self, from: data)
    }

    /// The request for `url`, with the bearer token attached when configured.
    func request(for url: URL) -> URLRequest {
        var request = URLRequest(url: url)
        if let apiToken {
            request.setValue("Bearer \(apiToken)", forHTTPHeaderField: "Authorization")
        }
        return request
    }

    private func get(_ path: String) async throws -> Data {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw WeatherServiceError.invalidURL
        }

        let (data, response) = try await URLSession.shared.data(for: request(for: url))

        guard let httpResponse = response as? HTTPURLResponse else {
            throw WeatherServiceError.invalidResponse
        }
        if httpResponse.statusCode == 401 {
            throw WeatherServiceError.unauthorized
        }
        guard (200..<300).contains(httpResponse.statusCode) else {
            throw WeatherServiceError.serverError(httpResponse.statusCode)
        }

        return data
    }
}

extension JSONDecoder {
    /// Decoder for `/forecast`.
    static var weatherForecast: JSONDecoder { weatherServer }

    /// Decoder for `/current`. Its CodingKeys name each key explicitly, so no
    /// key conversion is applied.
    static var currentWeather: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .weatherServerDates
        return decoder
    }

    private static var weatherServer: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .weatherServerDates
        return decoder
    }
}

extension JSONDecoder.DateDecodingStrategy {
    /// RFC 3339 timestamps, with or without fractional seconds.
    static let weatherServerDates: JSONDecoder.DateDecodingStrategy = .custom { decoder in
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
}

private extension ISO8601DateFormatter {
    static let weatherStandard = ISO8601DateFormatter()

    static let weatherWithFractionalSeconds: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()
}
