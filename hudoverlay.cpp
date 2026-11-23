#include "hudoverlay.h"

#include <QDir>
#include <QFile>
#include <QGuiApplication>
#include <QPainter>
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
    // Cover the workspace to the left of the right-docked 220px companion strip
    setGeometry(avail.left(), avail.top(), avail.width() - 222, avail.height());

    reloadMarketBreadth();
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

        // Reference dashed lines at 4000 and 5000
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

            // Date labels on every 4th bar + last bar
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

            // Row 1: Big Drawdown highlight
            p.setFont(fBig);
            p.setPen(cy.drawdownPct <= -40.0 ? QColor(0, 230, 0) : QColor(255, 215, 0));
            p.drawText(QRectF(x0, topY + 26, cw, 20), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("6月高点回撤: %1%").arg(cy.drawdownPct, 0, 'f', 1));

            // Row 2: Peak & Low prices
            p.setFont(fBody);
            p.setPen(QColor(210, 210, 210));
            p.drawText(QRectF(x0, topY + 48, cw, 18), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("区间高: %1 (%2)   区间低: %3 (%4)")
                           .arg(cy.peakHigh, 0, 'f', 2).arg(pkMd)
                           .arg(cy.rangeLow, 0, 'f', 2).arg(loMd));

            // Row 3: 5d / 20d / MA20 bias
            p.setPen(QColor(185, 185, 185));
            p.drawText(QRectF(x0, topY + 68, cw, 18), Qt::AlignLeft | Qt::AlignVCenter,
                       QString("5日: %1%2%   20日: %3%4%   距MA20: %5%6%")
                           .arg(cy.chg5dPct > 0 ? "+" : "").arg(cy.chg5dPct, 0, 'f', 1)
                           .arg(cy.chg20dPct > 0 ? "+" : "").arg(cy.chg20dPct, 0, 'f', 1)
                           .arg(cy.ma20BiasPct > 0 ? "+" : "").arg(cy.ma20BiasPct, 0, 'f', 1));

            // Row 4: Native TDX horizontal position gauge (Low -> Current -> High)
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

            // Mini pct bar
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
}
