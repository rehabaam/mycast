//
//  WeatherCondition.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

extension DayForecast {
    var conditionSymbolName: String {
        condition.summary.weatherSymbolName
    }
}

extension CurrentWeather {
    var conditionSymbolName: String {
        if rain > 0 || sumRain1h > 0 {
            "cloud.rain.fill"
        } else if outdoorHumidity > 70 {
            "cloud.sun.fill"
        } else {
            "sun.max.fill"
        }
    }
}

extension String {
    /// Maps a free-text weather condition summary (e.g. "Overcast", "Light drizzle") to an SF Symbol name.
    var weatherSymbolName: String {
        let text = lowercased()
        return if text.contains("thunder") {
            "cloud.bolt.rain.fill"
        } else if text.contains("snow") {
            "cloud.snow.fill"
        } else if text.contains("drizzle") {
            "cloud.drizzle.fill"
        } else if text.contains("rain") || text.contains("shower") {
            "cloud.rain.fill"
        } else if text.contains("fog") {
            "cloud.fog.fill"
        } else if text.contains("overcast") {
            "cloud.fill"
        } else if text.contains("partly") {
            "cloud.sun.fill"
        } else if text.contains("clear") {
            "sun.max.fill"
        } else {
            "cloud.fill"
        }
    }
}
