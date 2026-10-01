//
//  CurrentWeather+Preview.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

#if DEBUG
extension CurrentWeather {
    static let preview = CurrentWeather(
        timestamp: Date(),
        stale: false,
        outdoorAvailable: true,
        outdoorTemp: 16.6,
        outdoorHumidity: 84,
        apparentTempC: 15.9,
        indoorTemp: 21.3,
        indoorHumidity: 60,
        pressure: 1003.7,
        pressureTrend: "up",
        tempTrend: "stable",
        windSpeed: 3,
        windAngle: 102,
        gustSpeed: 13,
        gustAngle: 110,
        rain: 0,
        sumRain1h: 0,
        sumRain24h: 10.1,
        todayOutdoorMinC: 11.2,
        todayOutdoorMaxC: 17.1
    )
}
#endif
