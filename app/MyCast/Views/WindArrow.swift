//
//  WindArrow.swift
//  MyCast
//
//  Created by Ahmad Samra on 9/19/26.
//

import SwiftUI

struct WindArrow: View {
    let speedKmh: Double
    let directionDeg: Double

    @State private var isPulsing = false

    private var cycleDuration: Double {
        max(0.4, 2.2 - speedKmh / 12)
    }

    var body: some View {
        VStack(spacing: 4) {
            Image(systemName: "location.north.fill")
                .font(.system(size: 20, weight: .bold))
                .foregroundStyle(.white)
                .padding(10)
                .background(Color.accentColor, in: Circle())
                .rotationEffect(.degrees(directionDeg))
                .scaleEffect(isPulsing ? 1.15 : 0.9)
                .shadow(radius: 2)

            Text("\(Int(speedKmh)) km/h")
                .font(.caption2.bold())
                .lineLimit(1)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(.thinMaterial, in: Capsule())
        }
        .onAppear {
            withAnimation(.easeInOut(duration: cycleDuration).repeatForever(autoreverses: true)) {
                isPulsing = true
            }
        }
    }
}

#Preview {
    WindArrow(speedKmh: 12, directionDeg: 45)
}
