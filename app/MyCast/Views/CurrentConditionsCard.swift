//
//  CurrentConditionsCard.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import SwiftUI

struct CurrentConditionsCard: View {
    let current: CurrentWeather

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 12) {
                Image(systemName: current.conditionSymbolName)
                    .font(.title2)
                    .foregroundStyle(.white)
                    .frame(width: 44, height: 44)
                    .background(Color.accentColor, in: Circle())

                Text(current.outdoorTemp.formattedTemperature)
                    .font(.system(size: 44, weight: .bold))

                VStack(alignment: .leading, spacing: 2) {
                    Text("Indoors \(current.indoorTemp.formattedTemperature)")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                    Text("Updated \(current.timestamp, style: .relative) ago")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
            }
            HStack(spacing: 10) {
                Label("\(Int(current.outdoorHumidity))%", systemImage: "humidity")
                Label("\(Int(current.windSpeed)) km/h \(current.windAngle.compassCardinal)", systemImage: "wind")
                Label("\(current.sumRain24h.formatted(.number.precision(.fractionLength(1)))) mm/24h", systemImage: "drop.fill")
            }
            .labelStyle(CompactLabelStyle())
            .font(.caption)
            .foregroundStyle(.secondary)
            .lineLimit(1)

            HStack(spacing: 10) {
                Label("Feels \(current.apparentTempC.formattedTemperature) \(current.tempTrend.trendArrow)", systemImage: "thermometer.variable")
                Label("H:\(current.todayOutdoorMaxC.formattedTemperature) L:\(current.todayOutdoorMinC.formattedTemperature)", systemImage: "arrow.up.arrow.down")
                Label("\(Int(current.pressure)) hPa \(current.pressureTrend.trendArrow)", systemImage: "gauge")
            }
            .labelStyle(CompactLabelStyle())
            .font(.caption)
            .foregroundStyle(.secondary)
            .lineLimit(1)
        }
        .padding(.vertical, 4)
    }
}

#Preview {
    List {
        CurrentConditionsCard(current: .preview)
    }
}
