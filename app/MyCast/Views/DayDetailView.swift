//
//  DayDetailView.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import SwiftUI
import Charts

struct DayDetailView: View {
    let day: DayForecast

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                conditionHeader
                summarySection
                temperatureSection
                precipitationSection
                windSection
                if let aurora = day.aurora {
                    auroraSection(aurora)
                }
            }
            .padding()
        }
        .navigationTitle(day.dayOfWeek)
        .navigationSubtitle(day.date)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var conditionHeader: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                Image(systemName: day.conditionSymbolName)
                    .font(.title2)
                    .foregroundStyle(.white)
                    .frame(width: 52, height: 52)
                    .background(Color.accentColor, in: Circle())

                VStack(alignment: .leading, spacing: 2) {
                    Text(day.condition.summary)
                        .font(.title3.bold())
                        .lineLimit(1)
                    HStack(spacing: 10) {
                        Text("Feels like \(day.temperature.apparentAvgC.formattedTemperature)")
                        if let sunrise = day.sunrise {
                            Label(sunrise.formatted(date: .omitted, time: .shortened), systemImage: "sunrise.fill")
                        }
                        if let sunset = day.sunset {
                            Label(sunset.formatted(date: .omitted, time: .shortened), systemImage: "sunset.fill")
                        }
                    }
                    .labelStyle(CompactLabelStyle())
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
                Spacer()
            }
        }
    }

    private var summarySection: some View {
        HStack(spacing: 16) {
            SummaryStat(title: "High", value: day.temperature.maxC.formattedTemperature, systemImage: "thermometer.sun")
            SummaryStat(title: "Low", value: day.temperature.minC.formattedTemperature, systemImage: "thermometer.snowflake")
            SummaryStat(title: "Humidity", value: "\(Int(day.humidity.avgPct))%", systemImage: "humidity")
            SummaryStat(title: "Wind", value: "\(Int(day.wind.avgSpeedKmh)) km/h", systemImage: "wind")
        }
    }

    private var temperatureSeries: [TemperatureSeriesPoint] {
        day.temperature.hourly.map { TemperatureSeriesPoint(time: $0.time, value: $0.value, series: "Actual") }
            + day.temperature.apparentHourly.map { TemperatureSeriesPoint(time: $0.time, value: $0.value, series: "Feels like") }
    }

    private func conditionIconInterval(forPlotWidth width: CGFloat) -> Int {
        let hourCount = day.condition.hourly.count
        guard hourCount > 0 else { return 6 }
        let minIconSpacing: CGFloat = 32
        let maxIcons = max(1, Int(width / minIconSpacing))
        let iconsAtThreeHours = (hourCount + 2) / 3
        return iconsAtThreeHours <= maxIcons ? 3 : 6
    }

    private func sampledConditions(interval: Int) -> [HourlyCondition] {
        day.condition.hourly.enumerated()
            .filter { $0.offset % interval == 0 }
            .map(\.element)
    }

    private var temperatureSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 12) {
                Text("Hourly Temperature")
                    .font(.headline)
                Spacer()
                HStack(spacing: 4) {
                    Rectangle().fill(.orange).frame(width: 10, height: 2)
                    Text("Actual").font(.caption2).foregroundStyle(.secondary)
                }
                HStack(spacing: 4) {
                    Rectangle().fill(.orange.opacity(0.5)).frame(width: 10, height: 2)
                    Text("Feels like").font(.caption2).foregroundStyle(.secondary)
                }
            }
            Chart {
                ForEach(day.temperature.hourly) { point in
                    AreaMark(x: .value("Time", point.time), y: .value("Temperature", point.value))
                        .interpolationMethod(.catmullRom)
                        .foregroundStyle(.orange.opacity(0.15))
                }
                ForEach(temperatureSeries) { point in
                    LineMark(x: .value("Time", point.time), y: .value("Temperature", point.value))
                        .interpolationMethod(.catmullRom)
                        .foregroundStyle(by: .value("Series", point.series))
                        .lineStyle(point.series == "Actual" ? StrokeStyle(lineWidth: 2) : StrokeStyle(lineWidth: 1.5, dash: [4, 3]))
                }
            }
            .chartForegroundStyleScale([
                "Actual": Color.orange,
                "Feels like": Color.orange.opacity(0.5)
            ])
            .chartLegend(.hidden)
            .chartXAxis {
                AxisMarks(values: .stride(by: .hour, count: 4)) { _ in
                    AxisGridLine()
                    AxisValueLabel(format: .dateTime.hour())
                }
            }
            .chartOverlay { proxy in
                GeometryReader { geometry in
                    let plotFrame = geometry[proxy.plotFrame!]
                    let interval = conditionIconInterval(forPlotWidth: plotFrame.width)
                    ForEach(sampledConditions(interval: interval)) { point in
                        if let x = proxy.position(forX: point.time) {
                            Image(systemName: point.summary.weatherSymbolName)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                                .position(x: plotFrame.origin.x + x, y: plotFrame.origin.y + 8)
                        }
                    }
                }
            }
            .frame(height: 150)
        }
    }

    private var precipitationSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Precipitation")
                .font(.headline)
            Text("\(day.precipitation.totalMm.formatted(.number.precision(.fractionLength(1)))) mm total · \(Int(day.precipitation.probability * 100))% chance")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            Chart(day.precipitation.hourly) { point in
                BarMark(x: .value("Time", point.time), y: .value("Probability", point.probability))
                    .foregroundStyle(.blue)
            }
            .chartXAxis {
                AxisMarks(values: .stride(by: .hour, count: 4)) { _ in
                    AxisValueLabel(format: .dateTime.hour())
                }
            }
            .chartYScale(domain: 0...1)
            .frame(height: 120)
        }
    }

    private var windSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Wind")
                .font(.headline)
            Text("Avg \(Int(day.wind.avgSpeedKmh)) km/h \(day.wind.cardinal) · Max \(Int(day.wind.maxSpeedKmh)) km/h")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }

    private func auroraSection(_ aurora: AuroraForecast) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Aurora")
                .font(.headline)
            Text("\(Int(aurora.maxProbabilityPct))% chance of visibility overnight")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            if aurora.maxProbabilityPct > 0 {
                Chart(aurora.hourly) { point in
                    BarMark(x: .value("Time", point.time), y: .value("Probability", point.probabilityPct))
                        .foregroundStyle(.purple)
                }
                .chartXAxis {
                    AxisMarks(values: .stride(by: .hour, count: 4)) { _ in
                        AxisValueLabel(format: .dateTime.hour())
                    }
                }
                .chartYScale(domain: 0...100)
                .frame(height: 100)
            }
        }
    }
}

private struct TemperatureSeriesPoint: Identifiable {
    let time: Date
    let value: Double
    let series: String

    var id: String { "\(series)-\(time.timeIntervalSince1970)" }
}

private struct SummaryStat: View {
    let title: String
    let value: String
    let systemImage: String

    var body: some View {
        VStack(spacing: 4) {
            Image(systemName: systemImage)
                .foregroundStyle(.tint)
            Text(value)
                .font(.subheadline.bold())
                .lineLimit(1)
            Text(title)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity)
    }
}

#Preview {
    NavigationStack {
        DayDetailView(day: .preview)
    }
}
