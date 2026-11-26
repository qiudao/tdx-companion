#include "macwindowhelper.h"

#include <QWidget>
#include <unistd.h>

#import <AppKit/AppKit.h>

namespace {
bool matchTdxOrSelf(const QString &bundleId, const QString &appName, int pid) {
    if (pid == static_cast<int>(getpid())) {
        return true;
    }
    const QString bid = bundleId.toLower();
    if (bid == "com.parallels.desktop.console" ||
        bid.startsWith("com.parallels.winapp.") ||
        bid.contains("parallels")) {
        return true;
    }
    if (appName.contains("通达信") ||
        appName.contains("TdxW", Qt::CaseInsensitive) ||
        appName.contains("Parallels", Qt::CaseInsensitive)) {
        return true;
    }
    return false;
}

QString toQString(NSString *ns) {
    if (!ns) return {};
    return QString::fromUtf8([ns UTF8String]);
}
} // namespace

MacForegroundWatcher::MacForegroundWatcher(QObject *parent)
    : QObject(parent) {
    NSRunningApplication *front = [[NSWorkspace sharedWorkspace] frontmostApplication];
    if (front) {
        m_bundleId = toQString([front bundleIdentifier]);
        m_appName = toQString([front localizedName]);
        m_tdxOrSelf = matchTdxOrSelf(m_bundleId, m_appName, static_cast<int>([front processIdentifier]));
    }

    NSNotificationCenter *nc = [[NSWorkspace sharedWorkspace] notificationCenter];
    id obs = [nc addObserverForName:NSWorkspaceDidActivateApplicationNotification
                             object:nil
                              queue:[NSOperationQueue mainQueue]
                         usingBlock:^(NSNotification *note) {
        NSRunningApplication *app = note.userInfo[NSWorkspaceApplicationKey];
        if (!app) return;
        const QString bid = toQString([app bundleIdentifier]);
        const QString name = toQString([app localizedName]);
        const int pid = static_cast<int>([app processIdentifier]);
        this->handleFrontmostApp(bid, name, pid);
    }];
#if __has_feature(objc_arc)
    m_observer = (__bridge_retained void *)obs;
#else
    m_observer = static_cast<void *>([obs retain]);
#endif
}

MacForegroundWatcher::~MacForegroundWatcher() {
    if (m_observer) {
#if __has_feature(objc_arc)
        id obs = (__bridge_transfer id)m_observer;
        [[[NSWorkspace sharedWorkspace] notificationCenter] removeObserver:obs];
#else
        id obs = static_cast<id>(m_observer);
        [[[NSWorkspace sharedWorkspace] notificationCenter] removeObserver:obs];
        [obs release];
#endif
        m_observer = nullptr;
    }
}

void MacForegroundWatcher::handleFrontmostApp(const QString &bundleId, const QString &appName, int pid) {
    const bool active = matchTdxOrSelf(bundleId, appName, pid);
    if (active == m_tdxOrSelf && bundleId == m_bundleId && appName == m_appName) {
        return;
    }
    const bool stateChanged = (active != m_tdxOrSelf);
    m_tdxOrSelf = active;
    m_bundleId = bundleId;
    m_appName = appName;
    if (stateChanged) {
        emit foregroundChanged(m_tdxOrSelf, m_bundleId, m_appName);
    }
}

void MacForegroundWatcher::setWindowFloating(QWidget *widget, bool floating, bool bringToFrontNoFocus) {
    if (!widget) return;
    WId wid = widget->winId();
    if (!wid) return;
    NSView *view = reinterpret_cast<NSView *>(wid);
    NSWindow *win = [view window];
    if (!win) return;

    if (floating) {
        [win setLevel:NSFloatingWindowLevel];
        if (bringToFrontNoFocus) {
            [win orderFrontRegardless];
        }
    } else {
        [win setLevel:NSNormalWindowLevel];
        [win orderBack:nil];
    }
}
