#pragma once

#include <QObject>
#include <QNetworkAccessManager>
#include <QTimer>
#include <QMap>
#include <QVector>
#include <QStringList>

struct PeerQuote {
    QString key;
    QString name;
    double price = 0.0;
    double pct = 0.0;
    double amount = 0.0; // 万元
};

class TdxBridge : public QObject {
    Q_OBJECT
public:
    explicit TdxBridge(QObject *parent = nullptr);
    ~TdxBridge() override = default;

    // Load zxg.blk + tnf name dictionary via Go Agent HTTP API (or local cache)
    bool loadWatchlistCategories(bool forceSyncFromVm, QString &errMsg);

    // Start 300ms HTTP polling against http://<VM_IP>:19876/status (ZERO Windows terminals!)
    bool startWatcher(QString &errMsg);

    // Switch Tongdaxin chart via http://<VM_IP>:19876/switch?key=0002242
    void switchTdxStock(const QString &stockKey);

    // Category queries & editing
    QString categoryForStock(const QString &stockKey) const;
    QStringList peersInCategory(const QString &category) const;
    QStringList allCategories() const;
    void setStockCategory(const QString &stockKey, const QString &newCategory);

    // Name <-> key
    QString stockName(const QString &stockKey) const;
    QString resolveStockKeyByName(const QString &name) const;
    bool isInWatchlist(const QString &stockKey) const { return m_zxgKeys.contains(stockKey); }
    int watchlistCount() const { return m_zxgKeys.size(); }

    // Fetch live quotes for a list of 7-char stock keys
    void fetchQuotes(const QStringList &keys, int requestToken);

signals:
    void activeStockDetected(const QString &stockKey, const QString &stockName);
    void quotesBatchReady(int requestToken, const QMap<QString, PeerQuote> &quotes);
    void statusMessage(const QString &msg);

private:
    bool detectVmIp();
    bool syncFilesViaHttp(const QString &cacheDir, QString &errMsg);
    void pollAgentStatus();
    void parseTnfFile(const QString &path, const QString &mktPrefix);
    void loadCustomCategories();
    void saveCustomCategories() const;
    static QString autoClassify(const QString &code, const QString &name);

    static QString gbkToUtf8(const QByteArray &gbk);
    static QString toTencentSymbol(const QString &key);

    QNetworkAccessManager *m_nam = nullptr;
    QTimer *m_pollTimer = nullptr;
    QString m_vmIp;
    QString m_lastStockName;
    QString m_lastRawTitle;
    bool m_pollInFlight = false;
    int m_failCount = 0;

    QStringList m_zxgKeys;
    QMap<QString, QString> m_keyToName;
    QMap<QString, QString> m_nameToKey;
    QMap<QString, QString> m_stockToCat;
};
