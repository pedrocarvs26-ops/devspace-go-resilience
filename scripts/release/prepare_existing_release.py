#!/usr/bin/env python3
"""Prepare Git sources and package an EXISTING Linux amd64 binary; never build/push."""
import argparse, gzip, hashlib, io, json, os, re, subprocess, tarfile
from datetime import datetime, timezone
from pathlib import Path


def command(root, *args):
    return subprocess.check_output(args, cwd=root, text=True, stderr=subprocess.STDOUT)


def digest(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def prepare(root, test_status):
    root = root.resolve()
    binary = root / 'devspace'
    expected = '15a23f3abc68c913da9806f81ea04c8e1529c40a9cc57ab1b2723f93008772b6'
    if digest(binary) != expected:
        raise RuntimeError('Binary changed since inspection; inspect it again before packaging')
    with binary.open('rb') as f:
        header = f.read(20)
    if header[:6] != b'\x7fELF\x02\x01' or int.from_bytes(header[18:20], 'little') != 62:
        raise RuntimeError('Expected an ELF64 little-endian x86-64 binary')
    version = re.search(r'const\s+Version\s*=\s*"([0-9.]+)"', (root/'internal/server/server.go').read_text()).group(1)
    tag = 'v' + version
    name = 'devspace-go-' + tag + '-linux-amd64'
    out = root/'release-assets'/tag
    if out.exists():
        raise RuntimeError('Release output already exists; refusing to overwrite it')
    buildinfo = command(root, 'go', 'version', '-m', str(binary))
    buildinfo = '\n'.join(buildinfo.splitlines()[1:]) + '\n'
    symbols = command(root, 'readelf', '--version-info', str(binary))
    versions = set(re.findall(r'GLIBC_([0-9.]+)', symbols))
    glibc = max(versions, key=lambda s: tuple(map(int, s.split('.'))))
    command(root, str(binary), 'help')

    ignore = (root/'.gitignore').read_text()
    ignore = ignore.replace('!tools/cloudflared.exe\n', '')
    extra = '\n# Release assets and local data (never source-control these)\n/release-assets/\n/dist/\n/tools/cloudflared\n/tools/cloudflared.exe\n.cloudflared/\n.env\n.env.*\n!.env.example\n*.pem\n*.key\n/config.json\n__pycache__/\n*.py[cod]\n.pytest_cache/\n'
    if '# Release assets and local data' not in ignore:
        (root/'.gitignore').write_text(ignore.rstrip()+'\n'+extra)
    attributes = root/'.gitattributes'
    if not attributes.exists():
        attributes.write_text('* text=auto\n*.go text eol=lf\n*.sh text eol=lf\n*.py text eol=lf\n*.md text eol=lf\n*.json text eol=lf\n*.yml text eol=lf\n*.ps1 text eol=crlf\n*.exe binary\n*.tar.gz binary\n*.zip binary\n')

    notes = f'''# Dev Space Go {tag} — Linux amd64

Precompiled server package using the existing user-built binary, without rebuilding.

## Included
- Linux x86-64 server; dynamic glibc dependency >= {glibc}.
- Example configuration, installation instructions and SHA-256 checksums.
- Async bash jobs with polling/cancellation and tunnel resilience changes.

## Validation
- Existing binary: `devspace help` succeeded on the preparation host.
- Source tests: {test_status}
- Earlier Notion MCP smoke test: incremental stdout/stderr and successful completion.
- No heavy-load/soak test, race detection, Windows or macOS acceptance is claimed.
- Binary has no embedded Git revision. Source-to-binary correspondence is not reproducibly proven.

## Important
- This is NOT a static/musl/Alpine binary. No GUI/Windows/macOS binaries are included.
- Personal configs, workspace state, credentials and third-party tunnel binaries are excluded.
- Install cloudflared or OpenSSH separately if remote access is needed.
- Hard CPU/memory/bandwidth limits require explicit configuration and platform support.
- Pinggy Free expires after 60 minutes; Quick Tunnel URLs may change and Quick Tunnels do not support SSE.
- Use `/mcp` with JSON/polling. Protect this remote shell endpoint; URL secrecy is not authentication.
- Recommend publishing as a GitHub prerelease until target-host acceptance is complete.
'''
    publishing = f'''# Publicar fontes e release

O executavel existente foi preservado. Nenhum commit, tag, remote ou push foi criado automaticamente.
Os fontes ficam no Git; os pacotes ficam em `release-assets/{tag}/`, fora do Git.

## Repositorio novo e vazio

Crie um repositorio vazio no GitHub (sem README gerado pelo site). Depois, na raiz:

```bash
git status
git diff --cached --stat
git commit -m "Prepare {tag} resilience release"
git remote add origin https://github.com/SEU_USUARIO/SEU_REPOSITORIO.git
git push -u origin main
```

Se o Git pedir identidade, configure seu nome/e-mail localmente. Nao coloque tokens em arquivos ou na URL do remote.

## Repositorio que ja existe

Esta pasta veio de ZIP e nao tinha historico Git. Nao use `push --force` para substituir um historico existente.
Prefira clonar seu repositorio em outra pasta, copiar apenas os fontes revisados (sem `.git`, configs, estado ou binarios),
revisar o diff e fazer commit na branch apropriada. O `git status` abaixo foi preparado somente para esta pasta.

## GitHub Release

Depois do commit/push, crie uma release no GitHub apontando para o commit correto e tag `{tag}`.
Use o texto em `docs/RELEASE_NOTES_{tag}.md`. Enquanto faltarem os testes de aceitacao, marque **pre-release**.
Anexe de `release-assets/{tag}/`:

- `{name}.tar.gz`
- `SHA256SUMS`
- `BUILDINFO.json`

Nao anexe seu `.devspace/config.json`, `.devspace-state`, credenciais, chaves ou arquivos pessoais.
Confira `sha256sum -c SHA256SUMS` nessa pasta antes de enviar.

O projeto recebido nao contem LICENSE. Nao foi inventada uma licenca; confirme a autorizacao/licenca aplicavel antes de redistribuir.
'''
    (root/'docs').mkdir(exist_ok=True)
    (root/'docs'/f'RELEASE_NOTES_{tag}.md').write_text(notes)
    (root/'docs'/'PUBLICAR_GITHUB.md').write_text(publishing)
    readme = root/'README.md'
    marker = '## Prepared precompiled release'
    if marker not in readme.read_text():
        readme.write_text(readme.read_text().rstrip()+f'\n\n{marker}\n\nSee [publishing instructions](docs/PUBLICAR_GITHUB.md) and [release notes](docs/RELEASE_NOTES_{tag}.md).\nThe current prepared asset is Linux amd64 with glibc >= {glibc}; other binaries must be built and tested separately.\n')
    validation = root/'docs/VALIDATION.md'
    if validation.exists() and '## Follow-up release preparation' not in validation.read_text():
        validation.write_text(validation.read_text().rstrip()+f'\n\n## Follow-up release preparation — 2026-09-06\n\nAn existing Linux amd64 binary was inspected on the user host and `devspace help` succeeded.\nSource tests: {test_status}\nSources were formatted with gofmt for publication. The existing binary was NOT rebuilt or replaced.\nThe original sandbox notes above remain historical; see RELEASE_NOTES_{tag}.md for current distribution limits.\n')
    command(root, 'gofmt', '-w', 'cmd', 'internal')

    if not (root/'.git').exists():
        command(root, 'git', 'init', '-b', 'main')
    if Path(command(root, 'git', 'rev-parse', '--show-toplevel').strip()).resolve() != root:
        raise RuntimeError('Refusing to stage files in an ancestor repository')
    allowed = ['.gitignore','.gitattributes','README.md','go.mod','go.sum','cmd','internal','docs','examples','readme','scripts']
    for optional in ['LICENSE','COPYING','NOTICE','.github']:
        if (root/optional).exists(): allowed.append(optional)
    tracked = command(root, 'git', 'ls-files', '--cached', '--others', '--exclude-standard', '--', *allowed).splitlines()
    secret = re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{60,}|AKIA[A-Z0-9]{16}')
    for relative in tracked:
        file = root/relative
        if file.is_symlink(): raise RuntimeError('Review symlink before publication: '+relative)
        if not file.is_file(): continue
        if file.stat().st_size > 2*1024*1024: raise RuntimeError('Unexpected large source file: '+relative)
        data = file.read_bytes()
        if data.startswith((b'\x7fELF',b'MZ')) or secret.search(data):
            raise RuntimeError('Review possible binary/secret before staging: '+relative)
    command(root, 'git', 'add', '--', *allowed)
    staged = command(root, 'git', 'diff', '--cached', '--name-only').splitlines()
    forbidden = ('.devspace/','.devspace-state/','.cloudflared/','release-assets/','build/','tools/cloudflared')
    for path in staged:
        if path == 'devspace' or path.startswith(forbidden) or path.endswith(('.pem','.key','.exe')):
            raise RuntimeError('Unexpected sensitive/generated file in index: '+path)
    command(root, 'git', 'diff', '--cached', '--check')

    source_hash = hashlib.sha256()
    for relative in sorted(staged):
        file = root/relative
        if file.is_file():
            source_hash.update(relative.encode()+b'\0'+file.read_bytes()+b'\0')
    config = {'host':'127.0.0.1','port':7676,'allowedRoots':['/path/to/your/project'],'publicBaseUrl':'http://127.0.0.1:7676','shell':'auto','toolMode':'full','toolNaming':'short','bashResourceLimit':{'enabled':False}}
    install = f'''Dev Space Go {tag} — Linux amd64 (glibc >= {glibc})

1. Extract this archive and open a terminal in its directory.
2. Verify: sha256sum -c SHA256SUMS
3. Run ./devspace init to configure the allowed project roots, then ./devspace.
   Alternatively copy config.example.json to .devspace/config.json and EDIT allowedRoots first.
4. Local health check: http://127.0.0.1:7676/healthz
5. MCP endpoint: http://127.0.0.1:7676/mcp

This package contains no GUI, cloudflared, SSH keys or personal config.
Install cloudflared or OpenSSH separately for remote access.
Do not run as root. Protect remote access with authentication compatible with your client.
An unprotected endpoint can read/write files and execute shell commands.
Use a restricted project directory, not your whole home directory, as an allowed root.

Jobs return an ID immediately; poll bash_status and use bash_cancel to stop.
Only the existing binary was packaged. It was NOT rebuilt; source linkage is not reproducibly proven.
See RELEASE_NOTES.md and BUILDINFO.txt for validation and compatibility details.
'''
    files = {'README.txt':install.encode(),'config.example.json':(json.dumps(config,indent=2)+'\n').encode(),
             'RELEASE_NOTES.md':notes.encode(),'BUILDINFO.txt':buildinfo.encode(),
             'SHA256SUMS':(expected+'  devspace\n').encode()}
    out.mkdir(parents=True)
    archive = out/(name+'.tar.gz')
    with archive.open('xb') as raw, gzip.GzipFile(filename='',fileobj=raw,mode='wb',mtime=0) as compressed, tarfile.open(fileobj=compressed,mode='w') as tar:
        items = {'devspace':binary.read_bytes(),**files}
        for relative,data in items.items():
            info=tarfile.TarInfo(name+'/'+relative);info.size=len(data);info.mode=0o755 if relative=='devspace' else 0o644
            info.mtime=0;info.uid=0;info.gid=0
            tar.addfile(info,io.BytesIO(data))
    with tarfile.open(archive,'r:gz') as tar:
        if set(tar.getnames()) != {name+'/'+x for x in items}: raise RuntimeError('Archive contents mismatch')
        if hashlib.sha256(tar.extractfile(name+'/devspace').read()).hexdigest()!=expected: raise RuntimeError('Binary changed in archive')
    if digest(binary)!=expected: raise RuntimeError('Original binary was modified')
    (out/'SHA256SUMS').write_text(digest(archive)+'  '+archive.name+'\n')
    metadata={'version_from_source':version,'platform':'linux/amd64','minimum_glibc':glibc,'binary_sha256':expected,
              'archive_sha256':digest(archive),'source_snapshot_sha256':source_hash.hexdigest(),'source_tests':test_status,
              'rebuilt':False,'binary_source_revision':'unknown; no embedded VCS revision','prepared_utc':datetime.now(timezone.utc).isoformat()}
    (out/'BUILDINFO.json').write_text(json.dumps(metadata,indent=2)+'\n')
    (out/'RELEASE_NOTES.md').write_text(notes)
    print(json.dumps({'release_directory':str(out),'archive':archive.name,'bytes':archive.stat().st_size,'staged_source_files':len(staged),'minimum_glibc':glibc,'binary_unchanged':True,'commit_created':False,'push_performed':False},indent=2))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=Path.cwd())
    parser.add_argument('--test-status',default='not executed during this preparation')
    args=parser.parse_args()
    prepare(args.root,args.test_status)
