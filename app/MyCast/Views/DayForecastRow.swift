//
//  DayForecastRow.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import SwiftUI

struct DayForecastRow: View {
    let day: DayForecast

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: day.conditionSymbolName)
                .font(.title3)
                .foregroundStyle(.white)
                .frame(width: 34, height: 34)
                .background(Color.accentColor, in: Circle())

            VStack(alignment: .leading, spacing: 4) {
                Text(day.dayOfWeek)
                    .font(.headline)
                Text(day.date)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                HStack(spacing: 8) {
                    Label(day.wind.cardinal, systemImage: "wind")
                    Label("\(Int(day.precipitation.probability * 100))%", systemImage: "drop.fill")
                    if day.aurora.maxProbabilityPct > 0 {
                        Label("\(Int(day.aurora.maxProbabilityPct))%", systemImage: "sparkles")
                    }
                }
                .labelStyle(CompactLabelStyle())
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize(horizontal: true, vertical: false)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text(day.temperature.maxC.formattedTemperature)
                    .font(.title3.bold())
                    .lineLimit(1)
                Text(day.temperature.minC.formattedTemperature)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .padding(.vertical, 4)
    }
}

#Preview {
    List {
        DayForecastRow(day: .preview)
    }
}
