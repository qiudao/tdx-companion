#include "mainwindow.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QKeyEvent>
#include <QMouseEvent>
#include <QCloseEvent>
#include <QMenu>
#include <QShortcut>
#include <QScreen>
#include <QGuiApplication>
#include <algorithm>

MainWindow::MainWindow(QWidget *parent)
    : QMainWindow(parent), m_bridge(new TdxBridge(this)), m_autoTimer(new QTimer(this)) {
    setWindowTitle("通达信同类自选");
    setupUi();
    applyTdxClassicTheme();

    const QRect avail = QGuiApplication::primaryScreen()->availableGeometry();
    setGeometry(avail.left(), avail.top(), 220, avail.height());

    connect(m_bridge, &TdxBridge::activeStockDetected, this, &MainWindow::onActiveStockDetected);
    connect(m_bridge, &TdxBridge::quotesBatchReady, this, &MainWindow::onQuotesBatchReady);
    connect(m_bridge, &TdxBridge::statusMessage, this, [this](const QString &msg) {
        m_statusLbl->setText(msg);
    });

    m_autoTimer->setInterval(5000);
    connect(m_autoTimer, &QTimer::timeout, this, &MainWindow::onRefreshPeers);

    // Cmd+B shortcut toggles Frameless Embedded Mode (Scheme 1 <-> Scheme 2)
    auto *scFrameless = new QShortcut(QKeySequence("Ctrl+B"), this);
    connect(scFrameless, &QShortcut::activated, this, [this]() {
        m_btnFrameless->setChecked(!m_btnFrameless->isChecked());
    });

    // Full-Screen Transparent Click-Through Car-HUD
    m_hud = new HudOverlay(nullptr);
    if (m_hudEnabled) m_hud->show();

    auto *scHud = new QShortcut(QKeySequence("Ctrl+U"), this);
    connect(scHud, &QShortcut::activated, this, [this]() {
        m_btnHud->setChecked(!m_btnHud->isChecked());
    });
    auto *scHudAlpha = new QShortcut(QKeySequence("Ctrl+Shift+U"), this);
    connect(scHudAlpha, &QShortcut::activated, this, [this]() {
        if (m_hud) {
            m_hud->cycleOpacity();
            m_statusLbl->setText("HUD 透明度: " + m_hud->opacityLabel());
        }
    });

    QTimer::singleShot(80, this, [this]() {
        QString err;
        if (!m_bridge->loadWatchlistCategories(false, err)) {
            m_statusLbl->setText("错误: " + err);
            return;
        }
        if (m_bridge->startWatcher(err)) {
            m_statusLbl->setText(QString("就绪 · 自选%1只 · ⌘B切无框").arg(m_bridge->watchlistCount()));
        } else {
            m_statusLbl->setText("错误: " + err);
        }
    });
}

void MainWindow::setupUi() {
    auto *central = new QWidget(this);
    central->setObjectName("tdxRoot");
    auto *root = new QVBoxLayout(central);
    root->setContentsMargins(2, 2, 2, 2);
    root->setSpacing(0);
    setCentralWidget(central);

    // 1. Classic TDX Top Stock & Category Header (Draggable in Frameless Mode)
    m_headerBar = new QWidget();
    m_headerBar->setObjectName("tdxHeader");
    m_headerBar->installEventFilter(this);
    auto *hdrLay = new QHBoxLayout(m_headerBar);
    hdrLay->setContentsMargins(6, 4, 6, 4);
    hdrLay->setSpacing(4);

    m_stockTitleLbl = new QLabel("等待通达信...");
    m_stockTitleLbl->setStyleSheet("color: #FFFF00; font-size: 13px; font-weight: bold; border: none; background: transparent;");

    m_categoryLbl = new QLabel("[--]");
    m_categoryLbl->setStyleSheet("color: #00FFFF; font-size: 12px; font-weight: bold; border: none; background: transparent;");
    m_categoryLbl->setAlignment(Qt::AlignRight | Qt::AlignVCenter);

    hdrLay->addWidget(m_stockTitleLbl, 1);
    hdrLay->addWidget(m_categoryLbl);
    root->addWidget(m_headerBar);

    // 2. Ultra-compact TDX-style Flat Control Strip (Zero Emojis)
    auto *ctrlBar = new QWidget();
    ctrlBar->setObjectName("tdxCtrlBar");
    auto *ctrlRow = new QHBoxLayout(ctrlBar);
    ctrlRow->setContentsMargins(3, 2, 3, 2);
    ctrlRow->setSpacing(2);

    m_btnFollow = new QPushButton("跟");
    m_btnFollow->setCheckable(true);
    m_btnFollow->setChecked(true);
    m_btnFollow->setToolTip("自动跟随通达信当前股");
    connect(m_btnFollow, &QPushButton::toggled, this, [this](bool on) { m_followEnabled = on; });

    m_btnLock = new QPushButton("锁");
    m_btnLock->setCheckable(true);
    m_btnLock->setToolTip("锁定当前分类列表不跳走");
    connect(m_btnLock, &QPushButton::toggled, this, [this](bool on) { m_lockCategory = on; });

    m_btnOnTop = new QPushButton("顶");
    m_btnOnTop->setCheckable(true);
    m_btnOnTop->setToolTip("窗口置顶");
    connect(m_btnOnTop, &QPushButton::toggled, this, &MainWindow::onToggleAlwaysOnTop);

    m_btnAuto = new QPushButton("5s");
    m_btnAuto->setCheckable(true);
    m_btnAuto->setToolTip("每 5 秒自动刷新行情");
    connect(m_btnAuto, &QPushButton::toggled, this, [this](bool on) {
        if (on) m_autoTimer->start(); else m_autoTimer->stop();
    });

    m_btnFrameless = new QPushButton("无框");
    m_btnFrameless->setCheckable(true);
    m_btnFrameless->setToolTip("切换无边框嵌入模式 (⌘B) · 拖动顶部可移动");
    connect(m_btnFrameless, &QPushButton::toggled, this, &MainWindow::onToggleFrameless);

    m_btnHud = new QPushButton("HUD");
    m_btnHud->setCheckable(true);
    m_btnHud->setChecked(m_hudEnabled);
    m_btnHud->setToolTip("全屏透明穿透抬头显示 (⌘U 开关 · ⌘⇧U 调透明度)");
    connect(m_btnHud, &QPushButton::toggled, this, &MainWindow::onToggleHud);

    auto *btnSync = new QPushButton("刷");
    btnSync->setToolTip("重新同步通达信自选股");
    connect(btnSync, &QPushButton::clicked, this, &MainWindow::onForceSyncCache);

    ctrlRow->addWidget(m_btnFollow);
    ctrlRow->addWidget(m_btnLock);
    ctrlRow->addWidget(m_btnOnTop);
    ctrlRow->addWidget(m_btnAuto);
    ctrlRow->addWidget(m_btnFrameless);
    ctrlRow->addWidget(m_btnHud);
    ctrlRow->addWidget(btnSync);
    root->addWidget(ctrlBar);

    // 3. Classic TDX Stats Bar
    m_statLbl = new QLabel("均 --  涨0/跌0");
    m_statLbl->setObjectName("tdxStatBar");
    root->addWidget(m_statLbl);

    // 4. Classic TDX High-Density Peer Table (22px rows, pure black, yellow names, red/green pct)
    m_peerTable = new QTableWidget(0, 3);
    m_peerTable->setHorizontalHeaderLabels({"名称", "现价", "涨幅%"});
    m_peerTable->verticalHeader()->setVisible(false);
    m_peerTable->verticalHeader()->setDefaultSectionSize(22);
    m_peerTable->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_peerTable->setSelectionMode(QAbstractItemView::SingleSelection);
    m_peerTable->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_peerTable->setShowGrid(true);
    m_peerTable->setContextMenuPolicy(Qt::CustomContextMenu);
    m_peerTable->horizontalHeader()->setSectionResizeMode(0, QHeaderView::Stretch);
    m_peerTable->setColumnWidth(1, 56);
    m_peerTable->setColumnWidth(2, 62);
    m_peerTable->installEventFilter(this);

    connect(m_peerTable, &QTableWidget::cellClicked, this, [this](int row, int) {
        if (auto *it = m_peerTable->item(row, 0)) {
            switchToPeer(it->data(Qt::UserRole).toString());
        }
    });
    connect(m_peerTable, &QTableWidget::customContextMenuRequested, this, &MainWindow::onTableContextMenu);
    root->addWidget(m_peerTable, 1);

    // 5. Classic TDX Bottom Status Strip
    m_statusLbl = new QLabel("连接中...");
    m_statusLbl->setObjectName("tdxStatusBar");
    root->addWidget(m_statusLbl);
}

void MainWindow::applyTdxClassicTheme() {
    setStyleSheet(R"(
        QMainWindow, QWidget {
            background-color: #000000;
            color: #D4D4D4;
            font-family: "PingFang SC", "SimHei", "Helvetica Neue", sans-serif;
        }
        QWidget#tdxRoot {
            background-color: #000000;
            border: 1px solid #800000;
        }
        QWidget#tdxHeader {
            background-color: #100000;
            border-bottom: 1px solid #660000;
        }
        QWidget#tdxCtrlBar {
            background-color: #0A0A0A;
            border-bottom: 1px solid #333333;
        }
        QLabel#tdxStatBar {
            background-color: #000000;
            border-bottom: 1px solid #440000;
            padding: 3px 6px;
            font-size: 11px;
            color: #B0B0B0;
        }
        QLabel#tdxStatusBar {
            background-color: #0A0A0A;
            border-top: 1px solid #440000;
            padding: 2px 6px;
            font-size: 10px;
            color: #888888;
        }
        QPushButton {
            background-color: #141414;
            border: 1px solid #3A3A3A;
            border-radius: 0px;
            padding: 2px 5px;
            color: #CCCCCC;
            font-size: 11px;
            min-height: 18px;
        }
        QPushButton:hover {
            border-color: #B00000;
            color: #FFFF00;
        }
        QPushButton:checked {
            background-color: #7A0000;
            border: 1px solid #CC0000;
            color: #FFFF00;
            font-weight: bold;
        }
        QTableWidget {
            background-color: #000000;
            border: none;
            gridline-color: #1C1C1C;
            selection-background-color: #000078;
            selection-color: #FFFF00;
            font-size: 12px;
            outline: none;
        }
        QHeaderView::section {
            background-color: #0D0D0D;
            color: #A0A0A0;
            border: none;
            border-right: 1px solid #222222;
            border-bottom: 1px solid #660000;
            padding: 3px 4px;
            font-size: 11px;
            font-weight: normal;
        }
        QScrollBar:vertical {
            background: #0A0A0A;
            width: 8px;
            margin: 0px;
        }
        QScrollBar::handle:vertical {
            background: #333333;
            min-height: 24px;
        }
        QScrollBar::handle:vertical:hover {
            background: #660000;
        }
        QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
            height: 0px;
        }
    )");
}

void MainWindow::applyWindowFlags() {
    const QRect curGeom = geometry();
    Qt::WindowFlags f = Qt::Window;
    if (m_frameless) f |= Qt::FramelessWindowHint;
    if (m_alwaysOnTop) f |= Qt::WindowStaysOnTopHint;
    setWindowFlags(f);
    setGeometry(curGeom);
    show();
    raise();
}

void MainWindow::onToggleAlwaysOnTop(bool checked) {
    m_alwaysOnTop = checked;
    applyWindowFlags();
}

void MainWindow::onToggleFrameless(bool checked) {
    m_frameless = checked;
    m_btnFrameless->setText(checked ? "有框" : "无框");
    applyWindowFlags();
}

void MainWindow::onToggleHud(bool checked) {
    m_hudEnabled = checked;
    if (!m_hud) return;
    if (checked) {
        m_hud->reloadMarketBreadth();
        m_hud->show();
    } else {
        m_hud->hide();
    }
}

void MainWindow::closeEvent(QCloseEvent *event) {
    if (m_hud) {
        m_hud->close();
        m_hud->deleteLater();
        m_hud = nullptr;
    }
    QMainWindow::closeEvent(event);
}

void MainWindow::mousePressEvent(QMouseEvent *event) {
    if (m_frameless && event->button() == Qt::LeftButton) {
        m_dragging = true;
        m_dragOffset = event->globalPosition().toPoint() - frameGeometry().topLeft();
        event->accept();
        return;
    }
    QMainWindow::mousePressEvent(event);
}

void MainWindow::mouseMoveEvent(QMouseEvent *event) {
    if (m_frameless && m_dragging && (event->buttons() & Qt::LeftButton)) {
        move(event->globalPosition().toPoint() - m_dragOffset);
        event->accept();
        return;
    }
    QMainWindow::mouseMoveEvent(event);
}

void MainWindow::mouseReleaseEvent(QMouseEvent *event) {
    m_dragging = false;
    QMainWindow::mouseReleaseEvent(event);
}

void MainWindow::onForceSyncCache() {
    QString err;
    if (m_bridge->loadWatchlistCategories(true, err)) {
        m_statusLbl->setText(QString("已同步自选 %1 只").arg(m_bridge->watchlistCount()));
        if (!m_activeStockKey.isEmpty()) displayCategory(m_bridge->categoryForStock(m_activeStockKey));
    } else {
        m_statusLbl->setText("错误: " + err);
    }
}

void MainWindow::onActiveStockDetected(const QString &stockKey, const QString &stockName) {
    m_activeStockKey = stockKey;
    m_stockTitleLbl->setText(QString("%1 %2").arg(stockName, stockKey.mid(1)));

    if (m_lockCategory || !m_followEnabled || stockKey == m_selfSwitchedKey) {
        if (stockKey == m_selfSwitchedKey) m_selfSwitchedKey.clear();
        rebuildPeerTable();
        return;
    }

    displayCategory(m_bridge->categoryForStock(stockKey));
}

void MainWindow::displayCategory(const QString &category) {
    m_curCategory = category;
    QStringList peers = m_bridge->peersInCategory(category);
    if (!m_activeStockKey.isEmpty() && !peers.contains(m_activeStockKey)) {
        peers.prepend(m_activeStockKey);
    }

    m_categoryLbl->setText(QString("[%1]").arg(category));
    m_peerQuotes.clear();
    for (const QString &k : peers) {
        PeerQuote q;
        q.key = k;
        q.name = m_bridge->stockName(k);
        m_peerQuotes[k] = q;
    }
    rebuildPeerTable();

    m_quoteReqToken++;
    m_bridge->fetchQuotes(peers, m_quoteReqToken);
}

void MainWindow::onRefreshPeers() {
    if (m_curCategory.isEmpty()) return;
    QStringList peers = m_bridge->peersInCategory(m_curCategory);
    if (!m_activeStockKey.isEmpty() && !peers.contains(m_activeStockKey)) peers.prepend(m_activeStockKey);
    m_quoteReqToken++;
    m_bridge->fetchQuotes(peers, m_quoteReqToken);
}

void MainWindow::onQuotesBatchReady(int requestToken, const QMap<QString, PeerQuote> &quotes) {
    if (requestToken != m_quoteReqToken) return;
    for (auto it = quotes.begin(); it != quotes.end(); ++it) {
        m_peerQuotes[it.key()] = it.value();
    }
    rebuildPeerTable();
}

void MainWindow::rebuildPeerTable() {
    QVector<PeerQuote> list;
    list.reserve(m_peerQuotes.size());
    double sumPct = 0.0;
    int upCnt = 0, downCnt = 0, limitUpCnt = 0;

    for (auto it = m_peerQuotes.begin(); it != m_peerQuotes.end(); ++it) {
        list.append(it.value());
        sumPct += it.value().pct;
        if (it.value().pct > 0) upCnt++;
        else if (it.value().pct < 0) downCnt++;
        if (it.value().pct >= 9.8) limitUpCnt++;
    }

    std::sort(list.begin(), list.end(), [](const PeerQuote &a, const PeerQuote &b) {
        return a.pct > b.pct;
    });

    int activeRank = -1;
    for (int i = 0; i < list.size(); ++i) {
        if (list[i].key == m_activeStockKey) { activeRank = i + 1; break; }
    }

    const double avg = list.isEmpty() ? 0.0 : sumPct / list.size();
    const QString avgCol = avg >= 0 ? "#FF3232" : "#00E600";
    const QString rankStr = activeRank > 0 ? QString(" 第%1/%2").arg(activeRank).arg(list.size()) : "";
    m_statLbl->setText(QString("<span style='color:%1'>均%2%3%</span>  板%4  <span style='color:#FF3232'>%5</span>/<span style='color:#00E600'>%6</span>%7")
        .arg(avgCol).arg(avg > 0 ? "+" : "").arg(avg, 0, 'f', 2)
        .arg(limitUpCnt).arg(upCnt).arg(downCnt).arg(rankStr));

    QSignalBlocker blocker(m_peerTable);
    m_peerTable->setRowCount(list.size());
    int activeRow = -1;

    for (int i = 0; i < list.size(); ++i) {
        const PeerQuote &q = list[i];
        const bool isActive = (q.key == m_activeStockKey);
        if (isActive) activeRow = i;

        auto *c0 = new QTableWidgetItem((isActive ? "▸" : "") + q.name);
        c0->setData(Qt::UserRole, q.key);
        c0->setForeground(QColor("#FFFF00")); // Classic TDX Yellow stock name

        auto *c1 = new QTableWidgetItem(q.price > 0 ? QString::number(q.price, 'f', q.price < 10 ? 3 : 2) : "--");
        c1->setTextAlignment(Qt::AlignRight | Qt::AlignVCenter);

        auto *c2 = new QTableWidgetItem(q.price > 0 ? QString("%1%2%").arg(q.pct > 0 ? "+" : "").arg(q.pct, 0, 'f', 2) : "--");
        c2->setTextAlignment(Qt::AlignRight | Qt::AlignVCenter);

        // Classic TDX Red / Green / White
        const QColor col = q.pct > 0 ? QColor("#FF3232") : (q.pct < 0 ? QColor("#00E600") : QColor("#FFFFFF"));
        c1->setForeground(col);
        c2->setForeground(col);

        if (isActive) {
            QFont f = c0->font(); f.setBold(true);
            c0->setFont(f);
            c0->setBackground(QColor("#000078"));
            c1->setBackground(QColor("#000078"));
            c2->setBackground(QColor("#000078"));
        } else if (q.pct >= 9.8) {
            QFont f = c2->font(); f.setBold(true);
            c2->setFont(f);
            c2->setBackground(QColor("#550000"));
        }

        m_peerTable->setItem(i, 0, c0);
        m_peerTable->setItem(i, 1, c1);
        m_peerTable->setItem(i, 2, c2);
    }

    if (activeRow >= 0) m_peerTable->selectRow(activeRow);

    if (m_hud) {
        HudSnapshot snap;
        snap.stockKey = m_activeStockKey;
        snap.stockName = m_activeStockKey.isEmpty() ? QString() : m_bridge->stockName(m_activeStockKey);
        snap.category = m_curCategory;
        snap.activeRank = activeRank;
        snap.peerCount = list.size();
        snap.avgPct = avg;
        snap.upCnt = upCnt;
        snap.downCnt = downCnt;
        snap.limitUpCnt = limitUpCnt;
        for (int i = 0; i < qMin(3, list.size()); ++i) snap.topLeaders.append(list[i]);
        m_hud->updateSnapshot(snap);
    }
}

void MainWindow::switchToPeer(const QString &peerKey) {
    if (peerKey.isEmpty()) return;
    m_selfSwitchedKey = peerKey;
    m_activeStockKey = peerKey;
    m_stockTitleLbl->setText(QString("%1 %2").arg(m_bridge->stockName(peerKey), peerKey.mid(1)));
    m_bridge->switchTdxStock(peerKey);
    rebuildPeerTable();
}

void MainWindow::onTableContextMenu(const QPoint &pos) {
    int row = m_peerTable->rowAt(pos.y());
    QMenu menu(this);

    if (row >= 0) {
        if (auto *it = m_peerTable->item(row, 0)) {
            const QString key = it->data(Qt::UserRole).toString();
            QMenu *catMenu = menu.addMenu("调整所属分类");
            const QString curCat = m_bridge->categoryForStock(key);
            for (const QString &cat : m_bridge->allCategories()) {
                QAction *act = catMenu->addAction(cat);
                act->setCheckable(true);
                act->setChecked(cat == curCat);
                connect(act, &QAction::triggered, this, [this, key, cat]() {
                    m_bridge->setStockCategory(key, cat);
                    displayCategory(m_curCategory);
                });
            }
            menu.addSeparator();
        }
    }

    menu.addAction(m_frameless ? "恢复窗口边框 (⌘B)" : "无边框嵌入模式 (⌘B)", [this]() {
        m_btnFrameless->setChecked(!m_btnFrameless->isChecked());
    });
    menu.addAction(m_hudEnabled ? "隐藏全屏 HUD (⌘U)" : "显示全屏 HUD (⌘U)", [this]() {
        m_btnHud->setChecked(!m_btnHud->isChecked());
    });
    if (m_hud) {
        menu.addAction(QString("切换 HUD 透明度 [当前:%1] (⌘⇧U)").arg(m_hud->opacityLabel()), [this]() {
            m_hud->cycleOpacity();
            m_statusLbl->setText("HUD 透明度: " + m_hud->opacityLabel());
        });
    }
    menu.addAction(m_alwaysOnTop ? "取消置顶" : "窗口置顶", [this]() {
        m_btnOnTop->setChecked(!m_btnOnTop->isChecked());
    });

    menu.exec(m_peerTable->viewport()->mapToGlobal(pos));
}

bool MainWindow::eventFilter(QObject *obj, QEvent *event) {
    // Double-click header bar toggles Frameless <-> Framed mode
    if (obj == m_headerBar && event->type() == QEvent::MouseButtonDblClick) {
        m_btnFrameless->setChecked(!m_btnFrameless->isChecked());
        return true;
    }
    if (obj == m_peerTable && event->type() == QEvent::KeyPress) {
        auto *ke = static_cast<QKeyEvent *>(event);
        if (ke->key() == Qt::Key_Up || ke->key() == Qt::Key_Down) {
            int cur = m_peerTable->currentRow();
            int next = qBound(0, cur + (ke->key() == Qt::Key_Down ? 1 : -1), m_peerTable->rowCount() - 1);
            if (next != cur && next >= 0) {
                m_peerTable->selectRow(next);
                if (auto *it = m_peerTable->item(next, 0)) {
                    switchToPeer(it->data(Qt::UserRole).toString());
                }
            }
            return true;
        }
    }
    return QMainWindow::eventFilter(obj, event);
}
