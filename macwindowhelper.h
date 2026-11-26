#pragma once

#include <QObject>
#include <QString>

class QWidget;

class MacForegroundWatcher : public QObject {
    Q_OBJECT
public:
    explicit MacForegroundWatcher(QObject *parent = nullptr);
    ~MacForegroundWatcher() override;

    bool isTdxOrSelfFrontmost() const { return m_tdxOrSelf; }
    QString frontmostBundleId() const { return m_bundleId; }
    QString frontmostAppName() const { return m_appName; }

    static void setWindowFloating(QWidget *widget, bool floating, bool bringToFrontNoFocus);

signals:
    void foregroundChanged(bool isTdxOrSelf, const QString &bundleId, const QString &appName);

private:
    void handleFrontmostApp(const QString &bundleId, const QString &appName, int pid);

    void *m_observer = nullptr;
    bool m_tdxOrSelf = true;
    QString m_bundleId;
    QString m_appName;
};
