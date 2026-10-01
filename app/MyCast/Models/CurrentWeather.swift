//
//  CurrentWeather.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

struct CurrentWeather: Codable {
    let timestamp: Date

    /// True when the server hasn't managed to refresh this reading recently
    /// (its fetches from the station are failing), so what follows is the last
    /// good reading rather than a current one.
    let stale: Bool

    /// False when the station's outdoor module is missing or unreachable. The
    /// outdoor values below are then zeros, not readings.
    let outdoorAvailable: Bool

    let outdoorTemp: Double
    let outdoorHumidity: Double
    let apparentTempC: Double
    let indoorTemp: Double
    let indoorHumidity: Double
    let pressure: Double
    let pressureTrend: String
    let tempTrend: String
    let windSpeed: Double
    let windAngle: Double
    let gustSpeed: Double
    let gustAngle: Double
    let rain: Double
    let sumRain1h: Double
    let sumRain24h: Double
    let todayOutdoorMinC: Double
    let todayOutdoorMaxC: Double

    // Swift keeps its short property names; the server's keys carry units.
    enum CodingKeys: String, CodingKey {
        case timestamp
        case stale
        case outdoorAvailable = "outdoor_available"
        case outdoorTemp = "outdoor_temp_c"
        case outdoorHumidity = "outdoor_humidity_pct"
        case apparentTempC = "apparent_temp_c"
        case indoorTemp = "indoor_temp_c"
        case indoorHumidity = "indoor_humidity_pct"
        case pressure = "pressure_hpa"
        case pressureTrend = "pressure_trend"
        case tempTrend = "temp_trend"
        case windSpeed = "wind_speed_kmh"
        case windAngle = "wind_angle_deg"
        case gustSpeed = "gust_speed_kmh"
        case gustAngle = "gust_angle_deg"
        case rain = "rain_mm"
        case sumRain1h = "sum_rain_1h_mm"
        case sumRain24h = "sum_rain_24h_mm"
        case todayOutdoorMinC = "today_outdoor_min_c"
        case todayOutdoorMaxC = "today_outdoor_max_c"
    }
}
