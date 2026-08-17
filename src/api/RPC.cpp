#include "include/api/RPC.h"

#include <utility>

#include "include/global/Configs.hpp"

#include <QAbstractNetworkCache>
#include <QByteArray>
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QObject>
#include <QThread>
#include <QTimer>
#include <QtEndian>

#include <chrono>
#include <condition_variable>
#include <exception>
#include <memory>
#include <mutex>

namespace API {

    namespace {
        constexpr auto GrpcAcceptEncodingHeader = "grpc-accept-encoding";
        constexpr auto GrpcStatusHeader = "grpc-status";
        constexpr auto GrpcStatusMessageHeader = "grpc-message";
        constexpr auto TEHeader = "te";
        constexpr int GrpcFrameHeaderSize = 5;
        constexpr int DefaultRpcTimeoutMs = 30000;

        class NoCache final : public QAbstractNetworkCache {
        public:
            QNetworkCacheMetaData metaData(const QUrl &) override { return {}; }
            void updateMetaData(const QNetworkCacheMetaData &) override {}
            QIODevice *data(const QUrl &) override { return nullptr; }
            bool remove(const QUrl &) override { return false; }
            [[nodiscard]] qint64 cacheSize() const override { return 0; }
            QIODevice *prepare(const QNetworkCacheMetaData &) override { return nullptr; }
            void insert(QIODevice *) override {}
            void clear() override {}
        };

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

    // -----------------------------------------------------------------------
    // GrpcTcpChannel — protobuf RPC over gRPC/HTTP2 on 127.0.0.1:<port>
    // -----------------------------------------------------------------------
    class Client::GrpcTcpChannel {
        struct PendingCall {
            std::mutex mu;
            std::condition_variable cv;
            bool done = false;
            int status = 1;
            QByteArray data;
        };

        QThread *networkThread = nullptr;
        QNetworkAccessManager *networkManager = nullptr;
        QString urlBase;
        QString serviceName;

        static QByteArray makeGrpcFrame(const QByteArray &payload) {
            QByteArray frame(GrpcFrameHeaderSize, '\0');
            // byte 0 is the compression flag (0 = identity)
            qToBigEndian<quint32>(static_cast<quint32>(payload.size()),
                                  reinterpret_cast<uchar *>(frame.data() + 1));
            frame += payload;
            return frame;
        }

        static int processReply(QNetworkReply *reply, QByteArray &data) {
            if (reply->error() != QNetworkReply::NoError) {
                data = reply->errorString().toUtf8();
                return static_cast<int>(reply->error());
            }

            const int grpcStatus = reply->rawHeader(GrpcStatusHeader).toInt();
            if (grpcStatus != 0) {
                const auto rawMessage = reply->rawHeader(GrpcStatusMessageHeader);
                data = QByteArray::fromPercentEncoding(rawMessage);
                if (data.isEmpty()) data = QByteArray("gRPC status ") + QByteArray::number(grpcStatus);
                return 1000 + grpcStatus;
            }

            const QByteArray body = reply->readAll();
            if (body.isEmpty()) {
                data.clear();
                return 0;
            }
            if (body.size() < GrpcFrameHeaderSize) {
                data = "gRPC response frame is shorter than its header";
                return 2001;
            }

            const auto compressed = static_cast<quint8>(body.at(0));
            if (compressed != 0) {
                data = "compressed gRPC responses are not supported by the GUI transport";
                return 2002;
            }

            const quint32 payloadSize = qFromBigEndian<quint32>(
                reinterpret_cast<const uchar *>(body.constData() + 1));
            if (payloadSize > static_cast<quint32>(body.size() - GrpcFrameHeaderSize)) {
                data = "truncated gRPC response frame";
                return 2003;
            }

            data = body.mid(GrpcFrameHeaderSize, static_cast<int>(payloadSize));
            return 0;
        }

    public:
        GrpcTcpChannel(const QString &target, const QString &service)
            : urlBase("http://" + target), serviceName(service) {
            networkThread = new QThread;
            networkManager = new QNetworkAccessManager;
            networkManager->setCache(new NoCache);
            networkManager->moveToThread(networkThread);
            networkThread->start();
        }

        ~GrpcTcpChannel() {
            if (networkManager != nullptr) {
                networkManager->deleteLater();
            }
            if (networkThread != nullptr) {
                networkThread->quit();
                networkThread->wait();
                delete networkThread;
            }
        }

        int Call(const QString &methodName,
                 const std::string &request,
                 std::vector<uint8_t> &response,
                 int timeoutMs = 0) {
            response.clear();
            if (!Configs::dataManager->settingsRepo->core_running) {
                return Client::CallNotConnected;
            }

            const int effectiveTimeout = timeoutMs > 0 ? timeoutMs : DefaultRpcTimeoutMs;
            const auto pending = std::make_shared<PendingCall>();
            const QByteArray requestPayload = QByteArray::fromStdString(request);
            const QString callUrl = urlBase + "/" + serviceName + "/" + methodName;

            QMetaObject::invokeMethod(networkManager, [this, pending, requestPayload, callUrl, effectiveTimeout]() {
                QNetworkRequest requestObject{QUrl(callUrl)};
                requestObject.setAttribute(QNetworkRequest::Http2DirectAttribute, true);
                requestObject.setHeader(QNetworkRequest::ContentTypeHeader, QLatin1String("application/grpc"));
                requestObject.setRawHeader("Cache-Control", "no-store");
                requestObject.setRawHeader(GrpcAcceptEncodingHeader, "identity");
                requestObject.setRawHeader(TEHeader, "trailers");

                QNetworkReply *reply = networkManager->post(requestObject, makeGrpcFrame(requestPayload));
                auto *abortTimer = new QTimer(reply);
                abortTimer->setSingleShot(true);
                abortTimer->setInterval(effectiveTimeout);
                QObject::connect(abortTimer, &QTimer::timeout, reply, &QNetworkReply::abort);
                abortTimer->start();

                QObject::connect(reply, &QNetworkReply::finished, networkManager, [pending, reply]() {
                    QByteArray result;
                    const int status = processReply(reply, result);
                    {
                        std::lock_guard<std::mutex> lock(pending->mu);
                        pending->status = status;
                        pending->data = std::move(result);
                        pending->done = true;
                    }
                    pending->cv.notify_one();
                    reply->deleteLater();
                });
            }, Qt::QueuedConnection);

            std::unique_lock<std::mutex> lock(pending->mu);
            const bool completed = pending->cv.wait_for(
                lock,
                std::chrono::milliseconds(effectiveTimeout + 1500),
                [&pending] { return pending->done; });

            if (!completed) {
                const QByteArray msg = "gRPC/TCP call timed out";
                response.assign(msg.begin(), msg.end());
                return static_cast<int>(QNetworkReply::TimeoutError);
            }

            response.assign(pending->data.begin(), pending->data.end());
            return pending->status;
        }
    };

    // -----------------------------------------------------------------------
    // Client
    // -----------------------------------------------------------------------

    Client::Client() = default;

    Client::~Client() {
        std::shared_ptr<GrpcTcpChannel> old;
        {
            std::lock_guard<std::mutex> lock(channelMutex);
            old.swap(channel);
        }
    }

    void Client::Reconnect(QLocalSocket *readinessSocket) {
        Q_UNUSED(readinessSocket)

        const QString target = Configs::dataManager->settingsRepo->core_socket_name;
        if (target.isEmpty() || !target.contains(':')) {
            MW_show_log("[RPC] gRPC/TCP target is not configured");
            return;
        }

        auto replacement = std::make_shared<GrpcTcpChannel>(target, "libcore.LibcoreService");
        {
            std::lock_guard<std::mutex> lock(channelMutex);
            channel.swap(replacement);
        }
        MW_show_log("[RPC] gRPC/TCP connected target configured: " + target);
    }

    int Client::Call(const QString &methodName,
                     const std::string &request,
                     std::vector<uint8_t> &response,
                     int timeoutMs) const {
        std::shared_ptr<GrpcTcpChannel> current;
        {
            std::lock_guard<std::mutex> lock(channelMutex);
            current = channel;
        }
        if (!current) {
            response.clear();
            return CallNotConnected;
        }
        return current->Call(methodName, request, response, timeoutMs);
    }

#define NOT_OK      \
    *rpcOK = false; \
    MW_show_log(QString("gRPC/TCP call failed (code %1)\n").arg(status));

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
        return "gRPC/TCP error";
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
        return "gRPC/TCP error";
    }

    libcore::QueryConnectionsResp Client::QueryConnections() const {
        libcore::EmptyReq request;
        libcore::QueryConnectionsResp reply;
        std::vector<uint8_t> resp;
        const auto status = Call("QueryConnections", spb::pb::serialize<std::string>(request), resp);

        if (status == CallOK && tryDeserialize(resp, reply)) return reply;
        if (status != CallOK) MW_show_log("Failed to query connections: gRPC/TCP error");
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
        return "gRPC/TCP error";
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
