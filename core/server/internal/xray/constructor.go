package xray

import (
	"bytes"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

func buildXrayConfig(config string) (*core.Config, error) {
	patchedJSON, customOutbounds, err := prepareXrayCustomOutbounds(config)
	if err != nil {
		return nil, err
	}

	r := bytes.NewReader([]byte(patchedJSON))
	conf, err := serial.DecodeJSONConfig(r)
	if err != nil {
		return nil, err
	}

	built, err := conf.Build()
	if err != nil {
		return nil, err
	}
	if err := patchXrayCustomOutbounds(built, customOutbounds); err != nil {
		return nil, err
	}
	return built, nil
}

func CreateXrayInstance(config string) (*core.Instance, error) {
	built, err := buildXrayConfig(config)
	if err != nil {
		return nil, err
	}

	server, err := core.New(built)
	if err != nil {
		return nil, err
	}

	return server, nil
}

// CheckXrayConfig validates an Xray JSON config without creating a running
// instance. It also validates Throne's internal Hysteria2 marker and patches
// the compiled config with the registered typed outbound message, but stops
// short of core.New so concurrent validation does not instantiate handlers.
func CheckXrayConfig(config string) error {
	_, err := buildXrayConfig(config)
	return err
}
