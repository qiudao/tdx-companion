#pragma once

#include <QMainWindow>
#include <QLabel>
#include <QPushButton>
#include <QTableWidget>
#include <QTimer>
#include <QMap>
#include <QPoint>
#include "tdxbridge.h"
#include "hudoverlay.h"
#include "macwindowhelper.h"

class MainWindow : public QMainWindow {
    Q_OBJECT
public:
    explicit MainWindow(QWidget *parent = nullptr);

protected:
    bool eventFilter(QObject *obj, QEvent *event) override;
    void mousePressEvent(QMouseEvent *event) override;
    void mouseMoveEvent(QMouseEvent *event) override;
    void mouseReleaseEvent(QMouseEvent *event) override;
    void showEvent(QShowEvent *event) override;
    void hideEvent(QHideEvent *event) override;
    void changeEvent(QEvent *event) override;
    void closeEvent(QCloseEvent *event) override;

private slots:
    void onActiveStockDetected(const QString &stockKey, const QString &stockName);
    void onQuotesBatchReady(int requestToken, const QMap<QString, PeerQuote> &quotes);
    void onRefreshPeers();
    void onForceSyncCache();
    void onToggleAlwaysOnTop(bool checked);
    void onToggleFrameless(bool checked);
    void onToggleHud(bool checked);
    void onTableContextMenu(const QPoint &pos);

private:
    void setupUi();
    void applyTdxClassicTheme();
    void applyWindowFlags();
    void syncVisibilityWithTdxForeground();
    void updateViewStateLabel();
    void displayCategory(const QString &category);
    void rebuildPeerTable();
    void switchToPeer(const QString &peerKey);

    TdxBridge *m_bridge = nullptr;
    HudOverlay *m_hud = nullptr;
    MacForegroundWatcher *m_fgWatcher = nullptr;
    QTimer *m_autoTimer = nullptr;

    QString m_activeStockKey;
    QString m_selfSwitchedKey;
    QString m_curCategory;
    int m_quoteReqToken = 0;
    QMap<QString, PeerQuote> m_peerQuotes;

    bool m_followEnabled = true;
    bool m_lockCategory = false;
    bool m_alwaysOnTop = true;
    bool m_frameless = false;
    bool m_hudEnabled = true;
    bool m_tdxFrontmost = true;
    bool m_tdxInKlineView = false;
    QString m_tdxViewLabel = "检测中";
    QString m_tdxRawBracket;

    // Frameless drag state
    bool m_dragging = false;
    QPoint m_dragOffset;

    QWidget *m_headerBar = nullptr;
    QLabel *m_stockTitleLbl = nullptr;
    QLabel *m_categoryLbl = nullptr;
    QLabel *m_statLbl = nullptr;
    QLabel *m_viewStateLbl = nullptr;
    QLabel *m_statusLbl = nullptr;

    QPushButton *m_btnFollow = nullptr;
    QPushButton *m_btnLock = nullptr;
    QPushButton *m_btnOnTop = nullptr;
    QPushButton *m_btnAuto = nullptr;
    QPushButton *m_btnFrameless = nullptr;
    QPushButton *m_btnHud = nullptr;
    QTableWidget *m_peerTable = nullptr;
};
