package boxmain

import "testing"

// Check must register the extension before JSON parsing, as the normal RPC
// Create path does. Registering only inside Box.New would be too late.
func TestCheckRoundRobinConfiguration(t *testing.T) {
	err := Check([]byte(`{
  "outbounds": [
   {"type":"socks","tag":"member-a","server":"127.0.0.1","server_port":31001},
   {"type":"socks","tag":"member-b","server":"127.0.0.1","server_port":31002},
   {"type":"auto-selector-round-robin","tag":"proxy","outbounds":["member-a","member-b"],"balance":true,"balance_mode":"round-robin","expected":2},
   {"type":"auto-selector","tag":"ai-proxy","outbounds":["member-a"],"balance":true,"balance_mode":"connection"}
  ], "route":{"final":"proxy"}
 }`))
	if err != nil {
		t.Fatal(err)
	}
}
