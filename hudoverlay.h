#pragma once

#include <QWidget>
#include <QString>
#include <QVector>
#include "tdxbridge.h"

struct StockCycleInfo {
    bool valid = false;
    double peakHigh = 0.0;
    int peakDate = 0;
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
    QVector<PeerQuote> topLeaders; // Top 3 peers in category
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

protected:
    void paintEvent(QPaintEvent *event) override;

private:
    QString dbPath() const;
    void drawPill(QPainter &p, const QRectF &rect, const QColor &accent) const;

    HudSnapshot m_snap;
    QVector<BreadthDayInfo> m_breadth; // last 10 trading days
    double m_opacity = 0.72;
    int m_opacityIdx = 1; // 0=淡(0.45), 1=中(0.72), 2=强(0.92)
};
