#include "tdxbridge.h"

#include <QDir>
#include <QFile>
#include <QProcess>
#include <QEventLoop>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkRequest>
#include <QNetworkReply>
#include <QRegularExpression>
#include <QUrl>
#include <iconv.h>

TdxBridge::TdxBridge(QObject *parent)
    : QObject(parent), m_nam(new QNetworkAccessManager(this)), m_pollTimer(new QTimer(this)) {
    m_pollTimer->setInterval(300);
    connect(m_pollTimer, &QTimer::timeout, this, &TdxBridge::pollAgentStatus);
}

QString TdxBridge::gbkToUtf8(const QByteArray &gbk) {
    if (gbk.isEmpty()) return {};
    iconv_t cd = iconv_open("UTF-8//IGNORE", "GB18030");
    if (cd == (iconv_t)-1) return QString::fromLatin1(gbk);

    size_t inBytes = static_cast<size_t>(gbk.size());
    size_t outCap = inBytes * 3 + 16;
    QByteArray out(static_cast<int>(outCap), '\0');
    char *inPtr = const_cast<char *>(gbk.constData());
    char *outPtr = out.data();
    size_t outLeft = outCap;

    iconv(cd, &inPtr, &inBytes, &outPtr, &outLeft);
    iconv_close(cd);
    out.resize(static_cast<int>(outCap - outLeft));
    return QString::fromUtf8(out);
}

QString TdxBridge::toTencentSymbol(const QString &key) {
    if (key.size() != 7) return {};
    const QChar m = key[0];
    const QString c = key.mid(1);
    if (m == '1' && c == "999999") return "sh000001";
    if (m == '1') return "sh" + c;
    if (m == '0') return "sz" + c;
    if (m == '2') return "bj" + c;
    return "sh" + c;
}

bool TdxBridge::detectVmIp() {
    if (!m_vmIp.isEmpty()) return true;
    // Pure local Mac command: reads Parallels VM IP without touching Windows
    QProcess proc;
    proc.start("/usr/local/bin/prlctl", {"list", "-a", "-f"});
    if (!proc.waitForFinished(4000)) return false;

    const QString out = QString::fromUtf8(proc.readAllStandardOutput());
    QRegularExpression ipRe(R"((\d+\.\d+\.\d+\.\d+))");
    for (const QString &line : out.split('\n')) {
        if (line.contains("running", Qt::CaseInsensitive) && line.contains("Windows", Qt::CaseInsensitive)) {
            auto m = ipRe.match(line);
            if (m.hasMatch()) {
                m_vmIp = m.captured(1);
                return true;
            }
        }
    }
    return false;
}

bool TdxBridge::syncFilesViaHttp(const QString &cacheDir, QString &errMsg) {
    if (!detectVmIp()) {
        errMsg = "未检测到运行中的 Parallels Windows 虚拟机";
        return false;
    }

    QDir().mkpath(cacheDir);
    const QStringList files = {"zxg.blk", "shs.tnf", "szs.tnf", "bjs.tnf"};
    for (const QString &fname : files) {
        emit statusMessage("HTTP 同步 " + fname + " ...");
        QUrl url(QString("http://%1:19876/file?name=%2").arg(m_vmIp, fname));
        QNetworkReply *reply = m_nam->get(QNetworkRequest(url));
        QEventLoop loop;
        connect(reply, &QNetworkReply::finished, &loop, &QEventLoop::quit);
        QTimer::singleShot(8000, &loop, &QEventLoop::quit);
        loop.exec();

        if (reply->error() != QNetworkReply::NoError) {
            errMsg = "连接 Go Agent 失败: " + reply->errorString();
            reply->deleteLater();
            return false;
        }
        const QByteArray data = reply->readAll();
        reply->deleteLater();
        if (!data.isEmpty()) {
            QFile outF(cacheDir + "/" + fname);
            if (outF.open(QIODevice::WriteOnly)) outF.write(data);
        }
    }
    return true;
}

void TdxBridge::parseTnfFile(const QString &path, const QString &mktPrefix) {
    QFile f(path);
    if (!f.open(QIODevice::ReadOnly)) return;
    const QByteArray buf = f.readAll();
    const int stride = 360;
    for (int i = 50; i + stride <= buf.size(); i += stride) {
        const QString code = QString::fromLatin1(buf.mid(i, 6)).trimmed();
        if (code.size() != 6) continue;
        bool isDigit = true;
        for (QChar c : code) { if (!c.isDigit()) { isDigit = false; break; } }
        if (!isDigit) continue;

        bool isStock = false;
        if (mktPrefix == "1") isStock = code.startsWith("60") || code.startsWith("68") || code.startsWith("51") || code.startsWith("58") || code == "999999";
        else if (mktPrefix == "0") isStock = code.startsWith("00") || code.startsWith("30") || code.startsWith("15") || code.startsWith("399");
        else if (mktPrefix == "2") isStock = code.startsWith("92") || code.startsWith("8") || code.startsWith("43");
        if (!isStock) continue;

        QByteArray nameRaw = buf.mid(i + 31, 16);
        int nul = nameRaw.indexOf('\0');
        if (nul >= 0) nameRaw = nameRaw.left(nul);
        const QString name = gbkToUtf8(nameRaw).trimmed();
        if (name.isEmpty()) continue;

        const QString key = mktPrefix + code;
        m_keyToName[key] = name;
        if (!m_nameToKey.contains(name)) m_nameToKey[name] = key;
    }
}

QString TdxBridge::autoClassify(const QString &c, const QString &n) {
    if (c == "999999" || c.startsWith("399") || c == "000688" || c.startsWith("159") || c.startsWith("510") || c.startsWith("588"))
        return "大盘与ETF";
    if (n.contains("东方"))
        return "东方系";
    if (n.contains("旭创") || n.contains("新易盛") || n.contains("天孚") || n.contains("剑桥") ||
        n.contains("源杰") || n.contains("亨通") || n.contains("东山精密") || n.contains("澳弘") || n.contains("宇特"))
        return "光模块PCB";
    if (n.contains("芯") || n.contains("半导体") || n.contains("微") || n.contains("华创") || n.contains("盛美") ||
        n.contains("华虹") || n.contains("兆易") || n.contains("普冉") || n.contains("江波龙") || n.contains("德明利") ||
        n.contains("长鑫") || n.contains("特气") || n.contains("风华") || n.contains("托伦斯") || n.contains("贝特利") || n.contains("卡莱特"))
        return "半导体存储";
    if (n.contains("算") || n.contains("云") || n.contains("数据") || n.contains("润泽") || n.contains("拓维") ||
        n.contains("常山") || n.contains("优刻得") || n.contains("首都在线") || n.contains("并行") ||
        n.contains("亚康") || n.contains("真视通") || n.contains("华胜") || n.contains("中贝") ||
        n.contains("润建") || n.contains("证通") || n.contains("城地") || n.contains("利通") || n.contains("海天瑞声") ||
        n.contains("平治") || n.contains("协创") || n.contains("美利") || n.contains("恒润") || n.contains("莲花") ||
        n.contains("华是") || n.contains("行云") || n.contains("中嘉") || n.contains("香江"))
        return "算力AI";
    if (n.contains("宇树") || n.contains("机电") || n.contains("轴承") || n.contains("光洋") || n.contains("福莱") ||
        n.contains("荣泰") || n.contains("东力") || n.contains("机床") || n.contains("智能") || n.contains("华依") ||
        n.contains("杰克") || n.contains("凯迪") || n.contains("奥普特") || n.contains("索辰") || n.contains("锋龙") ||
        n.contains("大业") || n.contains("冰山"))
        return "机器人智造";
    if (n.contains("传媒") || n.contains("影") || n.contains("文化") || n.contains("数创") || n.contains("数媒") ||
        n.contains("文轩") || n.contains("文投") || n.contains("芒果") || n.contains("奥雅") || n.contains("利亚德"))
        return "传媒影视";
    if (n.contains("九阳") || n.contains("食品") || n.contains("百洋") || n.contains("米业") || n.contains("德农") ||
        n.contains("国芳") || n.contains("华瓷") || n.contains("海洋王") || n.contains("照明"))
        return "消费农林";
    if (n.contains("药") || n.contains("蛋白") || n.contains("基因") || n.contains("百普") || n.contains("义翘") || n.contains("诺禾"))
        return "医药生物";
    if (n.contains("银行") || n.contains("证券") || n.contains("银之杰") || n.contains("赢时胜") || n.contains("指南针") || n.contains("天阳"))
        return "金融科技";
    if (n.contains("稀土") || n.contains("稀有") || n.contains("新材") || n.contains("铝业") || n.contains("兴长") ||
        n.contains("节能") || n.contains("电力") || n.contains("宁德") || n.contains("阳光电源") || n.contains("艾布鲁") || n.contains("华升"))
        return "新能源周期";
    return "其他自选";
}

void TdxBridge::loadCustomCategories() {
    QFile f(QDir::homePath() + "/.tdx_companion_categories.json");
    if (!f.open(QIODevice::ReadOnly)) return;
    const QJsonObject obj = QJsonDocument::fromJson(f.readAll()).object();
    for (auto it = obj.begin(); it != obj.end(); ++it) {
        m_stockToCat[it.key()] = it.value().toString();
    }
}

void TdxBridge::saveCustomCategories() const {
    QJsonObject obj;
    for (auto it = m_stockToCat.begin(); it != m_stockToCat.end(); ++it) {
        obj[it.key()] = it.value();
    }
    QFile f(QDir::homePath() + "/.tdx_companion_categories.json");
    if (f.open(QIODevice::WriteOnly)) {
        f.write(QJsonDocument(obj).toJson(QJsonDocument::Indented));
    }
}

bool TdxBridge::loadWatchlistCategories(bool forceSyncFromVm, QString &errMsg) {
    const QString cacheDir = QDir::homePath() + "/.tdx_companion_cache";
    const bool hasCache = QFile::exists(cacheDir + "/shs.tnf") && QFile::exists(cacheDir + "/zxg.blk");

    if (forceSyncFromVm || !hasCache) {
        if (!syncFilesViaHttp(cacheDir, errMsg)) {
            if (!hasCache) return false;
        }
    }

    m_zxgKeys.clear();
    m_keyToName.clear();
    m_nameToKey.clear();
    m_stockToCat.clear();

    parseTnfFile(cacheDir + "/shs.tnf", "1");
    parseTnfFile(cacheDir + "/szs.tnf", "0");
    parseTnfFile(cacheDir + "/bjs.tnf", "2");

    {
        QFile f(cacheDir + "/zxg.blk");
        if (f.open(QIODevice::ReadOnly)) {
            for (const QString &row : QString::fromLatin1(f.readAll()).split('\n', Qt::SkipEmptyParts)) {
                const QString k = row.trimmed();
                if (k.size() == 7 && (k[0] == '0' || k[0] == '1' || k[0] == '2') && !m_zxgKeys.contains(k)) {
                    m_zxgKeys.append(k);
                }
            }
        }
    }

    loadCustomCategories();
    for (const QString &k : m_zxgKeys) {
        if (!m_stockToCat.contains(k)) {
            m_stockToCat[k] = autoClassify(k.mid(1), stockName(k));
        }
    }
    saveCustomCategories();
    return !m_zxgKeys.isEmpty();
}

QString TdxBridge::categoryForStock(const QString &stockKey) const {
    if (m_stockToCat.contains(stockKey)) return m_stockToCat.value(stockKey);
    return autoClassify(stockKey.mid(1), stockName(stockKey));
}

QStringList TdxBridge::peersInCategory(const QString &category) const {
    QStringList peers;
    for (const QString &k : m_zxgKeys) {
        if (m_stockToCat.value(k) == category) peers.append(k);
    }
    return peers;
}

QStringList TdxBridge::allCategories() const {
    return {"算力AI", "半导体存储", "光模块PCB", "机器人智造", "传媒影视", "消费农林", "东方系", "医药生物", "金融科技", "新能源周期", "大盘与ETF", "其他自选"};
}

void TdxBridge::setStockCategory(const QString &stockKey, const QString &newCategory) {
    m_stockToCat[stockKey] = newCategory;
    saveCustomCategories();
}

bool TdxBridge::startWatcher(QString &errMsg) {
    if (!detectVmIp()) {
        errMsg = "未检测到运行中的 Parallels Windows 虚拟机";
        return false;
    }
    m_pollTimer->start();
    pollAgentStatus();
    return true;
}

void TdxBridge::pollAgentStatus() {
    if (m_pollInFlight) return;
    if (m_vmIp.isEmpty()) {
        if (!detectVmIp()) {
            emit statusMessage("等待 Windows 虚拟机...");
            return;
        }
    }
    m_pollInFlight = true;

    QUrl url(QString("http://%1:19876/status").arg(m_vmIp));
    QNetworkRequest req(url);
    req.setTransferTimeout(1500);
    QNetworkReply *reply = m_nam->get(req);
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        m_pollInFlight = false;
        reply->deleteLater();
        if (reply->error() != QNetworkReply::NoError) {
            m_failCount++;
            emit statusMessage("Agent未连接 (" + m_vmIp + ":19876)");
            if (m_failCount >= 5) {
                m_failCount = 0;
                m_vmIp.clear();
                detectVmIp();
            }
            return;
        }
        m_failCount = 0;
        const QJsonObject obj = QJsonDocument::fromJson(reply->readAll()).object();
        const QString title = obj["title"].toString();
        const QString stock = obj["stock"].toString().trimmed();
        const QString chartType = obj["chart_type"].toString().trimmed().toLower();

        // Parse Tongdaxin view mode (prefer win-agent chart_type, fallback to title bracket)
        QString inner;
        int lb = title.lastIndexOf('[');
        int rb = title.lastIndexOf(']');
        if (lb >= 0 && rb > lb) {
            inner = title.mid(lb + 1, rb - lb - 1).trimmed();
        }
        bool isKline = false;
        QString viewLabel;
        if (chartType == "kline") {
            isKline = true;
            viewLabel = "K线图";
        } else if (chartType == "min") {
            viewLabel = "分时图";
        } else if (chartType == "quote") {
            viewLabel = inner.contains("综合排名") ? "综合排名" : "行情报价";
        } else if (chartType == "f10") {
            viewLabel = "F10资料";
        } else if (inner.contains("分析图表")) {
            isKline = true;
            viewLabel = "K线图";
        } else if (inner.contains("分时")) {
            viewLabel = "分时图";
        } else if (inner.contains("行情报价")) {
            viewLabel = "行情报价";
        } else if (inner.contains("综合排名")) {
            viewLabel = "综合排名";
        } else if (inner.contains("基本资料") || inner.contains("F10", Qt::CaseInsensitive)) {
            viewLabel = "F10资料";
        } else if (inner.isEmpty()) {
            viewLabel = title.isEmpty() ? "未检测" : "主界面";
        } else {
            int dash = inner.indexOf('-');
            viewLabel = (dash > 0 ? inner.left(dash) : inner).left(8);
        }

        const bool titleChanged = (!title.isEmpty() && title != m_lastRawTitle);
        if (titleChanged) {
            m_lastRawTitle = title;
            QString shortT = lb >= 0 ? title.mid(lb) : title;
            emit statusMessage("联动 · " + shortT);
        }
        if (!m_hasViewState || isKline != m_lastIsKline || viewLabel != m_lastViewLabel || titleChanged) {
            m_hasViewState = true;
            m_lastIsKline = isKline;
            m_lastViewLabel = viewLabel;
            emit viewStateChanged(isKline, viewLabel, inner);
        }

        if (!stock.isEmpty() && stock != m_lastStockName) {
            const QString key = resolveStockKeyByName(stock);
            if (!key.isEmpty()) {
                m_lastStockName = stock;
                emit statusMessage(QString("跟随: %1 (%2)").arg(stockName(key), key.mid(1)));
                emit activeStockDetected(key, stockName(key));
            }
        }
    });
}

void TdxBridge::switchTdxStock(const QString &stockKey) {
    if (stockKey.size() != 7 || m_vmIp.isEmpty()) return;
    QUrl url(QString("http://%1:19876/switch?key=%2").arg(m_vmIp, stockKey));
    QNetworkReply *reply = m_nam->get(QNetworkRequest(url));
    connect(reply, &QNetworkReply::finished, reply, &QNetworkReply::deleteLater);
}

QString TdxBridge::stockName(const QString &stockKey) const {
    return m_keyToName.value(stockKey, stockKey.mid(1));
}

QString TdxBridge::resolveStockKeyByName(const QString &name) const {
    if (m_nameToKey.contains(name)) return m_nameToKey.value(name);
    QString cleaned = name;
    int dash = cleaned.indexOf('-');
    if (dash > 0) cleaned = cleaned.left(dash);
    cleaned.remove(' ');
    if (m_nameToKey.contains(cleaned)) return m_nameToKey.value(cleaned);
    for (auto it = m_nameToKey.begin(); it != m_nameToKey.end(); ++it) {
        QString k = it.key();
        k.remove(' ');
        if (k == cleaned) return it.value();
    }
    return {};
}

void TdxBridge::fetchQuotes(const QStringList &keys, int requestToken) {
    if (keys.isEmpty()) return;
    const int batchSize = 60;
    for (int start = 0; start < keys.size(); start += batchSize) {
        QStringList batchKeys, syms;
        for (int i = start; i < qMin(start + batchSize, keys.size()); ++i) {
            const QString sym = toTencentSymbol(keys[i]);
            if (!sym.isEmpty()) { batchKeys.append(keys[i]); syms.append(sym); }
        }
        if (syms.isEmpty()) continue;

        QUrl url("http://qt.gtimg.cn/q=" + syms.join(','));
        QNetworkRequest req(url);
        req.setRawHeader("User-Agent", "Mozilla/5.0");
        QNetworkReply *reply = m_nam->get(req);
        connect(reply, &QNetworkReply::finished, this, [this, reply, batchKeys, requestToken]() {
            reply->deleteLater();
            if (reply->error() != QNetworkReply::NoError) return;
            const QString text = gbkToUtf8(reply->readAll());
            const QStringList lines = text.trimmed().split('\n', Qt::SkipEmptyParts);
            QMap<QString, PeerQuote> out;
            for (int i = 0; i < qMin(lines.size(), batchKeys.size()); ++i) {
                const QStringList p = lines[i].split('~');
                if (p.size() > 37) {
                    PeerQuote q;
                    q.key = batchKeys[i];
                    q.name = p[1].trimmed();
                    q.price = p[3].toDouble();
                    q.pct = p[32].toDouble();
                    q.amount = p[37].toDouble();
                    out[q.key] = q;
                }
            }
            if (!out.isEmpty()) emit quotesBatchReady(requestToken, out);
        });
    }
}
