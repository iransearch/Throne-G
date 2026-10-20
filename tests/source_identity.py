"""Build real Core identity code inside a different Git repository, as Windows staging does."""
from pathlib import Path
import argparse, json, os, shutil, subprocess, tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--go', required=True)
parser.add_argument('--core-source', required=True)
args = parser.parse_args()
go = str(Path(args.go).resolve())
identity_source = Path(args.core_source)/'core/server/internal/buildidentity/identity.go'

def checked(command, cwd, env=None):
    return subprocess.check_output(command, cwd=cwd, env=env, text=True, stderr=subprocess.STDOUT).strip()

def commit(directory, file):
    checked(['git', 'init', '-q'], directory)
    checked(['git', 'add', file], directory)
    checked(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'Fixture'], directory)
    return checked(['git', 'rev-parse', 'HEAD'], directory)

with tempfile.TemporaryDirectory(prefix='exact-core-source-') as temporary:
    root = Path(temporary)
    selected = root/'selected'; selected.mkdir()
    (selected/'identity.go').write_bytes(identity_source.read_bytes())
    selected_commit = commit(selected, 'identity.go')
    builder = root/'builder'; builder.mkdir(); (builder/'builder.txt').write_text('different repository')
    builder_commit = commit(builder, 'builder.txt')
    stage = builder/'.core-builder/staging/core/server'; stage.mkdir(parents=True)
    (stage/'go.mod').write_text('module ThroneCore\n\ngo 1.26.0\n')
    package = stage/'internal/buildidentity'; package.mkdir(parents=True)
    shutil.copyfile(selected/'identity.go', package/'identity.go')
    (stage/'main.go').write_text('''package main
import ("encoding/json"; "os"; "fmt"; runtimeDebug "runtime/debug"; "ThroneCore/internal/buildidentity")
func main() { info,_:=runtimeDebug.ReadBuildInfo(); if len(os.Args)>1 { fmt.Println(buildidentity.Report(info)); return }; commit,modified:=buildidentity.Revision(info); json.NewEncoder(os.Stdout).Encode([]string{commit,modified}) }
''')
    env = {**os.environ, 'GOTOOLCHAIN':'local', 'CGO_ENABLED':'0'}
    unstamped = root/'unstamped'
    checked([go, 'build', '-buildvcs=true', '-o', str(unstamped), '.'], stage, env)
    result = json.loads(checked([str(unstamped)], stage))
    assert result == [builder_commit, 'true'], result
    print('PASS original staging reproduces the wrong enclosing-builder commit')
    flags = '-w -s -X ThroneCore/internal/buildidentity.SourceCommit='+selected_commit+' -X ThroneCore/internal/buildidentity.SourceModified=false'
    stamped = root/'stamped'
    checked([go, 'build', '-trimpath', '-buildvcs=false', '-ldflags', flags, '-o', str(stamped), '.'], stage, env)
    result = json.loads(checked([str(stamped)], stage))
    assert result == [selected_commit, 'false'], result
    assert checked([str(stamped), '-build-info'], stage) == 'CORE_BUILD_IDENTITY commit='+selected_commit+' modified=false'
    print('PASS running executable and builder report return selected-source commit and clean state')
    for architecture in ['amd64', '386']:
        output = root/('windows-'+architecture+'.exe')
        target_env = {**env, 'GOOS':'windows', 'GOARCH':architecture}
        checked([go, 'build', '-trimpath', '-buildvcs=false', '-ldflags', flags, '-o', str(output), '.'], stage, target_env)
        metadata = checked([go, 'version', '-m', str(output)], stage, env)
        assert selected_commit.encode('ascii') in output.read_bytes()
        assert 'vcs.revision' not in metadata
        print('PASS Windows '+architecture+' embeds exact stamps without parent Git metadata')
