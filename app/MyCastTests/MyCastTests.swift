//
//  MyCastTests.swift
//  MyCastTests
//
//  Created by Ahmad Samra on 9/19/26.
//

import XCTest
@testable import MyCast

/// Decodes the fixtures in `Fixtures/`, which are the Go server's real
/// responses for each path it can take (regenerate them with
/// `go test ./forecast ./api -run Golden -update` from `server/`). If the
/// server's JSON changes in a way the app can't read, these fail.
@MainActor
final class WeatherDecodingTests: XCTestCase {

    // MARK: - /forecast

    func testDecodesTheFullForecast() throws {
        let forecast = try decodeForecast("forecast_full")

        XCTAssertEqual(forecast.days.count, 3)
        XCTAssertEqual(forecast.stationId, "70:ee:50:00:00:01")
        XCTAssertTrue(forecast.model.hasPrefix("ecmwf"))
        XCTAssertFalse(forecast.stale)

        let day = try XCTUnwrap(forecast.days.first)
        XCTAssertNotNil(day.sunrise)
        XCTAssertNotNil(day.sunset)
        let aurora = try XCTUnwrap(day.aurora)
        XCTAssertEqual(aurora.hourly.count, day.temperature.hourly.count)
        XCTAssertEqual(day.temperature.hourly.count, 8, "the first day only has its remaining hours")
        XCTAssertNotEqual(day.condition.summary, "Unknown")
    }

    /// The server omits `aurora` when only the NOAA fetch fails.
    func testDecodesAForecastWithoutAurora() throws {
        let forecast = try decodeForecast("forecast_no_aurora")

        for day in forecast.days {
            XCTAssertNil(day.aurora)
            XCTAssertNotNil(day.sunrise)
            XCTAssertNotNil(day.sunset)
        }
    }

    /// The documented fallback when Open-Meteo is down: no sunrise, sunset or
    /// aurora at all. This used to fail with `keyNotFound`, leaving the app
    /// with no forecast during exactly the outages the fallback exists for.
    func testDecodesTheStationOnlyFallbackForecast() throws {
        let forecast = try decodeForecast("forecast_station_only")

        XCTAssertTrue(forecast.model.hasPrefix("station-holtwinters"))
        XCTAssertEqual(forecast.days.count, 3)
        for day in forecast.days {
            XCTAssertNil(day.sunrise)
            XCTAssertNil(day.sunset)
            XCTAssertNil(day.aurora)
            XCTAssertEqual(day.condition.summary, "Unknown")
            XCTAssertFalse(day.temperature.hourly.isEmpty)
        }
    }

    func testForecastHourlyTimesAreDecodedAsRealDates() throws {
        let forecast = try decodeForecast("forecast_full")
        let hourly = try XCTUnwrap(forecast.days.first?.temperature.hourly)

        // generated_at is 12:30 UTC; the first forecast hour is the next one.
        let expectedFirst = try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-06-15T13:00:00Z"))
        XCTAssertEqual(hourly.first?.time, expectedFirst)
        XCTAssertEqual(forecast.generatedAt, try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-06-15T12:30:00Z")))
    }

    // MARK: - /current

    func testDecodesCurrentWeather() throws {
        let current = try decodeCurrent("current")

        XCTAssertTrue(current.outdoorAvailable)
        XCTAssertFalse(current.stale)
        XCTAssertEqual(current.outdoorTemp, 16.6, accuracy: 0.001)
        XCTAssertEqual(current.outdoorHumidity, 84, accuracy: 0.001)
        XCTAssertEqual(current.apparentTempC, 17.24, accuracy: 0.001)
        XCTAssertEqual(current.indoorTemp, 21.3, accuracy: 0.001)
        XCTAssertEqual(current.pressure, 1003.7, accuracy: 0.001)
        XCTAssertEqual(current.pressureTrend, "up")
        XCTAssertEqual(current.windSpeed, 3, accuracy: 0.001)
        XCTAssertEqual(current.windAngle, 102, accuracy: 0.001)
        XCTAssertEqual(current.sumRain1h, 0.2, accuracy: 0.001)
        XCTAssertEqual(current.todayOutdoorMinC, 11.2, accuracy: 0.001)
        XCTAssertEqual(current.todayOutdoorMaxC, 17.1, accuracy: 0.001)
        XCTAssertEqual(current.timestamp, Date(timeIntervalSince1970: 1_781_000_000))
    }

    /// The server keeps serving its last good reading when fetches fail, and
    /// says so with `stale`; the app must be able to read that.
    func testDecodesAStaleCurrentReading() throws {
        let current = try decodeCurrent("current_stale")

        XCTAssertTrue(current.stale)
        XCTAssertEqual(current.outdoorTemp, 16.6, accuracy: 0.001, "the last good values are still there")
    }

    func testDecodesCurrentWeatherWhenTheOutdoorModuleIsOffline() throws {
        let current = try decodeCurrent("current_outdoor_offline")

        XCTAssertFalse(current.outdoorAvailable)
        XCTAssertEqual(current.windSpeed, 3, accuracy: 0.001, "other modules still report")
    }

    // MARK: - Dates

    func testServerDatesAcceptWholeAndFractionalSeconds() throws {
        struct Box: Decodable { let at: Date }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .weatherServerDates

        let whole = try decoder.decode(Box.self, from: Data(#"{"at":"2026-06-15T12:30:00Z"}"#.utf8))
        let millis = try decoder.decode(Box.self, from: Data(#"{"at":"2026-06-15T12:30:00.250Z"}"#.utf8))
        XCTAssertEqual(millis.at.timeIntervalSince(whole.at), 0.25, accuracy: 0.001)

        // Go's time.Time can emit up to nine fractional digits.
        let nanos = try? decoder.decode(Box.self, from: Data(#"{"at":"2026-06-15T12:30:00.123456789Z"}"#.utf8))
        XCTAssertNotNil(nanos, "nanosecond timestamps must decode too")

        XCTAssertThrowsError(try decoder.decode(Box.self, from: Data(#"{"at":"yesterday"}"#.utf8)))
    }

    // MARK: - Configuration

    func testBaseURLComesFromInfoPlistAndFallsBackToLocalhost() {
        // The app bundle declares the address in Info.plist...
        XCTAssertEqual(WeatherService.configuredBaseURL(), URL(string: "http://localhost:8080"))
        // ...and a bundle without the key falls back to the same default.
        XCTAssertEqual(WeatherService.configuredBaseURL(bundle: Bundle(for: Self.self)), WeatherService.fallbackBaseURL)
    }

    // MARK: - Helpers

    private func fixtureData(_ name: String) throws -> Data {
        let bundle = Bundle(for: Self.self)
        let url = bundle.url(forResource: name, withExtension: "json")
            ?? bundle.url(forResource: name, withExtension: "json", subdirectory: "Fixtures")
        return try Data(contentsOf: XCTUnwrap(url, "missing fixture \(name).json in the test bundle"))
    }

    private func decodeForecast(_ name: String) throws -> WeatherForecastResponse {
        try JSONDecoder.weatherForecast.decode(WeatherForecastResponse.self, from: fixtureData(name))
    }

    private func decodeCurrent(_ name: String) throws -> CurrentWeather {
        try JSONDecoder.currentWeather.decode(CurrentWeather.self, from: fixtureData(name))
    }
}
