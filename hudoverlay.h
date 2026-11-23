#pragma once

#include <QWidget>
#include <QString>
#include <QVector>
#include "tdxbridge.h"

struct StockCycleInfo {
    bool valid = false;
    double peakHigh = 0.0;
    int peakDate = 0;
    double rangeLow = 0.0;
    int lowDate = 0;
    double curClose = 0.0;
    double drawdownPct = 0.0;
    double chg5dPct = 0.0;
    double chg20dPct = 0.0;
    double ma20BiasPct = 0.0;
};

struct BreadthDayInfo {
    int date = 0;
    int total = 0;
    int down = 0;
    double downPct = 0.0;
    double medianPct = 0.0;
    int drop5 = 0;
};

struct HudSnapshot {
    QString stockKey;
    QString stockName;
    QString category;
    int activeRank = -1;
    int peerCount = 0;
    double avgPct = 0.0;
    int upCnt = 0;
    int downCnt = 0;
    int limitUpCnt = 0;
    QVector<PeerQuote> topLeaders;    // Top 3 peers
    QVector<PeerQuote> bottomLaggards; // Bottom 2 peers
    StockCycleInfo cycle;
};

class HudOverlay : public QWidget {
    Q_OBJECT
public:
    explicit HudOverlay(QWidget *parent = nullptr);

    void updateSnapshot(const HudSnapshot &snap);
    void reloadMarketBreadth();
    StockCycleInfo queryStockCycle(const QString &stockKey) const;

    void cycleOpacity();
    double hudOpacity() const { return m_opacity; }
    QString opacityLabel() const;
    void setAlwaysOnTop(bool onTop);

protected:
    void paintEvent(QPaintEvent *event) override;

private:
    QString dbPath() const;

    HudSnapshot m_snap;
    QVector<BreadthDayInfo> m_breadth; // last 20 trading days
    double m_opacity = 1.0;
    int m_opacityIdx = 2; // 0=半透(0.75), 1=高透(0.90), 2=原生不透明(1.0)
};
