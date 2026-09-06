# Publicar fontes e release

O executavel existente foi preservado. Nenhum commit, tag, remote ou push foi criado automaticamente.
Os fontes ficam no Git; os pacotes ficam em `release-assets/v2.2.0/`, fora do Git.

## Repositorio novo e vazio

Crie um repositorio vazio no GitHub (sem README gerado pelo site). Depois, na raiz:

```bash
git status
git diff --cached --stat
git commit -m "Prepare v2.2.0 resilience release"
git remote add origin https://github.com/SEU_USUARIO/SEU_REPOSITORIO.git
git push -u origin main
```

Se o Git pedir identidade, configure seu nome/e-mail localmente. Nao coloque tokens em arquivos ou na URL do remote.

## Repositorio que ja existe

Esta pasta veio de ZIP e nao tinha historico Git. Nao use `push --force` para substituir um historico existente.
Prefira clonar seu repositorio em outra pasta, copiar apenas os fontes revisados (sem `.git`, configs, estado ou binarios),
revisar o diff e fazer commit na branch apropriada. O `git status` abaixo foi preparado somente para esta pasta.

## GitHub Release

Depois do commit/push, crie uma release no GitHub apontando para o commit correto e tag `v2.2.0`.
Use o texto em `docs/RELEASE_NOTES_v2.2.0.md`. Enquanto faltarem os testes de aceitacao, marque **pre-release**.
Anexe de `release-assets/v2.2.0/`:

- `devspace-go-v2.2.0-linux-amd64.tar.gz`
- `SHA256SUMS`
- `BUILDINFO.json`

Nao anexe seu `.devspace/config.json`, `.devspace-state`, credenciais, chaves ou arquivos pessoais.
Confira `sha256sum -c SHA256SUMS` nessa pasta antes de enviar.

O projeto recebido nao contem LICENSE. Nao foi inventada uma licenca; confirme a autorizacao/licenca aplicavel antes de redistribuir.
