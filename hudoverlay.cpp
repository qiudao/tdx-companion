#include "hudoverlay.h"

#include <QDir>
#include <QFile>
#include <QGuiApplication>
#include <QPainter>
#include <QPainterPath>
#include <QScreen>
#include <algorithm>
#include <sqlite3.h>

HudOverlay::HudOverlay(QWidget *parent) : QWidget(parent) {
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
    // Cover the entire workspace area to the left of the right-docked 220px companion strip
    setGeometry(avail.left(), avail.top(), avail.width() - 224, avail.height());

    reloadMarketBreadth();
}

QString HudOverlay::dbPath() const {
    return QDir::homePath() + "/.tdx_local_data/market.db";
}

void HudOverlay::cycleOpacity() {
    static const double levels[3] = {0.42, 0.72, 0.92};
    m_opacityIdx = (m_opacityIdx + 1) % 3;
    m_opacity = levels[m_opacityIdx];
    update();
}

QString HudOverlay::opacityLabel() const {
    if (m_opacityIdx == 0) return "极淡";
    if (m_opacityIdx == 1) return "标准";
    return "高对比";
}

void HudOverlay::updateSnapshot(const HudSnapshot &snap) {
    m_snap = snap;
    if (!m_snap.stockKey.isEmpty() && !m_snap.cycle.valid) {
        m_snap.cycle = queryStockCycle(m_snap.stockKey);
    }
    update();
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

    const char *sql = "SELECT date, high, close FROM daily_bars WHERE key=? AND date >= 20260620 ORDER BY date ASC";
    sqlite3_stmt *stmt = nullptr;
    QVector<int> dates;
    QVector<double> highs, closes;
    if (sqlite3_prepare_v2(db, sql, -1, &stmt, nullptr) == SQLITE_OK) {
        sqlite3_bind_text(stmt, 1, stockKey.toUtf8().constData(), -1, SQLITE_TRANSIENT);
        while (sqlite3_step(stmt) == SQLITE_ROW) {
            dates.append(sqlite3_column_int(stmt, 0));
            highs.append(sqlite3_column_double(stmt, 1));
            closes.append(sqlite3_column_double(stmt, 2));
        }
        sqlite3_finalize(stmt);
    }
    sqlite3_close(db);

    if (closes.size() < 2) return info;

    info.valid = true;
    info.curClose = closes.last();
    double maxH = -1.0;
    int maxD = 0;
    for (int i = 0; i < highs.size(); ++i) {
        if (highs[i] > maxH) {
            maxH = highs[i];
            maxD = dates[i];
        }
    }
    info.peakHigh = maxH;
    info.peakDate = maxD;
    if (maxH > 0) info.drawdownPct = (info.curClose - maxH) / maxH * 100.0;

    const int n = closes.size();
    if (n >= 6 && closes[n - 6] > 0) {
        info.chg5dPct = (info.curClose - closes[n - 6]) / closes[n - 6] * 100.0;
    }
    if (n >= 21 && closes[n - 21] > 0) {
        info.chg20dPct = (info.curClose - closes[n - 21]) / closes[n - 21] * 100.0;
    }
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
        const char *sql = "SELECT date FROM daily_bars GROUP BY date HAVING COUNT(*) >= 1000 ORDER BY date DESC LIMIT 10";
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

void HudOverlay::drawPill(QPainter &p, const QRectF &rect, const QColor &accent) const {
    QPainterPath path;
    path.addRoundedRect(rect, 4, 4);
    p.fillPath(path, QColor(4, 10, 16, static_cast<int>(210 * m_opacity)));
    QPen pen(QColor(accent.red(), accent.green(), accent.blue(), static_cast<int>(190 * m_opacity)), 1.0);
    p.setPen(pen);
    p.drawPath(path);
}

void HudOverlay::paintEvent(QPaintEvent *) {
    QPainter p(this);
    p.setRenderHint(QPainter::Antialiasing, true);
    p.setRenderHint(QPainter::TextAntialiasing, true);

    const int W = width();
    const int H = height();
    const int alphaText = static_cast<int>(255 * m_opacity);

    // Subtle AR-HUD corner reticle brackets (inset below TDX top menu & above bottom news bar)
    const int topSafe = 82;
    const int bottomSafe = H - 102;
    const QColor hudCyan(0, 229, 255, static_cast<int>(90 * m_opacity));
    p.setPen(QPen(hudCyan, 1.0));
    const int m = 8, L = 14;
    p.drawLine(m, topSafe, m + L, topSafe); p.drawLine(m, topSafe, m, topSafe + L);
    p.drawLine(m, bottomSafe, m + L, bottomSafe); p.drawLine(m, bottomSafe, m, bottomSafe - L);

    QFont fTitle("PingFang SC", 12, QFont::Bold);
    QFont fSmall("PingFang SC", 10);

    // 1. UPPER-LEFT K-LINE EMPTY ZONE (y = 88): Active Stock + Category Rank + Top 3 Leaders
    if (!m_snap.stockKey.isEmpty()) {
        const QRectF tl(16, topSafe + 6, 430, 58);
        const QColor accent = m_snap.avgPct >= 0 ? QColor(255, 68, 68) : QColor(0, 230, 118);
        drawPill(p, tl, accent);

        p.setFont(fTitle);
        p.setPen(QColor(255, 240, 0, alphaText));
        const QString titleStr = QString("%1 %2").arg(m_snap.stockName, m_snap.stockKey.mid(1));
        p.drawText(QRectF(tl.left() + 10, tl.top() + 5, 175, 22), Qt::AlignLeft | Qt::AlignVCenter, titleStr);

        p.setPen(QColor(0, 229, 255, alphaText));
        const QString catStr = QString("[%1]  同类第%2/%3")
            .arg(m_snap.category)
            .arg(m_snap.activeRank > 0 ? QString::number(m_snap.activeRank) : "-")
            .arg(m_snap.peerCount);
        p.drawText(QRectF(tl.left() + 185, tl.top() + 5, 235, 22), Qt::AlignRight | Qt::AlignVCenter, catStr);

        // Line 2: Category average + Top 3 leaders
        p.setFont(fSmall);
        p.setPen(m_snap.avgPct >= 0 ? QColor(255, 110, 110, alphaText) : QColor(0, 230, 118, alphaText));
        QString leadersStr;
        for (int i = 0; i < qMin(3, m_snap.topLeaders.size()); ++i) {
            const auto &ld = m_snap.topLeaders[i];
            if (i > 0) leadersStr += " · ";
            leadersStr += QString("%1 %2%3%").arg(ld.name, ld.pct > 0 ? "+" : "").arg(ld.pct, 0, 'f', 1);
        }
        const QString line2 = QString("均%1%2% (%3涨/%4跌)  领涨: %5")
            .arg(m_snap.avgPct > 0 ? "+" : "")
            .arg(m_snap.avgPct, 0, 'f', 2)
            .arg(m_snap.upCnt)
            .arg(m_snap.downCnt)
            .arg(leadersStr);
        p.drawText(QRectF(tl.left() + 10, tl.top() + 31, tl.width() - 20, 20), Qt::AlignLeft | Qt::AlignVCenter, line2);
    }

    // 2. NEXT TO PILL 1 (or stacked cleanly below if narrow): Cycle Position — stays left of TDX Level-2 order book
    if (m_snap.cycle.valid) {
        const double trX = (W >= 1060) ? 456.0 : 16.0;
        const double trY = (W >= 1060) ? (topSafe + 6) : (topSafe + 70);
        const QRectF tr(trX, trY, 326, 58);
        const QColor ddAccent = m_snap.cycle.drawdownPct <= -50.0 ? QColor(0, 229, 255) : QColor(255, 193, 7);
        drawPill(p, tr, ddAccent);

        p.setFont(fTitle);
        p.setPen(QColor(255, 255, 255, alphaText));
        const QString peakDateStr = QString::number(m_snap.cycle.peakDate);
        const QString shortDate = peakDateStr.size() == 8 ? (peakDateStr.mid(4, 2) + "-" + peakDateStr.mid(6, 2)) : peakDateStr;
        p.drawText(QRectF(tr.left() + 10, tr.top() + 5, 170, 22), Qt::AlignLeft | Qt::AlignVCenter,
                   QString("高点 %1 (%2)").arg(m_snap.cycle.peakHigh, 0, 'f', 2).arg(shortDate));

        p.setPen(m_snap.cycle.drawdownPct <= -40.0 ? QColor(0, 230, 118, alphaText) : QColor(255, 213, 79, alphaText));
        p.drawText(QRectF(tr.left() + 180, tr.top() + 5, 136, 22), Qt::AlignRight | Qt::AlignVCenter,
                   QString("回撤 %1%").arg(m_snap.cycle.drawdownPct, 0, 'f', 1));

        p.setFont(fSmall);
        p.setPen(QColor(210, 225, 235, alphaText));
        const QString line2 = QString("5日 %1%2%   20日 %3%4%   距MA20 %5%6%")
            .arg(m_snap.cycle.chg5dPct > 0 ? "+" : "").arg(m_snap.cycle.chg5dPct, 0, 'f', 1)
            .arg(m_snap.cycle.chg20dPct > 0 ? "+" : "").arg(m_snap.cycle.chg20dPct, 0, 'f', 1)
            .arg(m_snap.cycle.ma20BiasPct > 0 ? "+" : "").arg(m_snap.cycle.ma20BiasPct, 0, 'f', 1);
        p.drawText(QRectF(tr.left() + 10, tr.top() + 31, tr.width() - 20, 20), Qt::AlignLeft | Qt::AlignVCenter, line2);
    }

    // 3. BOTTOM-LEFT ABOVE TDX INDICATOR/NEWS BAR: Market Breadth & 10-Day Declining Stocks Sparkline
    if (!m_breadth.isEmpty()) {
        const auto &last = m_breadth.last();
        const QRectF br(16, bottomSafe - 62, 394, 58);
        const bool isIce = (last.down >= 4500);
        const QColor bAccent = isIce ? QColor(0, 230, 118) : (last.down >= 3500 ? QColor(255, 179, 0) : QColor(255, 82, 82));
        drawPill(p, br, bAccent);

        // Mini 10-day bar chart on left side of pill
        const double chartX = br.left() + 10;
        const double chartY = br.top() + 9;
        const double chartH = 40.0;
        const double barW = 8.0;
        const double gap = 3.0;
        for (int i = 0; i < m_breadth.size(); ++i) {
            const double frac = qBound(0.08, m_breadth[i].down / 5500.0, 1.0);
            const double bh = frac * chartH;
            QColor bc = m_breadth[i].down >= 4500 ? QColor(0, 230, 118, alphaText)
                      : (m_breadth[i].down >= 3500 ? QColor(255, 179, 0, alphaText) : QColor(255, 82, 82, alphaText));
            p.fillRect(QRectF(chartX + i * (barW + gap), chartY + (chartH - bh), barW, bh), bc);
        }

        // Right side text stats
        const double tx = chartX + m_breadth.size() * (barW + gap) + 10;
        const double tw = br.right() - tx - 10;
        const QString dStr = QString::number(last.date);
        const QString md = dStr.size() == 8 ? (dStr.mid(4, 2) + "-" + dStr.mid(6, 2)) : dStr;
        const QString badge = isIce ? "冰点" : (last.down >= 3500 ? "偏弱" : "活跃");

        p.setFont(fTitle);
        p.setPen(QColor(bAccent.red(), bAccent.green(), bAccent.blue(), alphaText));
        p.drawText(QRectF(tx, br.top() + 5, tw, 22), Qt::AlignLeft | Qt::AlignVCenter,
                   QString("全市场(%1) 跌%2 [%3]").arg(md).arg(last.down).arg(badge));

        p.setFont(fSmall);
        p.setPen(QColor(215, 225, 235, alphaText));
        p.drawText(QRectF(tx, br.top() + 31, tw, 20), Qt::AlignLeft | Qt::AlignVCenter,
                   QString("跌比%1%  中位%2%3%  跌>5%%4家")
                       .arg(last.downPct, 0, 'f', 1)
                       .arg(last.medianPct > 0 ? "+" : "").arg(last.medianPct, 0, 'f', 2)
                       .arg(last.drop5));
    }
}
