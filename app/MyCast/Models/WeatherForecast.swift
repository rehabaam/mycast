//
//  WeatherForecast.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

struct WeatherForecastResponse: Codable {
    let generatedAt: Date
    let stationId: String
    let model: String
    let stale: Bool
    let days: [DayForecast]
}

struct DayForecast: Codable, Identifiable {
    let date: String
    let dayOfWeek: String
    let sunrise: Date
    let sunset: Date
    let aurora: AuroraForecast
    let condition: DayCondition
    let temperature: TemperatureForecast
    let humidity: HumidityForecast
    let wind: WindForecast
    let precipitation: PrecipitationForecast

    var id: String { date }
}

struct AuroraForecast: Codable {
    let maxProbabilityPct: Double
    let hourly: [HourlyAurora]
}

struct HourlyAurora: Codable, Identifiable {
    let time: Date
    let kp: Double
    let requiredKp: Double
    let cloudCoverPct: Double
    let isDark: Bool
    let probabilityPct: Double

    var id: Date { time }
}

struct DayCondition: Codable {
    let summary: String
    let hourly: [HourlyCondition]
}

struct HourlyCondition: Codable, Identifiable {
    let time: Date
    let code: Int
    let summary: String

    var id: Date { time }
}

struct TemperatureForecast: Codable {
    let minC: Double
    let maxC: Double
    let avgC: Double
    let hourly: [HourlyValue]
    let apparentAvgC: Double
    let apparentHourly: [HourlyValue]
}

struct HumidityForecast: Codable {
    let minPct: Double
    let maxPct: Double
    let avgPct: Double
    let hourly: [HourlyValue]
}

struct HourlyValue: Codable, Identifiable {
    let time: Date
    let value: Double

    var id: Date { time }
}

struct WindForecast: Codable {
    let avgSpeedKmh: Double
    let maxSpeedKmh: Double
    let avgDirectionDeg: Double
    let cardinal: String
    let hourly: [HourlyWind]
}

struct HourlyWind: Codable, Identifiable {
    let time: Date
    let speedKmh: Double
    let directionDeg: Double
    let cardinal: String

    var id: Date { time }
}

struct PrecipitationForecast: Codable {
    let totalMm: Double
    let probability: Double
    let hourly: [HourlyPrecipitation]
}

struct HourlyPrecipitation: Codable, Identifiable {
    let time: Date
    let amountMm: Double
    let probability: Double

    var id: Date { time }
}
