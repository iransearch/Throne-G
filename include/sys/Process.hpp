#pragma once

#include <memory>
#include <QElapsedTimer>
#include <QProcess>

namespace Configs_sys {
    class CoreProcess : public QProcess
    {
    public:
        QString tag;
        QString program;
        QStringList arguments;

        ~CoreProcess();

        // start & kill is one time

        void Start();

        void Kill();

        // socketName remains in the signature only for source compatibility with
        // the existing MainWindow constructor call; the Core no longer uses it.
        CoreProcess(const QString &core_path, const QString &socketName, bool debugMode);

        void Restart();

        int start_profile_when_core_is_up = -1;

    private:
        int m_rpcPort = 19810;
        bool m_debugMode = false;
        bool show_stderr = false;
        bool failed_to_start = false;
        bool restarting = false;

        QElapsedTimer coreRestartTimer;

    protected:
        bool started = false;
        bool crashed = false;
    };

    inline QAtomicInt logCounter;
} // namespace Configs_sys
