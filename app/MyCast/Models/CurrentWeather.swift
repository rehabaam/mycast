//
//  CurrentWeather.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

struct CurrentWeather: Codable {
    let timestamp: Date
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

    enum CodingKeys: String, CodingKey {
        case timestamp = "Timestamp"
        case outdoorTemp = "OutdoorTemp"
        case outdoorHumidity = "OutdoorHumidity"
        case apparentTempC = "ApparentTempC"
        case indoorTemp = "IndoorTemp"
        case indoorHumidity = "IndoorHumidity"
        case pressure = "Pressure"
        case pressureTrend = "PressureTrend"
        case tempTrend = "TempTrend"
        case windSpeed = "WindSpeed"
        case windAngle = "WindAngle"
        case gustSpeed = "GustSpeed"
        case gustAngle = "GustAngle"
        case rain = "Rain"
        case sumRain1h = "SumRain1h"
        case sumRain24h = "SumRain24h"
        case todayOutdoorMinC = "TodayOutdoorMinC"
        case todayOutdoorMaxC = "TodayOutdoorMaxC"
    }
}
