#pragma once

#include <QWidget>
#include <QString>
#include <QVector>
#include <QMap>
#include <QSet>
#include <QNetworkAccessManager>
#include <QDateTime>
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

struct DeviationInfo {
    bool valid = false;
    int date = 0;
    double close = 0.0;
    double ztRatio = 0.10;
    QString boardName;
    QString indexName;

    double dev10 = 0.0;
    int n10 = 0;
    int baseDate10 = 0;
    double baseClose10 = 0.0;
    bool triggered10 = false;

    double price100 = 0.0;        // 今日10日100%临界价
    double tomorrow100 = 0.0;     // 明日10日100%临界价 (假设指数不变)
    double day2_100 = 0.0;        // 后日10日100%临界价 (假设指数不变)
    double tomorrowZtPrice = 0.0; // 明日涨停价
    bool tomorrowZtTriggers = false;

    double dev30 = 0.0;
    int n30 = 0;
    bool triggered30 = false;
    double price200 = 0.0;
    double tomorrow200 = 0.0;
};

enum class DevHudMode {
    BottomCapsule = 0, // 2A: 底栏贴边进度条胶囊
    TopRightCard  = 1, // 2B: 主图右上角战术卡 + 3日松绑推演
    RightRuler    = 2, // 2D: 主图右侧控盘三价标尺
    AllInOne      = 3, // 组合全开 (2A + 2B + 2D)
    Off           = 4  // 关闭异动浮层
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
    QVector<PeerQuote> topLeaders;     // Top 3 peers
    QVector<PeerQuote> bottomLaggards; // Bottom 2 peers
    StockCycleInfo cycle;
    DeviationInfo deviation;
};

struct DailyBarLite {
    int date = 0;
    double close = 0.0;
};

class HudOverlay : public QWidget {
    Q_OBJECT
public:
    explicit HudOverlay(QWidget *parent = nullptr);

    void setAgentVmIp(const QString &vmIp);
    void updateSnapshot(const HudSnapshot &snap);
    void reloadMarketBreadth();
    StockCycleInfo queryStockCycle(const QString &stockKey) const;

    void requestDeviations(const QStringList &stockKeys);
    DeviationInfo cachedDeviation(const QString &stockKey) const;

    void cycleOpacity();
    double hudOpacity() const { return m_opacity; }
    QString opacityLabel() const;

    void cycleDevMode();
    DevHudMode devMode() const { return m_devMode; }
    QString devModeLabel() const;
    QString devModeShortLabel() const;

    void setAlwaysOnTop(bool onTop);

signals:
    void deviationUpdated(const QString &stockKey);

protected:
    void paintEvent(QPaintEvent *event) override;

private:
    QString dbPath() const;
    void fetchIndexBarsIfNeeded();
    void fetchStockBarsForDeviation(const QString &stockKey);
    void recomputePendingStocks();
    DeviationInfo computeDeviation(const QString &stockKey, const QVector<DailyBarLite> &stkBars) const;

    void paintDevBottomCapsule(QPainter &p, int W, int topY, int bgAlpha, const DeviationInfo &d) const;
    void paintDevTopRightCard(QPainter &p, int W, int bgAlpha, const DeviationInfo &d) const;
    void paintDevRightRuler(QPainter &p, int W, int topY, int bgAlpha, const DeviationInfo &d) const;

    QNetworkAccessManager *m_nam = nullptr;
    QString m_agentBaseUrl;

    HudSnapshot m_snap;
    QVector<BreadthDayInfo> m_breadth; // last 20 trading days
    double m_opacity = 1.0;
    int m_opacityIdx = 2; // 0=半透(0.75), 1=微透(0.90), 2=原生不透明(1.0)
    DevHudMode m_devMode = DevHudMode::TopRightCard;

    QMap<QString, QVector<DailyBarLite>> m_indexBars;
    QMap<QString, QVector<DailyBarLite>> m_stockBarsCache;
    QMap<QString, DeviationInfo> m_devCache;
    QSet<QString> m_inFlight;
    QSet<QString> m_pendingCompute;
};
