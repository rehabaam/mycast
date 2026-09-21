//
//  String+Trend.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

extension String {
    /// Maps a trend keyword ("up", "down", "stable") to a directional arrow.
    var trendArrow: String {
        switch lowercased() {
        case "up":
            "↑"
        case "down":
            "↓"
        default:
            "→"
        }
    }
}
