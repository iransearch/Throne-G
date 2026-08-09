#pragma once
#include "include/configs/common/Outbound.h"
#include "include/configs/common/TLS.h"

namespace Configs
{
    class hysteria : public outbound
    {
        public:
        QString protocol_version = "1";
        QStringList server_ports;
        QString hop_interval;
        int up_mbps = 0;
        int down_mbps = 0;
        QString obfs;

        // Throne can run Hysteria2 either through sing-box (default) or through
        // its embedded Xray instance using ThroneCore's custom Xray outbound.
        QString core = "sing-box";

        // Hysteria1
        QString auth_type;
        QString auth;
        int recv_window_conn = 0;
        int recv_window = 0;
        bool disable_mtu_discovery = false;

        // Hysteria2
        QString password;
        QString obfs_type = "salamander";

        std::shared_ptr<TLS> tls = std::make_shared<TLS>();

        hysteria()
        {
            tls->utls->supported = false;
        }

        bool IsXray() override {
            return protocol_version == "2" && core == "xray";
        }

        bool HasTLS() override {
            return true;
        }

        bool MustTLS() override {
            return true;
        }

        std::shared_ptr<TLS> GetTLS() override {
            return tls;
        }

        // baseConfig overrides
        bool ParseFromLink(const QString& link) override;
        bool ParseFromJson(const QJsonObject& object) override;
        bool ParseFromClash(const clash::Proxies& object) override;
        QString ExportToLink() override;
        QJsonObject ExportToJson() override;
        QJsonObject ExportIdentity() override;
        BuildResult Build() override;
        BuildResult BuildXray() override;

        QString DisplayType() override;
        SecurityInfo GetSecurity() override;
    };
}
