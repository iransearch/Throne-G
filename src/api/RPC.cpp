#include "include/api/RPC.h"

#include <utility>

#include "include/global/Configs.hpp"

#include <QAtomicInt>
#include <QByteArray>
#include <QDataStream>
#include <QElapsedTimer>
#include <QLocalSocket>
#include <QMap>
#include <QObject>
#include <QTcpSocket>
#include <QThread>

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <exception>
#include <memory>
#include <mutex>

namespace API {

    namespace {
        constexpr int DefaultRpcTimeoutMs = 30000;

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
    // ProtoRpcTcpChannel — protobuf messages with the legacy ProtoRPC framing
    // over one persistent TCP connection to 127.0.0.1:<port>.
    //
    // Request (little-endian):
    //   [uint32 reqId][uint16 methodLen][method][uint32 payloadLen][payload]
    // Response (little-endian):
    //   [uint32 reqId][uint8 status][uint32 dataLen][data]
    // -----------------------------------------------------------------------
    class Client::ProtoRpcTcpChannel {
        struct PendingCall {
            std::mutex mu;
            std::condition_variable cv;
            bool done = false;
            quint8 status = 1;
            QByteArray data;
        };

        QThread *ioThread = nullptr;
        QObject *ioAnchor = nullptr;
        QTcpSocket *socket = nullptr; // ioThread only
        QByteArray readBuffer;        // ioThread only

        QAtomicInt nextId{1};
        std::mutex pendingMutex;
        QMap<quint32, std::shared_ptr<PendingCall>> pending;
        std::atomic<bool> connected{false};

        void wakeAllWithError() {
            connected.store(false, std::memory_order_release);
            std::lock_guard<std::mutex> lock(pendingMutex);
            for (auto &call : pending) {
                std::lock_guard<std::mutex> callLock(call->mu);
                call->done = true;
                call->status = 1;
                call->cv.notify_one();
            }
            pending.clear();
        }

        // ioThread only
        void processBuffer() {
            // Response header: 4 (reqId) + 1 (status) + 4 (dataLen) = 9 bytes.
            while (readBuffer.size() >= 9) {
                quint32 reqId = 0;
                quint32 dataLen = 0;
                quint8 status = 1;
                {
                    QDataStream stream(readBuffer);
                    stream.setByteOrder(QDataStream::LittleEndian);
                    stream >> reqId >> status >> dataLen;
                }

                const qint64 totalSize = qint64(9) + dataLen;
                if (readBuffer.size() < totalSize) return;

                QByteArray data = readBuffer.mid(9, static_cast<int>(dataLen));
                readBuffer.remove(0, totalSize);

                std::shared_ptr<PendingCall> call;
                {
                    std::lock_guard<std::mutex> lock(pendingMutex);
                    call = pending.value(reqId, nullptr);
                    if (call) pending.remove(reqId);
                }
                if (!call) continue;

                std::lock_guard<std::mutex> callLock(call->mu);
                call->status = status;
                call->data = std::move(data);
                call->done = true;
                call->cv.notify_one();
            }
        }

    public:
        ProtoRpcTcpChannel() {
            ioThread = new QThread;
            ioAnchor = new QObject;
            ioAnchor->moveToThread(ioThread);
            ioThread->start();
        }

        ~ProtoRpcTcpChannel() {
            wakeAllWithError();

            if (ioThread && ioThread->isRunning()) {
                QMetaObject::invokeMethod(ioAnchor, [this]() {
                    if (socket) {
                        socket->abort();
                        delete socket;
                        socket = nullptr;
                    }
                    readBuffer.clear();
                }, Qt::BlockingQueuedConnection);
                ioThread->quit();
                ioThread->wait();
            }
            delete ioAnchor;
            delete ioThread;
        }

        bool Connect(const QString &host, quint16 port, int timeoutMs) {
            bool ok = false;
            QMetaObject::invokeMethod(ioAnchor, [this, host, port, timeoutMs, &ok]() {
                if (socket) {
                    socket->abort();
                    delete socket;
                    socket = nullptr;
                }
                readBuffer.clear();
                connected.store(false, std::memory_order_release);

                socket = new QTcpSocket;
                QElapsedTimer elapsed;
                elapsed.start();

                // The core creates the listener before sending the readiness
                // notification, but retry briefly to make restart/startup races
                // harmless on slower Windows systems.
                while (elapsed.elapsed() < timeoutMs) {
                    socket->abort();
                    socket->connectToHost(host, port);
                    const int remaining = timeoutMs - static_cast<int>(elapsed.elapsed());
                    const int waitMs = qMax(1, qMin(250, remaining));
                    if (socket->waitForConnected(waitMs)) {
                        ok = true;
                        break;
                    }
                    QThread::msleep(50);
                }

                if (!ok) {
                    delete socket;
                    socket = nullptr;
                    return;
                }

                QObject::connect(socket, &QTcpSocket::readyRead, ioAnchor, [this]() {
                    if (!socket) return;
                    readBuffer += socket->readAll();
                    processBuffer();
                });
                QObject::connect(socket, &QTcpSocket::disconnected, ioAnchor, [this]() {
                    wakeAllWithError();
                });
                connected.store(true, std::memory_order_release);
            }, Qt::BlockingQueuedConnection);
            return ok;
        }

        int Call(const QString &methodName,
                 const std::string &request,
                 std::vector<uint8_t> &response,
                 int timeoutMs = 0) {
            response.clear();
            if (!connected.load(std::memory_order_acquire)) return Client::CallNotConnected;

            const int effectiveTimeout = timeoutMs > 0 ? timeoutMs : DefaultRpcTimeoutMs;
            const quint32 reqId = static_cast<quint32>(nextId.fetchAndAddRelaxed(1));
            const QByteArray method = methodName.toUtf8();
            const QByteArray payload = QByteArray::fromStdString(request);

            QByteArray frame;
            {
                QDataStream stream(&frame, QIODevice::WriteOnly);
                stream.setByteOrder(QDataStream::LittleEndian);
                stream << reqId;
                stream << static_cast<quint16>(method.size());
                stream.writeRawData(method.constData(), method.size());
                stream << static_cast<quint32>(payload.size());
                stream.writeRawData(payload.constData(), payload.size());
            }

            auto call = std::make_shared<PendingCall>();
            {
                std::lock_guard<std::mutex> lock(pendingMutex);
                pending[reqId] = call;
            }

            QMetaObject::invokeMethod(ioAnchor, [this, frame]() {
                if (!socket || socket->state() != QAbstractSocket::ConnectedState) {
                    wakeAllWithError();
                    return;
                }
                if (socket->write(frame) < 0) {
                    wakeAllWithError();
                    return;
                }
                socket->flush();
            }, Qt::QueuedConnection);

            std::unique_lock<std::mutex> lock(call->mu);
            bool completed = call->cv.wait_for(
                lock,
                std::chrono::milliseconds(effectiveTimeout),
                [&call] { return call->done; });
            lock.unlock();

            if (!completed) {
                bool readerOwnsCall = false;
                {
                    std::lock_guard<std::mutex> pendingLock(pendingMutex);
                    if (pending.remove(reqId) == 0) readerOwnsCall = true;
                }
                if (readerOwnsCall) {
                    lock.lock();
                    completed = call->cv.wait_for(
                        lock,
                        std::chrono::milliseconds(250),
                        [&call] { return call->done; });
                    lock.unlock();
                }
            }

            std::lock_guard<std::mutex> callLock(call->mu);
            if (!completed) return 2;

            response.assign(call->data.begin(), call->data.end());
            if (call->status != 0) {
                if (!call->data.isEmpty())
                    MW_show_log("[Core error] " + QString::fromUtf8(call->data));
                return call->status;
            }
            return Client::CallOK;
        }
    };

    // -----------------------------------------------------------------------
    // Client
    // -----------------------------------------------------------------------

    Client::Client() = default;

    Client::~Client() {
        std::shared_ptr<ProtoRpcTcpChannel> old;
        {
            std::lock_guard<std::mutex> lock(channelMutex);
            old.swap(channel);
        }
    }

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
        const int portValue = target.mid(separator + 1).toInt(&portOk);
        if (!portOk || portValue < 1 || portValue > 65535) {
            MW_show_log("[RPC] invalid ProtoRPC/TCP target: " + target);
            return;
        }

        auto replacement = std::make_shared<ProtoRpcTcpChannel>();
        if (!replacement->Connect(host, static_cast<quint16>(portValue), 5000)) {
            MW_show_log("[RPC] failed to connect ProtoRPC/TCP target: " + target);
            return;
        }

        {
            std::lock_guard<std::mutex> lock(channelMutex);
            channel.swap(replacement);
        }
        MW_show_log("[RPC] ProtoRPC/TCP connected: " + target);
    }

    int Client::Call(const QString &methodName,
                     const std::string &request,
                     std::vector<uint8_t> &response,
                     int timeoutMs) const {
        std::shared_ptr<ProtoRpcTcpChannel> current;
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
