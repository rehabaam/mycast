//
//  DayForecast+Preview.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

#if DEBUG
extension DayForecast {
    static let preview: DayForecast = {
        let calendar = Calendar(identifier: .gregorian)
        let startOfDay = calendar.startOfDay(for: Date())
        let hourly = (0..<24).map { hour -> Int in hour }
        let temperatureHourly = hourly.map { hour in
            HourlyValue(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                value: 12 + 8 * sin(Double(hour) / 24 * .pi)
            )
        }
        let apparentHourly = hourly.map { hour in
            HourlyValue(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                value: 11 + 8 * sin(Double(hour) / 24 * .pi)
            )
        }
        let humidityHourly = hourly.map { hour in
            HourlyValue(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                value: 70 + 20 * cos(Double(hour) / 24 * .pi)
            )
        }
        let windHourly = hourly.map { hour in
            HourlyWind(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                speedKmh: 4 + Double(hour % 6),
                directionDeg: Double(hour) * 15,
                cardinal: "W"
            )
        }
        let precipitationHourly = hourly.map { hour in
            HourlyPrecipitation(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                amountMm: hour % 5 == 0 ? 0.4 : 0,
                probability: 0.1 + Double(hour % 5) / 10
            )
        }

        let conditionHourly = hourly.map { hour in
            HourlyCondition(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                code: 2,
                summary: "Partly cloudy"
            )
        }
        let auroraHourly = hourly.map { hour in
            HourlyAurora(
                time: calendar.date(byAdding: .hour, value: hour, to: startOfDay) ?? startOfDay,
                kp: 1.5,
                requiredKp: 4.29,
                cloudCoverPct: 40,
                isDark: hour < 6 || hour > 19,
                probabilityPct: 0
            )
        }

        return DayForecast(
            date: "2026-09-19",
            dayOfWeek: "Saturday",
            sunrise: calendar.date(byAdding: .hour, value: 7, to: startOfDay) ?? startOfDay,
            sunset: calendar.date(byAdding: .hour, value: 19, to: startOfDay) ?? startOfDay,
            aurora: AuroraForecast(maxProbabilityPct: 0, hourly: auroraHourly),
            condition: DayCondition(summary: "Partly cloudy", hourly: conditionHourly),
            temperature: TemperatureForecast(
                minC: 8.05,
                maxC: 21.19,
                avgC: 13.44,
                hourly: temperatureHourly,
                apparentAvgC: 12.5,
                apparentHourly: apparentHourly
            ),
            humidity: HumidityForecast(minPct: 57.87, maxPct: 100, avgPct: 91.08, hourly: humidityHourly),
            wind: WindForecast(avgSpeedKmh: 2.92, maxSpeedKmh: 8.88, avgDirectionDeg: 263.64, cardinal: "W", hourly: windHourly),
            precipitation: PrecipitationForecast(totalMm: 1.23, probability: 0.2, hourly: precipitationHourly)
        )
    }()
}
#endif
