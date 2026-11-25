#include "hudoverlay.h"

#include <QDir>
#include <QFile>
#include <QGuiApplication>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QPainter>
#include <QScreen>
#include <QUrl>
#include <algorithm>
#include <cmath>
#include <sqlite3.h>

namespace {
double ceil2(double v) {
    return std::ceil(v * 100.0) / 100.0;
}

double round2(double v) {
    return std::floor(v * 100.0 + 0.5) / 100.0;
}

QColor devLevelColor(const DeviationInfo &d) {
    if (d.triggered10 || d.triggered30) return QColor(255, 45, 45);     // 亮红：已触发
    if (d.dev10 >= 90.0 || d.tomorrowZtTriggers) return QColor(255, 110, 0); // 亮橙：高危/明板触发
    if (d.dev10 >= 70.0 || d.dev30 >= 140.0) return QColor(255, 210, 0);     // 金黄：逼近观察
    return QColor(145, 165, 180);                                             // 淡灰蓝：安全
}

QString devBadgeText(const DeviationInfo &d) {
    if (d.triggered10) return "10日严重异动!";
    if (d.triggered30) return "30日严重异动!";
    if (d.tomorrowZtTriggers) return "明板触发!";
    if (d.dev10 >= 85.0) return "高危控盘";
    if (d.dev10 >= 70.0) return "逼近异动";
    return "安全";
}
} // namespace

HudOverlay::HudOverlay(QWidget *parent)
    : QWidget(parent), m_nam(new QNetworkAccessManager(this)) {
    setWindowFlags(Qt::Window
                   | Qt::FramelessWindowHint
                   | Qt::WindowStaysOnTopHint
                   | Qt::WindowTransparentForInput
                   | Qt::WindowDoesNotAcceptFocus);
    setAttribute(Qt::WA_TranslucentBackground, true);
    setAttribute(Qt::WA_TransparentForMouseEvents, true);
    setAttribute(Qt::WA_ShowWithoutActivating, true);
    setFocusPolicy(Qt::NoFocus);

    const QRect avail = QGuiApplication::primaryScreen()->availableGeometry();
    // Cover the workspace to the left of the right-docked 220px companion strip
    setGeometry(avail.left(), avail.top(), avail.width() - 222, avail.height());

    reloadMarketBreadth();
}

void HudOverlay::setAgentVmIp(const QString &vmIp) {
    if (vmIp.isEmpty()) return;
    const QString url = QString("http://%1:19876").arg(vmIp);
    if (m_agentBaseUrl == url) return;
    m_agentBaseUrl = url;
    m_indicesRequested = false;
    fetchIndexBarsIfNeeded();
    if (!m_snap.stockKey.isEmpty()) {
        fetchStockBarsForDeviation(m_snap.stockKey);
    }
}

QString HudOverlay::dbPath() const {
    return QDir::homePath() + "/.tdx_local_data/market.db";
}

void HudOverlay::cycleOpacity() {
    static const double levels[3] = {0.75, 0.90, 1.0};
    m_opacityIdx = (m_opacityIdx + 1) % 3;
    m_opacity = levels[m_opacityIdx];
    update();
}

QString HudOverlay::opacityLabel() const {
    if (m_opacityIdx == 0) return "半透明";
    if (m_opacityIdx == 1) return "微透";
    return "原生不透明";
}

void HudOverlay::cycleDevMode() {
    int next = (static_cast<int>(m_devMode) + 1) % 5;
    m_devMode = static_cast<DevHudMode>(next);
    update();
}

QString HudOverlay::devModeLabel() const {
    switch (m_devMode) {
    case DevHudMode::BottomCapsule: return "2A 贴边胶囊";
    case DevHudMode::TopRightCard:  return "2B 右上战术卡";
    case DevHudMode::RightRuler:    return "2D 右侧控盘尺";
    case DevHudMode::AllInOne:      return "全开组合";
    case DevHudMode::Off:           return "关闭";
    }
    return "2A 贴边胶囊";
}

QString HudOverlay::devModeShortLabel() const {
    switch (m_devMode) {
    case DevHudMode::BottomCapsule: return "2A";
    case DevHudMode::TopRightCard:  return "2B";
    case DevHudMode::RightRuler:    return "2D";
    case DevHudMode::AllInOne:      return "全";
    case DevHudMode::Off:           return "关";
    }
    return "2A";
}

void HudOverlay::setAlwaysOnTop(bool onTop) {
    const bool wasVisible = isVisible();
    Qt::WindowFlags f = Qt::Window
                        | Qt::FramelessWindowHint
                        | Qt::WindowTransparentForInput
                        | Qt::WindowDoesNotAcceptFocus;
    if (onTop) f |= Qt::WindowStaysOnTopHint;
    setWindowFlags(f);
    setAttribute(Qt::WA_TranslucentBackground, true);
    setAttribute(Qt::WA_TransparentForMouseEvents, true);
    setAttribute(Qt::WA_ShowWithoutActivating, true);
    if (wasVisible) show();
}

void HudOverlay::updateSnapshot(const HudSnapshot &snap) {
    m_snap = snap;
    if (!m_snap.stockKey.isEmpty() && !m_snap.cycle.valid) {
        m_snap.cycle = queryStockCycle(m_snap.stockKey);
    }
    if (!m_snap.stockKey.isEmpty()) {
        if (m_devCache.contains(m_snap.stockKey)) {
            m_snap.deviation = m_devCache.value(m_snap.stockKey);
        } else {
            fetchStockBarsForDeviation(m_snap.stockKey);
        }
    }
    update();
}

void HudOverlay::requestDeviations(const QStringList &stockKeys) {
    if (m_agentBaseUrl.isEmpty()) return;
    fetchIndexBarsIfNeeded();
    for (const QString &k : stockKeys) {
        if (k.size() == 7 && !m_devCache.contains(k)) {
            fetchStockBarsForDeviation(k);
        }
    }
}

DeviationInfo HudOverlay::cachedDeviation(const QString &stockKey) const {
    return m_devCache.value(stockKey);
}

void HudOverlay::fetchIndexBarsIfNeeded() {
    if (m_agentBaseUrl.isEmpty() || m_indicesRequested) return;
    m_indicesRequested = true;

    const QStringList idxKeys = {"1999998", "1000001", "1000688", "0399107", "0399102"};
    for (const QString &ik : idxKeys) {
        if (m_inFlight.contains(ik)) continue;
        m_inFlight.insert(ik);
        QUrl url(QString("%1/data/bars?key=%2&period=day&limit=45").arg(m_agentBaseUrl, ik));
        QNetworkReply *reply = m_nam->get(QNetworkRequest(url));
        connect(reply, &QNetworkReply::finished, this, [this, reply, ik]() {
            m_inFlight.remove(ik);
            const QByteArray raw = reply->readAll();
            const bool ok = (reply->error() == QNetworkReply::NoError);
            reply->deleteLater();
            if (!ok || raw.isEmpty()) return;

            const QJsonObject obj = QJsonDocument::fromJson(raw).object();
            if (!obj.value("ok").toBool()) return;
            QVector<DailyBarLite> bars;
            for (const QJsonValue &v : obj.value("bars").toArray()) {
                const QJsonObject bo = v.toObject();
                bars.append({bo.value("date").toString().toInt(), bo.value("close").toDouble()});
            }
            if (!bars.isEmpty()) {
                m_indexBars[ik] = bars;
                recomputePendingStocks();
            }
        });
    }
}

void HudOverlay::fetchStockBarsForDeviation(const QString &stockKey) {
    if (m_agentBaseUrl.isEmpty() || stockKey.size() != 7) return;
    fetchIndexBarsIfNeeded();
    if (m_stockBarsCache.contains(stockKey)) {
        DeviationInfo info = computeDeviation(stockKey, m_stockBarsCache.value(stockKey));
        if (info.valid) {
            m_devCache[stockKey] = info;
            if (m_snap.stockKey == stockKey) {
                m_snap.deviation = info;
                update();
            }
            emit deviationUpdated(stockKey);
        } else {
            m_pendingCompute.insert(stockKey);
        }
        return;
    }
    if (m_inFlight.contains(stockKey)) return;
    m_inFlight.insert(stockKey);

    QUrl url(QString("%1/data/bars?key=%2&period=day&limit=45").arg(m_agentBaseUrl, stockKey));
    QNetworkReply *reply = m_nam->get(QNetworkRequest(url));
    connect(reply, &QNetworkReply::finished, this, [this, reply, stockKey]() {
        m_inFlight.remove(stockKey);
        const QByteArray raw = reply->readAll();
        const bool ok = (reply->error() == QNetworkReply::NoError);
        reply->deleteLater();
        if (!ok || raw.isEmpty()) return;

        const QJsonObject obj = QJsonDocument::fromJson(raw).object();
        if (!obj.value("ok").toBool()) return;
        QVector<DailyBarLite> bars;
        for (const QJsonValue &v : obj.value("bars").toArray()) {
            const QJsonObject bo = v.toObject();
            bars.append({bo.value("date").toString().toInt(), bo.value("close").toDouble()});
        }
        if (bars.isEmpty()) return;
        m_stockBarsCache[stockKey] = bars;

        DeviationInfo info = computeDeviation(stockKey, bars);
        if (info.valid) {
            m_devCache[stockKey] = info;
            if (m_snap.stockKey == stockKey) {
                m_snap.deviation = info;
                update();
            }
            emit deviationUpdated(stockKey);
        } else {
            m_pendingCompute.insert(stockKey);
        }
    });
}

void HudOverlay::recomputePendingStocks() {
    QSet<QString> stillPending;
    for (const QString &k : m_pendingCompute) {
        if (!m_stockBarsCache.contains(k)) continue;
        DeviationInfo info = computeDeviation(k, m_stockBarsCache.value(k));
        if (info.valid) {
            m_devCache[k] = info;
            if (m_snap.stockKey == k) {
                m_snap.deviation = info;
                update();
            }
            emit deviationUpdated(k);
        } else {
            stillPending.insert(k);
        }
    }
    m_pendingCompute = stillPending;
}

DeviationInfo HudOverlay::computeDeviation(const QString &stockKey, const QVector<DailyBarLite> &stkBars) const {
    DeviationInfo out;
    if (stockKey.size() != 7 || stkBars.size() <= 30) return out;

    const QString code = stockKey.mid(1);
    QString idxKey, fallbackKey, boardName, idxName;
    double ztRatio = 0.10;
    if (code.startsWith("688") || code.startsWith("689")) {
        boardName = "科创板"; idxKey = "1000688"; idxName = "科创50"; ztRatio = 0.20;
    } else if (code.startsWith("60")) {
        boardName = "沪主板"; idxKey = "1999998"; idxName = "上证A指"; fallbackKey = "1000001"; ztRatio = 0.10;
    } else if (code.startsWith("30")) {
        boardName = "创业板"; idxKey = "0399102"; idxName = "创业板综"; ztRatio = 0.20;
    } else if (code.startsWith("00")) {
        boardName = "深主板"; idxKey = "0399107"; idxName = "深证A指"; ztRatio = 0.10;
    } else {
        return out;
    }

    QVector<DailyBarLite> idxBars = m_indexBars.value(idxKey);
    if (idxBars.isEmpty() && !fallbackKey.isEmpty()) {
        idxBars = m_indexBars.value(fallbackKey);
        idxName = "上证指数";
    }
    if (idxBars.isEmpty()) return out;

    QMap<int, double> idxMap;
    for (const auto &b : idxBars) {
        if (b.close > 0) idxMap[b.date] = b.close;
    }

    struct Row { int date; double c; double idx; };
    QVector<Row> aligned;
    aligned.reserve(stkBars.size());
    for (const auto &b : stkBars) {
        auto it = idxMap.constFind(b.date);
        if (it != idxMap.constEnd() && b.close > 0 && it.value() > 0) {
            aligned.append({b.date, b.close, it.value()});
        }
    }
    const int n = aligned.size();
    if (n <= 30) return out;

    const auto &cur = aligned[n - 1];
    auto ref = [&](int k) -> const Row & { return aligned[n - 1 - k]; };

    double dArr[11] = {0};
    double maxD10 = -1e18;
    int maxN10 = 1;
    int trigN10 = 0;
    for (int k = 1; k <= 10; ++k) {
        const auto &r = ref(k);
        const double dk = (cur.c / r.c - cur.idx / r.idx) * 100.0;
        dArr[k] = dk;
        if (dk > maxD10) { maxD10 = dk; maxN10 = k; }
        if (trigN10 == 0 && dk >= 100.0) trigN10 = k;
    }
    const int effN10 = (trigN10 > 0) ? trigN10 : maxN10;
    const auto &base10 = ref(effN10);

    // 30日内极值与固定30日
    double minC30 = cur.c;
    int lowB30Raw = 0;
    for (int i = 0; i < 30; ++i) {
        const double c = ref(i).c;
        if (c < minC30) { minC30 = c; lowB30Raw = i; }
    }
    const int lowB30 = qMax(1, lowB30Raw);
    const double dynD30 = (cur.c / ref(lowB30).c - cur.idx / ref(lowB30).idx) * 100.0;
    const double fixD30 = (cur.c / ref(30).c - cur.idx / ref(30).idx) * 100.0;
    const int effN30 = (dynD30 >= fixD30) ? lowB30 : 30;
    const double maxD30 = qMax(dynD30, fixD30);
    const auto &base30 = ref(effN30);

    // 今日临界价
    const double price100 = ceil2(base10.c * (cur.idx / base10.idx + 1.0));
    const double price200 = ceil2(base30.c * (cur.idx / base30.idx + 2.0));

    // 明日临界价（假设指数不变，基准范围 k=0..9）
    double minM10 = cur.c * 2.0;
    for (int k = 1; k <= 9; ++k) {
        const auto &r = ref(k);
        minM10 = qMin(minM10, r.c * (cur.idx / r.idx + 1.0));
    }
    const double tomorrow100 = ceil2(minM10);

    // 后日临界价（假设指数不变，基准范围 k=0..8，滚出 T-9）
    double minD2_10 = cur.c * 2.0;
    for (int k = 1; k <= 8; ++k) {
        const auto &r = ref(k);
        minD2_10 = qMin(minD2_10, r.c * (cur.idx / r.idx + 1.0));
    }
    const double day2_100 = ceil2(minD2_10);

    // 明日30日临界价
    double minC29 = cur.c;
    int mLowB30 = 0;
    for (int i = 0; i < 29; ++i) {
        const double c = ref(i).c;
        if (c < minC29) { minC29 = c; mLowB30 = i; }
    }
    const double m30Dyn = ref(mLowB30).c * (cur.idx / ref(mLowB30).idx + 2.0);
    const double m30Fix = ref(29).c * (cur.idx / ref(29).idx + 2.0);
    const double tomorrow200 = ceil2(qMin(m30Dyn, m30Fix));

    const double tomorrowZt = round2(cur.c * (1.0 + ztRatio));

    out.valid = true;
    out.date = cur.date;
    out.close = cur.c;
    out.ztRatio = ztRatio;
    out.boardName = boardName;
    out.indexName = idxName;
    out.dev10 = dArr[effN10];
    out.n10 = effN10;
    out.baseDate10 = base10.date;
    out.baseClose10 = base10.c;
    out.triggered10 = (trigN10 > 0);
    out.price100 = price100;
    out.tomorrow100 = tomorrow100;
    out.day2_100 = day2_100;
    out.tomorrowZtPrice = tomorrowZt;
    out.tomorrowZtTriggers = (tomorrow100 <= tomorrowZt + 1e-6);
    out.dev30 = maxD30;
    out.n30 = effN30;
    out.triggered30 = (maxD30 >= 200.0);
    out.price200 = price200;
    out.tomorrow200 = tomorrow200;
    return out;
}

StockCycleInfo HudOverlay::queryStockCycle(const QString &stockKey) const {
    StockCycleInfo info;
    const QString path = dbPath();
    if (stockKey.size() != 7 || !QFile::exists(path)) return info;

    sqlite3 *db = nullptr;
    if (sqlite3_open_v2(path.toUtf8().constData(), &db, SQLITE_OPEN_READONLY, nullptr) != SQLITE_OK) {
        if (db) sqlite3_close(db);
        return info;
    }

    const char *sql = "SELECT date, high, low, close FROM daily_bars WHERE key=? AND date >= 20260620 ORDER BY date ASC";
    sqlite3_stmt *stmt = nullptr;
    QVector<int> dates;
    QVector<double> highs, lows, closes;
    if (sqlite3_prepare_v2(db, sql, -1, &stmt, nullptr) == SQLITE_OK) {
        sqlite3_bind_text(stmt, 1, stockKey.toUtf8().constData(), -1, SQLITE_TRANSIENT);
        while (sqlite3_step(stmt) == SQLITE_ROW) {
            dates.append(sqlite3_column_int(stmt, 0));
            highs.append(sqlite3_column_double(stmt, 1));
            lows.append(sqlite3_column_double(stmt, 2));
            closes.append(sqlite3_column_double(stmt, 3));
        }
        sqlite3_finalize(stmt);
    }
    sqlite3_close(db);

    if (closes.size() < 2) return info;

    info.valid = true;
    info.curClose = closes.last();
    double maxH = -1.0, minL = 1e12;
    int maxD = 0, minD = 0;
    for (int i = 0; i < highs.size(); ++i) {
        if (highs[i] > maxH) { maxH = highs[i]; maxD = dates[i]; }
        if (lows[i] > 0 && lows[i] < minL) { minL = lows[i]; minD = dates[i]; }
    }
    info.peakHigh = maxH;
    info.peakDate = maxD;
    info.rangeLow = (minL < 1e11) ? minL : info.curClose;
    info.lowDate = minD;
    if (maxH > 0) info.drawdownPct = (info.curClose - maxH) / maxH * 100.0;

    const int n = closes.size();
    if (n >= 6 && closes[n - 6] > 0) info.chg5dPct = (info.curClose - closes[n - 6]) / closes[n - 6] * 100.0;
    if (n >= 21 && closes[n - 21] > 0) info.chg20dPct = (info.curClose - closes[n - 21]) / closes[n - 21] * 100.0;

    const int maN = qMin(20, n);
    double sum = 0.0;
    for (int i = n - maN; i < n; ++i) sum += closes[i];
    const double ma20 = sum / maN;
    if (ma20 > 0) info.ma20BiasPct = (info.curClose - ma20) / ma20 * 100.0;
    return info;
}

void HudOverlay::reloadMarketBreadth() {
    m_breadth.clear();
    const QString path = dbPath();
    if (!QFile::exists(path)) return;

    sqlite3 *db = nullptr;
    if (sqlite3_open_v2(path.toUtf8().constData(), &db, SQLITE_OPEN_READONLY, nullptr) != SQLITE_OK) {
        if (db) sqlite3_close(db);
        return;
    }

    QVector<int> dates;
    {
        const char *sql = "SELECT date FROM daily_bars GROUP BY date HAVING COUNT(*) >= 1000 ORDER BY date DESC LIMIT 20";
        sqlite3_stmt *stmt = nullptr;
        if (sqlite3_prepare_v2(db, sql, -1, &stmt, nullptr) == SQLITE_OK) {
            while (sqlite3_step(stmt) == SQLITE_ROW) dates.append(sqlite3_column_int(stmt, 0));
            sqlite3_finalize(stmt);
        }
    }
    std::reverse(dates.begin(), dates.end());

    const char *sqlDay = "SELECT close, prev_close FROM daily_bars WHERE date=? AND prev_close > 0";
    sqlite3_stmt *stmt = nullptr;
    if (sqlite3_prepare_v2(db, sqlDay, -1, &stmt, nullptr) == SQLITE_OK) {
        for (int d : dates) {
            sqlite3_reset(stmt);
            sqlite3_bind_int(stmt, 1, d);
            QVector<double> pcts;
            pcts.reserve(5600);
            int down = 0, drop5 = 0;
            while (sqlite3_step(stmt) == SQLITE_ROW) {
                const double c = sqlite3_column_double(stmt, 0);
                const double pc = sqlite3_column_double(stmt, 1);
                const double pct = (c - pc) * 100.0 / pc;
                pcts.append(pct);
                if (c < pc) down++;
                if (pct <= -5.0) drop5++;
            }
            if (!pcts.isEmpty()) {
                std::sort(pcts.begin(), pcts.end());
                BreadthDayInfo bi;
                bi.date = d;
                bi.total = pcts.size();
                bi.down = down;
                bi.downPct = down * 100.0 / bi.total;
                bi.medianPct = pcts[pcts.size() / 2];
                bi.drop5 = drop5;
                m_breadth.append(bi);
            }
        }
        sqlite3_finalize(stmt);
    }
    sqlite3_close(db);
    update();
}

// ================= 2A: 底栏贴边进度条胶囊 =================
void HudOverlay::paintDevBottomCapsule(QPainter &p, int W, int topY, int bgAlpha, const DeviationInfo &d) const {
    const int capH = 26;
    const int capY = topY - capH - 1;
    const int capX = static_cast<int>(W * 0.44);
    const int capW = W - capX - 1;
    const QColor lv = devLevelColor(d);

    // Base dark canvas
    p.fillRect(QRectF(capX, capY, capW, capH), QColor(8, 8, 8, qMin(235, bgAlpha)));

    // Progress bar fill behind text (0% -> 100%)
    const double frac = qBound(0.0, d.dev10 / 100.0, 1.0);
    p.fillRect(QRectF(capX + 1, capY + 1, (capW - 2) * frac, capH - 2),
               QColor(lv.red(), lv.green(), lv.blue(), d.triggered10 ? 95 : 52));

    // Crisp border
    p.setPen(QPen(QColor(lv.red(), lv.green(), lv.blue(), 190), 1.0));
    p.drawRect(QRectF(capX, capY, capW, capH));

    // Status badge pill on left
    const QString badge = devBadgeText(d);
    QFont fBadge("PingFang SC", 10, QFont::Bold);
    p.setFont(fBadge);
    const int badgeW = (d.triggered10 || d.triggered30) ? 94 : 68;
    p.fillRect(QRectF(capX + 4, capY + 4, badgeW, capH - 8),
               d.triggered10 ? QColor(200, 20, 20, 230) : QColor(lv.red(), lv.green(), lv.blue(), 45));
    p.setPen(d.triggered10 ? QColor(255, 255, 255) : lv);
    p.drawText(QRectF(capX + 4, capY + 4, badgeW, capH - 8), Qt::AlignCenter, badge);

    // Core numbers
    const double remPct = (d.close > 0) ? ((d.tomorrow100 / d.close - 1.0) * 100.0) : 0.0;
    const double remBoards = remPct / (d.ztRatio * 100.0);
    const QString ctrlHint = d.tomorrowZtTriggers
        ? QString(" [明板触发! 控%1]").arg(d.tomorrow100 - 0.01, 0, 'f', 2)
        : QString(" (%1%2% / %3板)").arg(remPct > 0 ? "+" : "").arg(remPct, 0, 'f', 1).arg(remBoards, 0, 'f', 1);

    const QString line = QString("偏10: %1%2%(N%3)  │  今100: %4  │  明100: %5%6  │  偏30: %7%8% (价200:%9)")
        .arg(d.dev10 > 0 ? "+" : "").arg(d.dev10, 0, 'f', 1).arg(d.n10)
        .arg(d.price100, 0, 'f', 2)
        .arg(d.tomorrow100, 0, 'f', 2).arg(ctrlHint)
        .arg(d.dev30 > 0 ? "+" : "").arg(d.dev30, 0, 'f', 1)
        .arg(d.price200, 0, 'f', 2);

    QFont fBody("PingFang SC", 11, QFont::Bold);
    p.setFont(fBody);
    p.setPen(QColor(245, 245, 245));
    p.drawText(QRectF(capX + badgeW + 12, capY, capW - badgeW - 18, capH), Qt::AlignLeft | Qt::AlignVCenter, line);
}

// ================= 2B: 主图右上角战术卡片 + 3日松绑推演 =================
void HudOverlay::paintDevTopRightCard(QPainter &p, int W, int bgAlpha, const DeviationInfo &d) const {
    const int cardW = 372;
    const int cardH = 92;
    const int x0 = W - cardW - 12;
    const int y0 = 36;
    const QColor lv = devLevelColor(d);

    // Background + top accent strip
    p.fillRect(QRectF(x0, y0, cardW, cardH), QColor(10, 8, 8, qMin(232, bgAlpha)));
    p.fillRect(QRectF(x0, y0, cardW, 24), QColor(28, 6, 6, qMin(240, bgAlpha)));
    p.setPen(QPen(QColor(lv.red(), lv.green(), lv.blue(), 200), 1.0));
    p.drawRect(QRectF(x0, y0, cardW, cardH));

    // Header row
    QFont fHdr("PingFang SC", 11, QFont::Bold);
    p.setFont(fHdr);
    p.setPen(QColor(255, 255, 0));
    p.drawText(QRectF(x0 + 8, y0 + 2, 180, 20), Qt::AlignLeft | Qt::AlignVCenter,
               QString("%1 异动战术卡").arg(m_snap.stockName.isEmpty() ? m_snap.stockKey.mid(1) : m_snap.stockName));

    const QString badge = QString("%1  偏10 %2%3%")
        .arg(devBadgeText(d))
        .arg(d.dev10 > 0 ? "+" : "").arg(d.dev10, 0, 'f', 1);
    p.setPen(lv);
    p.drawText(QRectF(x0 + 180, y0 + 2, cardW - 188, 20), Qt::AlignRight | Qt::AlignVCenter, badge);

    // Dual Progress Bars (10d & 30d)
    auto drawBar = [&](int by, const QString &lbl, double val, double maxV, const QColor &col, const QString &rightTxt) {
        QFont fSmall("PingFang SC", 10);
        p.setFont(fSmall);
        p.setPen(QColor(210, 210, 210));
        p.drawText(QRectF(x0 + 8, by, 34, 14), Qt::AlignLeft | Qt::AlignVCenter, lbl);

        const int bx = x0 + 44;
        const int bw = 158;
        p.fillRect(QRectF(bx, by + 2, bw, 10), QColor(28, 28, 28));
        p.setPen(QPen(QColor(70, 20, 20), 1.0));
        p.drawRect(QRectF(bx, by + 2, bw, 10));
        const double frac = qBound(0.0, val / maxV, 1.0);
        p.fillRect(QRectF(bx + 1, by + 3, (bw - 2) * frac, 8), col);

        p.setPen(col);
        p.drawText(QRectF(bx + bw + 8, by, cardW - (bx - x0) - bw - 16, 14), Qt::AlignRight | Qt::AlignVCenter, rightTxt);
    };

    const QString r10 = QString("%1%2% (N%3 / 价%4)")
        .arg(d.dev10 > 0 ? "+" : "").arg(d.dev10, 0, 'f', 1).arg(d.n10).arg(d.price100, 0, 'f', 2);
    drawBar(y0 + 30, "10日", d.dev10, 100.0, lv, r10);

    const QColor col30 = d.triggered30 ? QColor(255, 45, 45)
                        : (d.dev30 >= 140.0 ? QColor(255, 160, 0) : QColor(150, 150, 170));
    const QString r30 = QString("%1%2% (价200: %3)")
        .arg(d.dev30 > 0 ? "+" : "").arg(d.dev30, 0, 'f', 1).arg(d.price200, 0, 'f', 2);
    drawBar(y0 + 49, "30日", d.dev30, 200.0, col30, r30);

    // 3-Day Rolling Forecast Row (今100 → 明100 → 后100)
    const QString relaxTag = (d.day2_100 > d.tomorrow100 + 0.05) ? "↑松绑" : "持平";
    const QString forecast = QString("3日红线:  今 %1 → 明 %2%3 → 后 %4 (%5)")
        .arg(d.price100, 0, 'f', 2)
        .arg(d.tomorrow100, 0, 'f', 2)
        .arg(d.tomorrowZtTriggers ? "(板触!)" : "")
        .arg(d.day2_100, 0, 'f', 2)
        .arg(relaxTag);
    QFont fCast("PingFang SC", 10, QFont::Bold);
    p.setFont(fCast);
    p.setPen(d.tomorrowZtTriggers ? QColor(255, 120, 60) : QColor(0, 235, 255));
    p.drawText(QRectF(x0 + 8, y0 + 69, cardW - 16, 18), Qt::AlignLeft | Qt::AlignVCenter, forecast);
}

// ================= 2D: 主图右侧控盘三价标尺 =================
void HudOverlay::paintDevRightRuler(QPainter &p, int W, int topY, int bgAlpha, const DeviationInfo &d) const {
    const int boxW = 196;
    const int boxH = 152;
    const int x0 = W - boxW - 12;
    const int y0 = qMax(140, (topY - boxH) / 2);
    const QColor lv = devLevelColor(d);

    p.fillRect(QRectF(x0, y0, boxW, boxH), QColor(10, 8, 8, qMin(228, bgAlpha)));
    p.setPen(QPen(QColor(lv.red(), lv.green(), lv.blue(), 190), 1.0));
    p.drawRect(QRectF(x0, y0, boxW, boxH));

    // Title
    QFont fHdr("PingFang SC", 10, QFont::Bold);
    p.setFont(fHdr);
    p.fillRect(QRectF(x0 + 1, y0 + 1, boxW - 2, 20), QColor(26, 6, 6, 230));
    p.setPen(lv);
    p.drawText(QRectF(x0 + 6, y0 + 1, boxW - 12, 20), Qt::AlignLeft | Qt::AlignVCenter,
               QString("控盘标尺 · %1").arg(devBadgeText(d)));

    // Compute vertical scale covering [curClose, tomorrowZtPrice, tomorrow100]
    const double curP = d.close;
    const double ztP = d.tomorrowZtPrice;
    const double redP = d.tomorrow100;
    const double minP = qMin(curP, qMin(ztP, redP)) * 0.985;
    const double maxP = qMax(curP, qMax(ztP, redP)) * 1.015;
    const double span = qMax(0.05, maxP - minP);

    const int railX = x0 + 18;
    const int railT = y0 + 32;
    const int railB = y0 + boxH - 26;
    const int railH = railB - railT;
    auto yFor = [&](double price) -> double {
        return railB - qBound(0.0, (price - minP) / span, 1.0) * railH;
    };

    // Vertical rail + danger zone above redP
    p.fillRect(QRectF(railX - 2, railT, 4, railH), QColor(50, 50, 50));
    const double yRed = yFor(redP);
    p.fillRect(QRectF(railX - 3, railT, 6, qMax(0.0, yRed - railT)), QColor(220, 35, 35, 190));

    struct Mark { double price; double y; QString label; QColor col; };
    QVector<Mark> marks = {
        {ztP,  yFor(ztP),  QString("明涨停 %1").arg(ztP, 0, 'f', 2), QColor(255, 80, 160)},
        {redP, yRed,       QString("红线 %1 (控%2)").arg(redP, 0, 'f', 2).arg(redP - 0.01, 0, 'f', 2), QColor(255, 60, 40)},
        {curP, yFor(curP), QString("现价 %1").arg(curP, 0, 'f', 2), QColor(255, 255, 0)}
    };
    std::sort(marks.begin(), marks.end(), [](const Mark &a, const Mark &b) { return a.y < b.y; });
    // Avoid overlapping labels vertically
    for (int i = 1; i < marks.size(); ++i) {
        if (marks[i].y - marks[i - 1].y < 20.0) {
            marks[i].y = marks[i - 1].y + 20.0;
        }
    }

    QFont fLbl("PingFang SC", 10, QFont::Bold);
    p.setFont(fLbl);
    for (const auto &m : marks) {
        const double actualY = yFor(m.price);
        p.setPen(QPen(m.col, 1.5));
        p.drawLine(QPointF(railX - 6, actualY), QPointF(railX + 10, actualY));
        p.setPen(m.col);
        p.drawText(QRectF(railX + 14, m.y - 9, boxW - 36, 18), Qt::AlignLeft | Qt::AlignVCenter, m.label);
    }

    // Bottom distance summary
    const double diff = redP - curP;
    const double diffPct = (curP > 0) ? (diff / curP * 100.0) : 0.0;
    QFont fFoot("PingFang SC", 10);
    p.setFont(fFoot);
    p.setPen(diff <= 0 ? QColor(255, 60, 60) : QColor(0, 235, 255));
    p.drawText(QRectF(x0 + 6, y0 + boxH - 20, boxW - 12, 18), Qt::AlignCenter,
               QString("距明红线: %1%2 (%3%4%)")
                   .arg(diff > 0 ? "+" : "").arg(diff, 0, 'f', 2)
                   .arg(diffPct > 0 ? "+" : "").arg(diffPct, 0, 'f', 1));
}

void HudOverlay::paintEvent(QPaintEvent *) {
    QPainter p(this);
    p.setRenderHint(QPainter::Antialiasing, true);
    p.setRenderHint(QPainter::TextAntialiasing, true);

    const int W = width();
    const int H = height();
    const int bgAlpha = static_cast<int>(255 * m_opacity);

    // Covers the 2nd (unused) Volume sub-chart (including its header line) + bottom indicator/news/status strips (~17.2%)
    const int barH = qBound(140, static_cast<int>(H * 0.172), 160);
    const int topY = H - barH;

    // 1. Native TDX pure black canvas + classic dark-red 1px borders
    p.fillRect(QRectF(0, topY, W, barH), QColor(0, 0, 0, bgAlpha));
    p.fillRect(QRectF(0, topY, W, 24), QColor(16, 0, 0, bgAlpha)); // TDX dark-red header strip

    p.setPen(QPen(QColor(135, 0, 0, bgAlpha), 1.0));
    p.drawLine(0, topY, W, topY);
    p.setPen(QPen(QColor(90, 0, 0, bgAlpha), 1.0));
    p.drawLine(0, topY + 24, W, topY + 24);
    p.drawLine(W - 1, topY, W - 1, H);

    // 3 balanced columns
    const int div1 = static_cast<int>(W * 0.44);
    const int div2 = static_cast<int>(W * 0.72);
    p.setPen(QPen(QColor(110, 0, 0, bgAlpha), 1.0));
    p.drawLine(div1, topY, div1, H);
    p.drawLine(div2, topY, div2, H);

    QFont fHdr("PingFang SC", 11, QFont::Bold);
    QFont fBig("PingFang SC", 13, QFont::Bold);
    QFont fBody("PingFang SC", 11);
    QFont fTiny("Menlo", 9);

    // ================= PANEL 1 (LEFT): 20-Day Market Breadth & Declining Stocks =================
    if (!m_breadth.isEmpty()) {
        const auto &last = m_breadth.last();
        const bool isIce = (last.down >= 4500);
        const QColor bCol = isIce ? QColor(0, 230, 0) : (last.down >= 3500 ? QColor(255, 200, 0) : QColor(255, 50, 50));
        const QString dStr = QString::number(last.date);
        const QString md = dStr.size() == 8 ? (dStr.mid(4, 2) + "-" + dStr.mid(6, 2)) : dStr;
        const QString badge = isIce ? "极端冰点" : (last.down >= 3500 ? "普跌偏弱" : "情绪活跃");

        p.setFont(fHdr);
        p.setPen(QColor(255, 255, 0));
        p.drawText(QRectF(10, topY + 2, 230, 20), Qt::AlignLeft | Qt::AlignVCenter,
                   QString("全市场每日下跌家数 (近%1日)").arg(m_breadth.size()));
        p.setPen(bCol);
        p.drawText(QRectF(230, topY + 2, div1 - 240, 20), Qt::AlignRight | Qt::AlignVCenter,
                   QString("%1 跌%2家 [%3]").arg(md).arg(last.down).arg(badge));

        // Summary metrics row
        p.setFont(fBody);
        p.setPen(QColor(210, 210, 210));
        p.drawText(QRectF(10, topY + 26, div1 - 20, 18), Qt::AlignLeft | Qt::AlignVCenter,
                   QString("下跌占比 %1%   涨跌中位 %2%3%   跌超5%: %4 家")
                       .arg(last.downPct, 0, 'f', 1)
                       .arg(last.medianPct > 0 ? "+" : "").arg(last.medianPct, 0, 'f', 2)
                       .arg(last.drop5));

        // 20-day bar chart
        const double chartL = 38.0;
        const double chartR = div1 - 12.0;
        const double chartT = topY + 48.0;
        const double chartB = H - 17.0;
        const double chartW = qMax(60.0, chartR - chartL);
        const double chartH = qMax(40.0, chartB - chartT);

        auto drawRefLine = [&](int val, const QColor &col, const QString &lbl) {
            const double y = chartB - (val / 5600.0) * chartH;
            QPen rp(col, 1.0, Qt::DashLine);
            p.setPen(rp);
            p.drawLine(QPointF(chartL, y), QPointF(chartR, y));
            p.setFont(fTiny);
            p.setPen(col);
            p.drawText(QRectF(2, y - 7, 34, 14), Qt::AlignRight | Qt::AlignVCenter, lbl);
        };
        drawRefLine(4000, QColor(140, 90, 0), "4000");
        drawRefLine(5000, QColor(0, 140, 0), "5000");

        const int cnt = m_breadth.size();
        const double slotW = chartW / cnt;
        const double bw = qMax(4.0, slotW * 0.68);
        for (int i = 0; i < cnt; ++i) {
            const auto &bi = m_breadth[i];
            const double frac = qBound(0.05, bi.down / 5600.0, 1.0);
            const double bh = frac * chartH;
            const double bx = chartL + i * slotW + (slotW - bw) * 0.5;
            const double by = chartB - bh;
            QColor bc = bi.down >= 4500 ? QColor(0, 220, 0)
                      : (bi.down >= 3500 ? QColor(235, 170, 0) : QColor(230, 50, 50));
            if (i == cnt - 1) bc = bc.lighter(115);
            p.fillRect(QRectF(bx, by, bw, bh), bc);

            if (i == 0 || i == cnt - 1 || i % 4 == 0) {
                const QString ds = QString::number(bi.date);
                const QString sMd = ds.size() == 8 ? (ds.mid(4, 2) + "/" + ds.mid(6, 2)) : ds;
                p.setFont(fTiny);
                p.setPen(i == cnt - 1 ? QColor(255, 255, 0) : QColor(130, 130, 130));
                p.drawText(QRectF(bx - 12, chartB + 2, bw + 24, 15), Qt::AlignCenter, sMd);
            }
        }
    }

    // ================= PANEL 2 (MIDDLE): Active Stock Cycle & 6-Month Position =================
    {
        const int x0 = div1 + 12;
        const int cw = div2 - div1 - 24;

        p.setFont(fHdr);
        p.setPen(QColor(255, 255, 0));
        const QString sTitle = m_snap.stockKey.isEmpty()
            ? "等待通达信切股..."
            : QString("%1 %2").arg(m_snap.stockName, m_snap.stockKey.mid(1));
        p.drawText(QRectF(x0, topY + 2, cw * 0.55, 20), Qt::AlignLeft | Qt::AlignVCenter, sTitle);

        if (!m_snap.category.isEmpty()) {
            p.setPen(QColor(0, 255, 255));
            p.drawText(QRectF(x0 + cw * 0.50, topY + 2, cw * 0.50, 20), Qt::AlignRight | Qt::AlignVCenter,
                       QString("[%1] 第%2/%3")
                           .arg(m_snap.category)
                           .arg(m_snap.activeRank > 0 ? QString::number(m_snap.activeRank) : "-")
                           .arg(m_snap.peerCount));
        }

        if (m_snap.cycle.valid) {
            const auto &cy = m_snap.cycle;
            const QString pkD = QString::number(cy.peakDate);
            const QString pkMd = pkD.size() == 8 ? (pkD.mid(4, 2) + "-" + pkD.mid(6, 2)) : pkD;
            const QString loD = QString::number(cy.lowDate);
            const QString loMd = loD.size() == 8 ? (loD.mid(4, 2) + "-" + loD.mid(6, 2)) : loD;

            p.setFont(fBig);
            p.setPen(cy.drawdownPct <= -40.0 ? QColor(0, 230, 0) : QColor(255, 215, 0));
            p.drawText(QRectF(x0, topY + 26, cw, 20), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("6月高点回撤: %1%").arg(cy.drawdownPct, 0, 'f', 1));

            p.setFont(fBody);
            p.setPen(QColor(210, 210, 210));
            p.drawText(QRectF(x0, topY + 48, cw, 18), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("区间高: %1 (%2)   区间低: %3 (%4)")
                           .arg(cy.peakHigh, 0, 'f', 2).arg(pkMd)
                           .arg(cy.rangeLow, 0, 'f', 2).arg(loMd));

            p.setPen(QColor(185, 185, 185));
            p.drawText(QRectF(x0, topY + 68, cw, 18), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("5日: %1%2%   20日: %3%4%   距MA20: %5%6%")
                           .arg(cy.chg5dPct > 0 ? "+" : "").arg(cy.chg5dPct, 0, 'f', 1)
                           .arg(cy.chg20dPct > 0 ? "+" : "").arg(cy.chg20dPct, 0, 'f', 1)
                           .arg(cy.ma20BiasPct > 0 ? "+" : "").arg(cy.ma20BiasPct, 0, 'f', 1));

            const double gaugeY = topY + 92.0;
            const double gaugeH = 10.0;
            p.fillRect(QRectF(x0, gaugeY, cw, gaugeH), QColor(28, 28, 28));
            p.setPen(QPen(QColor(90, 0, 0), 1.0));
            p.drawRect(QRectF(x0, gaugeY, cw, gaugeH));

            double pos = 0.5;
            if (cy.peakHigh > cy.rangeLow) {
                pos = qBound(0.0, (cy.curClose - cy.rangeLow) / (cy.peakHigh - cy.rangeLow), 1.0);
            }
            p.fillRect(QRectF(x0 + 1, gaugeY + 1, (cw - 2) * pos, gaugeH - 2),
                       pos >= 0.65 ? QColor(210, 40, 40) : (pos <= 0.35 ? QColor(0, 180, 0) : QColor(210, 160, 0)));

            p.setFont(fTiny);
            p.setPen(QColor(150, 150, 150));
            p.drawText(QRectF(x0, gaugeY + 16, cw, 16), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("低 %1").arg(cy.rangeLow, 0, 'f', 2));
            p.setPen(QColor(255, 255, 0));
            p.drawText(QRectF(x0, gaugeY + 16, cw, 16), Qt::AlignCenter,
                       QString("现位 %1%").arg(pos * 100.0, 0, 'f', 0));
            p.setPen(QColor(150, 150, 150));
            p.drawText(QRectF(x0, gaugeY + 16, cw, 16), Qt::AlignRight | Qt::AlignVCenter,
                       QString("高 %1").arg(cy.peakHigh, 0, 'f', 2));
        }
    }

    // ================= PANEL 3 (RIGHT): Category Dispersion (Top 3 Leaders + Bottom 2 Laggards) =================
    {
        const int x0 = div2 + 12;
        const int cw = W - div2 - 22;

        p.setFont(fHdr);
        p.setPen(m_snap.avgPct >= 0 ? QColor(255, 50, 50) : QColor(0, 230, 0));
        p.drawText(QRectF(x0, topY + 2, cw, 20), Qt::AlignLeft | Qt::AlignVCenter,
                   QString("同类强弱  均%1%2%  涨%3/跌%4  板%5")
                       .arg(m_snap.avgPct > 0 ? "+" : "").arg(m_snap.avgPct, 0, 'f', 2)
                       .arg(m_snap.upCnt).arg(m_snap.downCnt).arg(m_snap.limitUpCnt));

        struct RowItem { QString tag; PeerQuote q; bool isLeader; };
        QVector<RowItem> rows;
        for (int i = 0; i < qMin(3, m_snap.topLeaders.size()); ++i) {
            rows.append({QString("领%1").arg(i + 1), m_snap.topLeaders[i], true});
        }
        for (int i = 0; i < qMin(2, m_snap.bottomLaggards.size()); ++i) {
            rows.append({QString("弱%1").arg(i + 1), m_snap.bottomLaggards[i], false});
        }

        const double startY = topY + 32.0;
        const double rowH = qMin(28.0, (H - startY - 8.0) / qMax(1, rows.size()));
        for (int i = 0; i < rows.size(); ++i) {
            const double ry = startY + i * rowH;
            const auto &it = rows[i];
            const QColor pCol = it.q.pct > 0 ? QColor(255, 50, 50) : (it.q.pct < 0 ? QColor(0, 230, 0) : QColor(220, 220, 220));

            p.setFont(fTiny);
            p.setPen(it.isLeader ? QColor(255, 80, 80) : QColor(0, 200, 0));
            p.drawText(QRectF(x0, ry, 28, rowH), Qt::AlignLeft | Qt::AlignVCenter, it.tag);

            p.setFont(fBody);
            p.setPen(QColor(255, 255, 0));
            p.drawText(QRectF(x0 + 30, ry, 86, rowH), Qt::AlignLeft | Qt::AlignVCenter, it.q.name);

            const double barX = x0 + 118;
            const double barMaxW = qMax(20.0, cw - 190.0);
            const double barW = qBound(2.0, (qAbs(it.q.pct) / 10.0) * barMaxW, barMaxW);
            p.fillRect(QRectF(barX, ry + rowH * 0.30, barW, rowH * 0.40),
                       QColor(pCol.red(), pCol.green(), pCol.blue(), 170));

            p.setPen(pCol);
            p.drawText(QRectF(x0 + cw - 68, ry, 68, rowH), Qt::AlignRight | Qt::AlignVCenter,
                       QString("%1%2%").arg(it.q.pct > 0 ? "+" : "").arg(it.q.pct, 0, 'f', 2));
        }
    }

    // ================= FLOATING SEVERE DEVIATION OVERLAYS (2A / 2B / 2D / ALL) =================
    if (m_devMode != DevHudMode::Off && m_snap.deviation.valid) {
        const auto &d = m_snap.deviation;
        switch (m_devMode) {
        case DevHudMode::BottomCapsule:
            paintDevBottomCapsule(p, W, topY, bgAlpha, d);
            break;
        case DevHudMode::TopRightCard:
            paintDevTopRightCard(p, W, bgAlpha, d);
            break;
        case DevHudMode::RightRuler:
            paintDevRightRuler(p, W, topY, bgAlpha, d);
            break;
        case DevHudMode::AllInOne:
            paintDevBottomCapsule(p, W, topY, bgAlpha, d);
            paintDevTopRightCard(p, W, bgAlpha, d);
            paintDevRightRuler(p, W, topY, bgAlpha, d);
            break;
        case DevHudMode::Off:
            break;
        }
    }
}
