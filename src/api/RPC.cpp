#include "include/api/RPC.h"

#include <utility>

#include "include/global/Configs.hpp"

#include <QElapsedTimer>
#include <QTcpSocket>
#include <QThread>

#include <exception>

namespace API {

    namespace {
        // spb throws std::runtime_error on malformed/torn input. These calls run
        // on worker threads too, so turn malformed responses into failed RPCs
        // instead of terminating the process.
        template <typename T>
        bool tryDeserialize(const std::vector<uint8_t> &resp, T &out) {
            try {
                out = spb::pb::deserialize<T>(resp);
                return true;
            } catch (const std::exception &e) {
                MW_show_log(QString("[RPC] dropped malformed response: ") + e.what());
                return false;
            } catch (...) {
                MW_show_log("[RPC] dropped malformed response");
                return false;
            }
        }
    }

    Client::Client() = default;

    Client::~Client() = default;

    void Client::Reconnect(QLocalSocket *readinessSocket) {
        Q_UNUSED(readinessSocket)

        const QString target = Configs::dataManager->settingsRepo->core_socket_name;
        const int separator = target.lastIndexOf(':');
        if (separator <= 0) {
            MW_show_log("[RPC] ProtoRPC/TCP target is not configured");
            return;
        }

        const QString host = target.left(separator);
        bool portOk = false;
        const int port = target.mid(separator + 1).toInt(&portOk);
        if (!portOk || port < 1 || port > 65535) {
            MW_show_log("[RPC] invalid ProtoRPC/TCP target: " + target);
            return;
        }

        QTcpSocket readinessProbe;
        QElapsedTimer elapsed;
        elapsed.start();
        bool ready = false;
        while (elapsed.elapsed() < 5000) {
            readinessProbe.abort();
            readinessProbe.connectToHost(host, static_cast<quint16>(port));
            const int remaining = 5000 - static_cast<int>(elapsed.elapsed());
            if (readinessProbe.waitForConnected(qMax(1, qMin(250, remaining)))) {
                ready = true;
                readinessProbe.disconnectFromHost();
                break;
            }
            QThread::msleep(50);
        }
        if (!ready) {
            MW_show_log("[RPC] failed to reach ProtoRPC/TCP target: " + target);
            return;
        }

        {
            std::lock_guard<std::mutex> lock(endpointMutex);
            rpcHost = host.toStdString();
            rpcPort = port;
        }
        MW_show_log("[RPC] ProtoRPC/TCP target ready: " + target);
    }

    int Client::Call(const QString &methodName,
                     const std::string &request,
                     std::vector<uint8_t> &response,
                     int timeoutMs) const {
        (void) timeoutMs;

        std::string host;
        int port = 0;
        {
            std::lock_guard<std::mutex> lock(endpointMutex);
            host = rpcHost;
            port = rpcPort;
        }
        if (host.empty() || port <= 0) {
            response.clear();
            return CallNotConnected;
        }

        protorpc::Client rpc(host.c_str(), port);
        std::string rawResponse;
        const std::string serviceMethod = "LibcoreService." + methodName.toStdString();
        const auto err = rpc.CallMethod(serviceMethod, &request, &rawResponse);
        if (!err.IsNil()) {
            response.clear();
            MW_show_log("[Core error] " + QString::fromStdString(err.String()));
            return 1;
        }

        response.assign(rawResponse.begin(), rawResponse.end());
        return CallOK;
    }

#define NOT_OK      \
    *rpcOK = false; \
    MW_show_log(QString("ProtoRPC/TCP call failed (code %1)\n").arg(status));

    QString Client::Start(bool *rpcOK, const libcore::LoadConfigReq &request) {
        libcore::ErrorResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("Start", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return QString::fromStdString(reply.error.value());
        }
        NOT_OK
        return "";
    }

    QString Client::Stop(bool *rpcOK) {
        libcore::EmptyReq request;
        libcore::ErrorResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("Stop", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return QString::fromStdString(reply.error.value());
        }
        NOT_OK
        return "";
    }

    libcore::QueryStatsResp Client::QueryStats() {
        libcore::EmptyReq request;
        libcore::QueryStatsResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryStats", spb::pb::serialize<std::string>(request), resp, 500);

        if (status == CallOK && tryDeserialize(resp, reply)) return reply;
        return {};
    }

    libcore::TestResp Client::Test(bool *rpcOK, const libcore::TestReq &request, QString *coreError) {
        libcore::TestResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("Test", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        if (coreError && !resp.empty())
            *coreError = QString::fromUtf8(reinterpret_cast<const char *>(resp.data()), static_cast<int>(resp.size()));
        NOT_OK
        return {};
    }

    void Client::StopTests(bool *rpcOK) {
        const libcore::EmptyReq request;
        std::vector<uint8_t> resp;
        const auto status = Call("StopTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK) {
            *rpcOK = true;
            return;
        }
        NOT_OK
    }

    libcore::QueryURLTestResponse Client::QueryURLTest(bool *rpcOK) {
        libcore::EmptyReq request;
        libcore::QueryURLTestResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryURLTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

    libcore::IPTestResp Client::IPTest(bool *rpcOK, const libcore::IPTestRequest &request, QString *coreError) {
        libcore::IPTestResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("IPTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        if (coreError && !resp.empty())
            *coreError = QString::fromUtf8(reinterpret_cast<const char *>(resp.data()), static_cast<int>(resp.size()));
        NOT_OK
        return {};
    }

    libcore::QueryIPTestResponse Client::QueryIPTest(bool *rpcOK) {
        libcore::EmptyReq request;
        libcore::QueryIPTestResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryIPTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

    libcore::GetDefaultInterfaceResponse Client::GetDefaultInterface(bool *rpcOK) const {
        libcore::EmptyReq request;
        libcore::GetDefaultInterfaceResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("GetDefaultInterface", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

    libcore::QueryAutoSelectorsResponse Client::QueryAutoSelectors(bool *rpcOK) const {
        libcore::EmptyReq request;
        libcore::QueryAutoSelectorsResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryAutoSelectors", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

    QString Client::AutoSelectorAction(bool *rpcOK, const QString &tag, const QString &action,
                                       const QString &member) const {
        libcore::AutoSelectorActionRequest request;
        request.tag = tag.toStdString();
        request.action = action.toStdString();
        request.member = member.toStdString();
        libcore::ErrorResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("AutoSelectorAction", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return QString::fromStdString(reply.error.value());
        }
        NOT_OK
        return "ProtoRPC/TCP error";
    }

    QString Client::SetSystemDNS(bool *rpcOK, const bool clear) const {
        libcore::SetSystemDNSRequest request{clear};
        std::vector<uint8_t> resp;
        const auto status = Call("SetSystemDNS", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK) {
            *rpcOK = true;
            return "";
        }
        NOT_OK
        return "ProtoRPC/TCP error";
    }

    libcore::QueryConnectionsResp Client::QueryConnections() const {
        libcore::EmptyReq request;
        libcore::QueryConnectionsResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryConnections", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) return reply;
        if (status != CallOK) MW_show_log("Failed to query connections: ProtoRPC/TCP error");
        return {};
    }

    QString Client::CheckConfig(bool *rpcOK, const QString &config, bool isXray) const {
        libcore::LoadConfigReq request;
        if (isXray) {
            request.need_xray = true;
            request.xray_config = config.toStdString();
        } else {
            request.core_config = config.toStdString();
        }
        libcore::ErrorResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("CheckConfig", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return QString::fromStdString(reply.error.value());
        }
        NOT_OK
        return "ProtoRPC/TCP error";
    }

    bool Client::IsPrivileged(bool *rpcOK) const {
        libcore::EmptyReq request;
        libcore::IsPrivilegedResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("IsPrivileged", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply.has_privilege.value();
        }
        NOT_OK
        return false;
    }

    libcore::SpeedTestResponse Client::SpeedTest(bool *rpcOK, const libcore::SpeedTestRequest &request, QString *coreError) {
        libcore::SpeedTestResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("SpeedTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        if (coreError && !resp.empty())
            *coreError = QString::fromUtf8(reinterpret_cast<const char *>(resp.data()), static_cast<int>(resp.size()));
        NOT_OK
        return {};
    }

    libcore::QuerySpeedTestResponse Client::QueryCurrentSpeedTests(bool *rpcOK) {
        const libcore::EmptyReq request;
        libcore::QuerySpeedTestResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QuerySpeedTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

    libcore::QueryCountryTestResponse Client::QueryCountryTestResults(bool *rpcOK) {
        const libcore::EmptyReq request;
        libcore::QueryCountryTestResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryCountryTest", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

    libcore::GenWgKeyPairResponse Client::GenWgKeyPair(bool *rpcOK) {
        const libcore::EmptyReq request;
        libcore::GenWgKeyPairResponse reply;
        std::vector<uint8_t> resp;
        const auto status = Call("GenWgKeyPair", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) {
            *rpcOK = true;
            return reply;
        }
        NOT_OK
        return {};
    }

#undef NOT_OK

} // namespace API
