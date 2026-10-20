package main

import (
	"ThroneCore/gen"
	"ThroneCore/internal/buildidentity"
	"os"
	"runtime"
	"runtime/debug"

	C "github.com/sagernet/sing-box/constant"
	"github.com/xtls/xray-core/core"
	"google.golang.org/protobuf/proto"
)

func populateBuildIdentity(out *gen.IsPrivilegedResponse) {
	info, _ := debug.ReadBuildInfo()
	commit, modified := buildidentity.Revision(info)
	out.SingBoxVersion = proto.String(C.Version)
	out.XrayVersion = proto.String(core.Version())
	out.SourceCommit = proto.String(commit)
	out.SourceModified = proto.String(modified)
	out.Architecture = proto.String(runtime.GOOS + "/" + runtime.GOARCH)
	out.ProcessId = proto.Int32(int32(os.Getpid()))
	out.GoVersion = proto.String(runtime.Version())
}
