#pragma once

#ifndef Q_MOC_RUN
#include <core/server/gen/libcore.pb.h>
#endif

#include <QString>
#include <memory>
#include <mutex>
#include <string>
#include <vector>

class QLocalSocket;

namespace API {
    class Client {
    public:
        Client();

        ~Client();

        // The local socket is retained only as a startup-ready signal while the
        // migration is in progress. RPC traffic itself is sent over gRPC/HTTP2
        // to the loopback TCP endpoint prepared by CoreProcess.
        void Reconnect(QLocalSocket *readinessSocket);

        // QString returns is error string

        QString Start(bool *rpcOK, const libcore::LoadConfigReq &request);

        QString Stop(bool *rpcOK);

        libcore::QueryStatsResp QueryStats();

        // coreError (optional): on RPC failure, receives the core's error message
        // so callers can react to it (e.g. missing Xray geo assets) rather than
        // silently dropping the failed test.
        libcore::TestResp Test(bool *rpcOK, const libcore::TestReq &request, QString *coreError = nullptr);

        void StopTests(bool *rpcOK);

        libcore::QueryURLTestResponse QueryURLTest(bool *rpcOK);

        libcore::IPTestResp IPTest(bool *rpcOK, const libcore::IPTestRequest &request, QString *coreError = nullptr);

        libcore::QueryIPTestResponse QueryIPTest(bool *rpcOK);

        QString SetSystemDNS(bool *rpcOK, bool clear) const;

        [[nodiscard]] libcore::QueryConnectionsResp QueryConnections() const;

        // isXray selects the validating core: false (default) validates a
        // sing-box config, true validates an Xray-format config.
        QString CheckConfig(bool *rpcOK, const QString& config, bool isXray = false) const;

        bool IsPrivileged(bool *rpcOK) const;

        libcore::SpeedTestResponse SpeedTest(bool *rpcOK, const libcore::SpeedTestRequest &request, QString *coreError = nullptr);

        libcore::QuerySpeedTestResponse QueryCurrentSpeedTests(bool *rpcOK);

        libcore::QueryCountryTestResponse QueryCountryTestResults(bool *rpcOK);

        libcore::GenWgKeyPairResponse GenWgKeyPair(bool *rpcOK);

        // Empty name = the OS has no default route. A local, censorship-proof
        // way to tell "my network died" from "the servers died".
        [[nodiscard]] libcore::GetDefaultInterfaceResponse GetDefaultInterface(bool *rpcOK) const;

        // Idempotent snapshot of every running auto-selector group: it clears no
        // core-side counters, so polling it alongside QueryStats is safe.
        [[nodiscard]] libcore::QueryAutoSelectorsResponse QueryAutoSelectors(bool *rpcOK) const;

        // action: "recheck" forces a full sweep now, "select" pins the group to
        // member. An empty tag targets every auto-selector group.
        QString AutoSelectorAction(bool *rpcOK, const QString &tag, const QString &action,
                                   const QString &member = {}) const;

    private:
        class GrpcTcpChannel;

        static constexpr int CallOK = 0;
        static constexpr int CallNotConnected = -1919;

        int Call(const QString &methodName,
                 const std::string &request,
                 std::vector<uint8_t> &response,
                 int timeoutMs = 0) const;

        mutable std::mutex channelMutex;
        std::shared_ptr<GrpcTcpChannel> channel;
    };

    inline Client *defaultClient;
} // namespace API
