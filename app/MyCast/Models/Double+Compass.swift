//
//  Double+Compass.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

extension Double {
    var compassCardinal: String {
        let directions = ["N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
                           "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"]
        let normalized = (truncatingRemainder(dividingBy: 360) + 360).truncatingRemainder(dividingBy: 360)
        let index = Int((normalized / 22.5) + 0.5) % directions.count
        return directions[index]
    }
}
