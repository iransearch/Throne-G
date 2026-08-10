#include "include/ui/profile/edit_hysteria.h"

#include <QTimer>

EditHysteria::EditHysteria(QWidget *parent)
    : QWidget(parent),
      ui(new Ui::EditHysteria) {
    ui->setupUi(this);

    _protocol_version = ui->protocol_version;
}

EditHysteria::~EditHysteria() {
    delete ui;
}

void EditHysteria::onStart(std::shared_ptr<Configs::Profile> _ent) {
    this->ent = _ent;
    auto outbound = _ent->Hysteria();

    ui->protocol_version->setCurrentText(outbound->protocol_version);
    ui->core->setCurrentText(outbound->core == "xray" ? "xray" : "sing-box");
    ui->server_ports->setText(outbound->server_ports.join(","));
    ui->hop_interval->setText(outbound->hop_interval);
    ui->up_mbps->setText(Int2String(outbound->up_mbps));
    ui->down_mbps->setText(Int2String(outbound->down_mbps));
    ui->obfs_type->setCurrentText(outbound->obfs_type.isEmpty() ? "salamander" : outbound->obfs_type);
    ui->obfs->setText(outbound->obfs);
    ui->auth_type->setCurrentText(outbound->auth_type);
    ui->auth->setText(outbound->auth);
    ui->recv_window->setText(Int2String(outbound->recv_window));
    ui->recv_window_conn->setText(Int2String(outbound->recv_window_conn));
    ui->disable_mtu_discovery->setChecked(outbound->disable_mtu_discovery);
    ui->password->setText(outbound->password);
    editHysteriaLayout(outbound->protocol_version);

    // Hysteria owns its transport and TLS settings even when the selected core
    // is Xray. The generic Xray stream editor (network/security/mux) is not part
    // of the throne-hysteria2 outbound and only exposes misleading controls.
    // DialogEditProfile decides which core widgets to show before onStart(), so
    // apply the Hysteria-specific chrome after that setup has completed.
    QTimer::singleShot(0, this, [this] {
        if (!get_edit_dialog) return;
        auto *dialog = get_edit_dialog();
        if (dialog == nullptr) return;

        if (auto *widget = dialog->findChild<QWidget *>("xray_settings_box")) widget->hide();
        if (auto *widget = dialog->findChild<QWidget *>("xray_widget")) widget->hide();

        // Keep the normal Hysteria TLS controls visible. Individual Network /
        // Security / Mux rows are still filtered by DialogEditProfile according
        // to HasTransport(), HasTLS() and HasMux().
        if (auto *widget = dialog->findChild<QWidget *>("stream_box")) widget->show();
        if (auto *widget = dialog->findChild<QWidget *>("right_all_w")) widget->show();

        dialog->adjustSize();
    });
}

bool EditHysteria::onEnd() {
    auto outbound = ent->Hysteria();
    outbound->protocol_version = ui->protocol_version->currentText();
    outbound->core = outbound->protocol_version == "2" && ui->core->currentText() == "xray" ? "xray" : "sing-box";
    outbound->server_ports = SplitAndTrim(ui->server_ports->text(), ",", false);
    outbound->hop_interval = ui->hop_interval->text();
    outbound->up_mbps = ui->up_mbps->text().toInt();
    outbound->down_mbps = ui->down_mbps->text().toInt();
    outbound->obfs_type = ui->obfs_type->currentText();
    outbound->obfs = ui->obfs->text();
    outbound->auth_type = ui->auth_type->currentText();
    outbound->auth = ui->auth->text();
    outbound->recv_window = ui->recv_window->text().toInt();
    outbound->recv_window_conn = ui->recv_window_conn->text().toInt();
    outbound->disable_mtu_discovery = ui->disable_mtu_discovery->isChecked();
    outbound->password = ui->password->text();
    return true;
}

void EditHysteria::editHysteriaLayout(const QString& version) {
    if (version == "1")
    {
        ui->core->setVisible(false);
        ui->core_l->setVisible(false);
        ui->obfs_type->setVisible(false);
        ui->obfs_type_l->setVisible(false);
        ui->auth_type->setVisible(true);
        ui->auth_type_l->setVisible(true);
        ui->auth->setVisible(true);
        ui->auth_l->setVisible(true);
        ui->recv_window_conn->setVisible(true);
        ui->recv_window_conn_l->setVisible(true);
        ui->recv_window->setVisible(true);
        ui->recv_window_l->setVisible(true);
        ui->disable_mtu_discovery->setVisible(true);
        ui->password->setVisible(false);
        ui->password_l->setVisible(false);
    } else
    {
        ui->core->setVisible(true);
        ui->core_l->setVisible(true);
        ui->obfs_type->setVisible(true);
        ui->obfs_type_l->setVisible(true);
        ui->auth_type->setVisible(false);
        ui->auth_type_l->setVisible(false);
        ui->auth->setVisible(false);
        ui->auth_l->setVisible(false);
        ui->recv_window_conn->setVisible(false);
        ui->recv_window_conn_l->setVisible(false);
        ui->recv_window->setVisible(false);
        ui->recv_window_l->setVisible(false);
        ui->disable_mtu_discovery->setVisible(false);
        ui->password->setVisible(true);
        ui->password_l->setVisible(true);
    }
}
