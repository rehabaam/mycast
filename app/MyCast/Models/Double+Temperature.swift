//
//  Double+Temperature.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import Foundation

extension Double {
    var formattedTemperature: String {
        Measurement(value: self, unit: UnitTemperature.celsius)
            .formatted(
                .measurement(
                    width: .narrow,
                    numberFormatStyle: .number.precision(.fractionLength(0))
                )
            )
    }
}
